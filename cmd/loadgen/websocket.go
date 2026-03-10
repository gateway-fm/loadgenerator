package main

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/websocket"

	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// connectPreconfWS connects to the preconfirmation WebSocket.
func (lg *LoadGenerator) connectPreconfWS() {
	defer func() {
		if r := recover(); r != nil {
			lg.logger.Error("Preconf WS goroutine PANIC", "panic", r)
		}
	}()

	lg.preconfWsConnMu.Lock()
	if lg.preconfWsConn != nil {
		lg.preconfWsConnMu.Unlock()
		return
	}

	conn, _, err := websocket.DefaultDialer.Dial(lg.cfg.PreconfWSURL, nil)
	if err != nil {
		lg.preconfWsConnMu.Unlock()
		lg.logger.Error("failed to connect to preconf WebSocket", "error", err)
		return
	}
	lg.preconfWsConn = conn
	lg.preconfWsConnMu.Unlock()

	lg.logger.Info("connected to preconf WebSocket", "url", lg.cfg.PreconfWSURL)

	// Read messages
	for {
		select {
		case <-lg.ctx.Done():
			return
		default:
		}

		// Check connection is still valid (may have been cleared by error)
		lg.preconfWsConnMu.Lock()
		if lg.preconfWsConn == nil {
			lg.preconfWsConnMu.Unlock()
			return
		}
		lg.preconfWsConnMu.Unlock()

		// Set read deadline to prevent hanging on disconnected server.
		// This allows the goroutine to check ctx.Done() periodically.
		if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			lg.logger.Error("preconf websocket SetReadDeadline failed", "error", err)
			lg.preconfWsConnMu.Lock()
			lg.preconfWsConn = nil
			lg.preconfWsConnMu.Unlock()
			return
		}

		// Use PreconfMessage which handles both batched and single events
		var msg types.PreconfMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			// Any read error (except timeout) means connection is dead - clear it to prevent repeated reads
			clearConn := true

			// Check for timeout - this is expected, just continue (don't clear connection)
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				clearConn = false
			}

			if clearConn {
				lg.preconfWsConnMu.Lock()
				lg.preconfWsConn = nil
				lg.preconfWsConnMu.Unlock()
			}

			// Check if it's a websocket close
			if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				lg.logger.Error("preconf websocket closed", "error", err)
				return
			}
			// Check for context cancellation
			if lg.ctx.Err() != nil {
				return
			}
			// Timeout - continue the loop
			if !clearConn {
				continue
			}
			lg.logger.Error("preconf websocket read error", "error", err)
			return
		}

		// Log batch info for debugging
		if msg.IsBatch() {
			lg.logger.Info("received batched preconf event",
				"type", msg.Type,
				"eventCount", len(msg.Events),
				"blockNumber", msg.BlockNumber,
				"seqNum", msg.SeqNum)
		}

		// Check for sequence gaps (indicates dropped events on the server)
		if msg.SeqNum > 0 {
			expected := atomic.LoadUint64(&lg.lastPreconfSeqNum) + 1
			if expected > 1 && msg.SeqNum > expected {
				gapSize := msg.SeqNum - expected
				totalGaps := atomic.AddUint64(&lg.preconfGaps, gapSize)
				lg.logger.Warn("preconf sequence gap detected - events were dropped",
					"expected", expected,
					"received", msg.SeqNum,
					"gapSize", gapSize,
					"totalGaps", totalGaps)
			}
			atomic.StoreUint64(&lg.lastPreconfSeqNum, msg.SeqNum)
		}

		// Process all events (handles both batch and single event formats)
		events := msg.GetEvents()
		for _, event := range events {
			lg.processPreconfEvent(event)
		}
	}
}

// processPreconfEvent handles a single preconfirmation event.
// Implements Flashblocks-compliant preconfirmation lifecycle:
//   - pending: TX received and queued by sequencer
//   - preconfirmed: TX selected for block (sequencer COMMITMENT)
//   - confirmed: TX included in finalized block
//   - revoked: Preconfirmation broken (execution layer rejected TX)
//   - dropped: TX permanently dropped
//   - requeued: TX requeued for later block
func (lg *LoadGenerator) processPreconfEvent(event *types.PreconfEvent) {
	if event == nil || event.TxHash == "" {
		lg.logger.Debug("ignoring empty preconf event")
		return
	}

	txHash := common.HexToHash(event.TxHash)
	now := time.Now()

	switch event.Status {
	case types.PreconfStagePending:
		// TX received and queued - acknowledgment from sequencer
		lg.metricsCol.RecordPending(txHash, now)

	case types.PreconfStagePreconfirmed:
		// TX selected for inclusion in a block - THIS is the preconfirmation!
		// The sequencer commits to including this TX in the next block
		lg.metricsCol.RecordPreconfirmed(txHash, now)
		// Also track in the legacy preconfLatencies for backwards compatibility
		if sentTime, ok := lg.metricsCol.GetTxSentTime(txHash); ok {
			latency := float64(now.Sub(sentTime).Milliseconds())
			lg.preconfLatencies.Add(latency)
		}
		// Record preconf in TX log (non-blocking)
		lg.recordTxPreconfirmed(txHash, now)

	case types.PreconfStageConfirmed:
		// Check if this confirmation is beyond the test end block (grace period arrival).
		// testEndBlockNumber is protected by blockMetricsMu; read it once under the lock.
		lg.blockMetricsMu.Lock()
		endBlock := lg.testEndBlockNumber
		lg.blockMetricsMu.Unlock()
		beyondTestEnd := endBlock > 0 && event.BlockNumber > endBlock

		if beyondTestEnd {
			// Block is beyond on-chain verification range. Record the flow stage
			// to keep TxFlowTracker accurate and prevent memory leaks, but don't
			// inflate the confirmed counter or record latency stats.
			lg.metricsCol.RecordTxConfirmedFlowOnly(txHash, now)
		} else {
			// Normal path: record full confirmation (counter + latency + flow)
			lg.metricsCol.RecordTxConfirmed(txHash, now)
		}
		metrics.AtomicSubSaturating(&lg.pendingCount, 1)
		// Record confirmation in TX log (non-blocking)
		lg.recordTxConfirmed(txHash, now)
		// Track recent confirmed TX for incremental verification (keep last 1000)
		// Skip for blocks beyond test end since verification won't cover them.
		if !beyondTestEnd {
			lg.recentConfirmedMu.Lock()
			lg.recentConfirmedHashes = append(lg.recentConfirmedHashes, event.TxHash)
			currentCount := len(lg.recentConfirmedHashes)
			if currentCount > 1000 {
				lg.recentConfirmedHashes = lg.recentConfirmedHashes[currentCount-1000:]
			}
			lg.recentConfirmedMu.Unlock()
		}
		// Track block number from confirmation event (fallback when L2 WebSocket unavailable)
		if event.BlockNumber > 0 && !beyondTestEnd {
			lg.recentBlockNumbersMu.Lock()
			// Only append if this is a new block number (avoid duplicates from batched events)
			blockCount := len(lg.recentBlockNumbers)
			if blockCount == 0 || lg.recentBlockNumbers[blockCount-1] != event.BlockNumber {
				lg.recentBlockNumbers = append(lg.recentBlockNumbers, event.BlockNumber)
				if len(lg.recentBlockNumbers) > 1000 {
					lg.recentBlockNumbers = lg.recentBlockNumbers[len(lg.recentBlockNumbers)-1000:]
				}
			}
			lg.recentBlockNumbersMu.Unlock()
		}

	case types.PreconfStageRevoked:
		// Preconfirmation was broken - execution layer rejected the TX
		// This means we told the user "your TX will be included" but it wasn't
		lg.metricsCol.RecordRevoked(txHash, now)
		atomic.AddInt64(&lg.recentRevocations, 1) // Circuit breaker tracking
		lg.logger.Warn("preconfirmation revoked", "txHash", event.TxHash, "block", event.BlockNumber)

	case types.PreconfStageDropped:
		// TX permanently dropped - won't be retried
		lg.metricsCol.RecordDropped(txHash, now)
		metrics.AtomicSubSaturating(&lg.pendingCount, 1)

	case types.PreconfStageRequeued:
		// TX requeued for later block - nonce gap or other temporary issue
		lg.metricsCol.RecordRequeued(txHash, now)
	}
}

// connectBuilderMetricsWS connects to the builder's block metrics WebSocket.
// This provides per-block timing breakdown (filter, Engine API), rejection stats, and fill rate.
func (lg *LoadGenerator) connectBuilderMetricsWS() {
	defer func() {
		if r := recover(); r != nil {
			lg.logger.Error("BlockMetrics WS goroutine PANIC", "panic", r)
		}
	}()

	// Derive block metrics URL from preconf URL (same server, different endpoint)
	wsURL := strings.Replace(lg.cfg.PreconfWSURL, "/ws/preconfirmations", "/ws/block-metrics", 1)

	lg.builderMetricsWsConnMu.Lock()
	if lg.builderMetricsWsConn != nil {
		lg.builderMetricsWsConnMu.Unlock()
		return
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		lg.builderMetricsWsConnMu.Unlock()
		lg.logger.Warn("failed to connect to builder metrics WebSocket", "error", err, "url", wsURL)
		return
	}
	lg.builderMetricsWsConn = conn
	lg.builderMetricsWsConnMu.Unlock()

	lg.logger.Info("connected to builder metrics WebSocket", "url", wsURL)

	// Read messages
	for {
		select {
		case <-lg.ctx.Done():
			return
		default:
		}

		// Check connection is still valid (may have been cleared by error)
		lg.builderMetricsWsConnMu.Lock()
		if lg.builderMetricsWsConn == nil {
			lg.builderMetricsWsConnMu.Unlock()
			return
		}
		lg.builderMetricsWsConnMu.Unlock()

		// Set read deadline to prevent hanging on disconnected server
		if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			lg.logger.Error("builder metrics websocket SetReadDeadline failed", "error", err)
			lg.builderMetricsWsConnMu.Lock()
			lg.builderMetricsWsConn = nil
			lg.builderMetricsWsConnMu.Unlock()
			return
		}

		var event types.BuilderBlockMetrics
		err := conn.ReadJSON(&event)
		if err != nil {
			// Any read error (except timeout) means connection is dead - clear it to prevent repeated reads
			clearConn := true

			// Check for timeout - this is expected, just continue (don't clear connection)
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				clearConn = false
			}

			if clearConn {
				lg.builderMetricsWsConnMu.Lock()
				lg.builderMetricsWsConn = nil
				lg.builderMetricsWsConnMu.Unlock()
			}

			// Check if it's a websocket close
			if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				lg.logger.Error("builder metrics websocket closed", "error", err)
				return
			}
			// Check for context cancellation
			if lg.ctx.Err() != nil {
				return
			}
			// Timeout - continue the loop
			if !clearConn {
				continue
			}
			lg.logger.Error("builder metrics websocket read error", "error", err)
			return
		}

		// Process the block metrics event
		lg.processBuilderBlockMetrics(&event)
	}
}

// processBuilderBlockMetrics handles a block metrics event from the builder.
// This provides more detailed timing information than L2 newHeads.
func (lg *LoadGenerator) processBuilderBlockMetrics(event *types.BuilderBlockMetrics) {
	if event == nil {
		return
	}

	// Only record block metrics when test is actually running (not during init or after stop)
	// This prevents counting blocks from before/after the test period
	lg.statusMu.RLock()
	status := lg.status
	lg.statusMu.RUnlock()
	if status != types.StatusRunning {
		return
	}

	// Update block metrics tracking with the enhanced data
	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	// Skip blocks after the test end (blocks can arrive during grace period)
	// testEndBlockNumber is set when StopTest is called, before status changes
	if lg.testEndBlockNumber > 0 && event.BlockNumber > lg.testEndBlockNumber {
		lg.logger.Debug("skipping block after test end",
			"block", event.BlockNumber,
			"testEnd", lg.testEndBlockNumber)
		return
	}

	// Track first block number
	if lg.firstBlockNumber == 0 && event.BlockNumber > 0 {
		lg.firstBlockNumber = event.BlockNumber
	}

	// Skip if block was already recorded (prevents double-counting if both builder and L2 WS are active)
	// This must be checked BEFORE updating cumulative gas, rolling window, etc.
	now := time.Now()
	if event.BlockNumber <= lg.lastRecordedBlock {
		// Block already recorded - skip to avoid double-counting
		return
	}

	// Update cumulative metrics (only for new blocks)
	lg.cumulativeGasUsed += event.GasUsed
	lg.cumulativeGasLimit += event.GasLimit
	lg.totalBlockCount++

	lg.lastBlockNumber = event.BlockNumber

	// Update RPC fallback tracking to prevent double-counting
	// When we receive blocks via WebSocket, the RPC fallback shouldn't re-count them
	if lg.rpcLastBlockNumber < event.BlockNumber {
		lg.rpcLastBlockNumber = event.BlockNumber
	}

	// Add to block metrics slice (for time series)
	if !lg.lastBlockTime.IsZero() {
		blockTime := now.Sub(lg.lastBlockTime)
		lg.blockMetrics = append(lg.blockMetrics, blockMetricsPoint{
			timestamp:            now,
			gasUsed:              event.GasUsed,
			gasLimit:             event.GasLimit,
			blockNumber:          event.BlockNumber,
			txCount:              event.TxCount,
			blockTime:            blockTime.Seconds(), // seconds since last block (used by getBlockMetricsForPeriod)
			blockTimeMs:          blockTime.Milliseconds(),
			filterDurationMs:     event.FilterDurationMs,
			engineApiDurationMs:  event.EngineApiDurationMs,
			totalBuildDurationMs: event.TotalBuildDurationMs,
		})
	}
	lg.lastRecordedBlock = event.BlockNumber
	lg.lastBlockTime = now

	// Update rolling window for MGas/s calculation
	lg.rollingGasWindow = append(lg.rollingGasWindow, rollingGasPoint{
		timestamp: now,
		gasUsed:   event.GasUsed,
	})

	// Update rolling window for TX/s calculation (aligned with gas window)
	lg.rollingTxWindow = append(lg.rollingTxWindow, rollingTxPoint{
		timestamp: now,
		txCount:   event.TxCount,
	})
}

// connectL2WS connects to L2 WebSocket for newHeads subscription (block metrics).
func (lg *LoadGenerator) connectL2WS() {
	defer func() {
		if r := recover(); r != nil {
			lg.logger.Error("L2 WS goroutine PANIC", "panic", r)
		}
	}()

	// Derive WebSocket URL from HTTP URL if not explicitly configured
	wsURL := lg.cfg.L2WSURL
	if wsURL == "" {
		// Convert http://host:port to ws://host:port
		wsURL = lg.cfg.L2RPCURL
		if len(wsURL) > 7 && wsURL[:7] == "http://" {
			wsURL = "ws://" + wsURL[7:]
		} else if len(wsURL) > 8 && wsURL[:8] == "https://" {
			wsURL = "wss://" + wsURL[8:]
		}
	}

	lg.l2WsConnMu.Lock()
	if lg.l2WsConn != nil {
		lg.l2WsConnMu.Unlock()
		return
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		lg.l2WsConnMu.Unlock()
		lg.logger.Warn("failed to connect to L2 WebSocket for block metrics", "error", err, "url", wsURL)
		return
	}
	lg.l2WsConn = conn
	lg.l2WsConnMu.Unlock()

	lg.logger.Info("connected to L2 WebSocket for block metrics", "url", wsURL)

	// Subscribe to newHeads
	subscribeMsg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_subscribe",
		"params":  []string{"newHeads"},
		"id":      1,
	}
	if err := conn.WriteJSON(subscribeMsg); err != nil {
		lg.logger.Error("failed to subscribe to newHeads", "error", err)
		return
	}

	// Read messages
	for {
		select {
		case <-lg.ctx.Done():
			return
		default:
		}

		// Check connection is still valid (may have been cleared by error)
		lg.l2WsConnMu.Lock()
		if lg.l2WsConn == nil {
			lg.l2WsConnMu.Unlock()
			return
		}
		lg.l2WsConnMu.Unlock()

		var msg struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  *struct {
				Result struct {
					Number    string `json:"number"`
					GasUsed   string `json:"gasUsed"`
					GasLimit  string `json:"gasLimit"`
					Timestamp string `json:"timestamp"`
				} `json:"result"`
			} `json:"params"`
		}

		if err := conn.ReadJSON(&msg); err != nil {
			// Clear connection to prevent repeated reads on failed connection
			lg.l2WsConnMu.Lock()
			lg.l2WsConn = nil
			lg.l2WsConnMu.Unlock()
			lg.logger.Debug("L2 WebSocket read error", "error", err)
			return
		}

		// Handle newHead notification
		if msg.Params != nil {
			lg.processNewHead(msg.Params.Result.Number, msg.Params.Result.GasUsed, msg.Params.Result.GasLimit)
		}
	}
}

// processNewHead handles a new block header from L2 WebSocket.
func (lg *LoadGenerator) processNewHead(numberHex, gasUsedHex, gasLimitHex string) {
	// Parse hex values
	blockNumber, _ := parseHexUint64(numberHex)
	gasUsed, _ := parseHexUint64(gasUsedHex)
	gasLimit, _ := parseHexUint64(gasLimitHex)

	now := time.Now()

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	// Track first block number seen during test
	if lg.firstBlockNumber == 0 {
		lg.firstBlockNumber = blockNumber
	}

	// Calculate block time
	var blockTime float64
	if !lg.lastBlockTime.IsZero() && blockNumber > lg.lastBlockNumber {
		blockTime = now.Sub(lg.lastBlockTime).Seconds()
	}

	// Store block metrics (only if not already recorded by builder WebSocket)
	// This prevents double-counting when both builder and L2 WebSocket are active
	if blockNumber > lg.lastRecordedBlock {
		point := blockMetricsPoint{
			timestamp:   now,
			blockNumber: blockNumber,
			gasUsed:     gasUsed,
			gasLimit:    gasLimit,
			blockTime:   blockTime,
		}
		lg.blockMetrics = append(lg.blockMetrics, point)
		lg.lastRecordedBlock = blockNumber
	}

	lg.lastBlockTime = now
	lg.lastBlockNumber = blockNumber

	// Track recent block for incremental verification (keep last 1000)
	lg.recentBlockNumbersMu.Lock()
	lg.recentBlockNumbers = append(lg.recentBlockNumbers, blockNumber)
	if len(lg.recentBlockNumbers) > 1000 {
		lg.recentBlockNumbers = lg.recentBlockNumbers[len(lg.recentBlockNumbers)-1000:]
	}
	lg.recentBlockNumbersMu.Unlock()
}