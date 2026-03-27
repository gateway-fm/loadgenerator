package loadgen

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

const rollingWindowDuration = 5 * time.Second // 5-second rolling window for MGas/s and TX/s

// recordTimeSeriesPoint records a time-series sample (non-blocking).
func (lg *LoadGenerator) recordTimeSeriesPoint() {
	if lg.startTime.IsZero() {
		return
	}

	snapshot := lg.metricsCol.GetSnapshot()
	elapsed := time.Since(lg.startTime).Milliseconds()

	// Calculate average block time BEFORE getBlockMetricsForPeriod clears the array
	avgBlockTimeMs := lg.calculateAvgBlockTimeMs()

	// Get block metrics for this period (also updates cumulativeGasUsed and rollingGasWindow)
	// NOTE: This clears the blockMetrics array, so avgBlockTimeMs must be calculated first!
	gasUsed, gasLimit, blockCount, _, fillRate := lg.getBlockMetricsForPeriod()

	// Calculate MGas/s using rolling window for a SMOOTH chart
	mgasPerSec := lg.calculateRollingMgasPerSec()

	// Track peak Mgas/s - but only after 1 second warmup to avoid startup spikes
	// The rolling window produces inflated values when it only has 1-2 entries
	// because the minimum duration cap (200ms) creates artificial spikes
	testDuration := time.Since(lg.startTime)
	lg.blockMetricsMu.Lock()
	if mgasPerSec > lg.peakMgasPerSec && testDuration > 1*time.Second {
		lg.peakMgasPerSec = mgasPerSec
	}
	lg.blockMetricsMu.Unlock()

	// Get gas pricing data
	lg.builderPressureMu.RLock()
	baseFeeGwei := lg.latestBaseFeeGwei
	gasPriceGwei := lg.latestGasPriceGwei
	lg.builderPressureMu.RUnlock()

	point := storage.TimeSeriesPoint{
		TimestampMs:  elapsed,
		TxSent:       snapshot.TxSent,
		TxConfirmed:  snapshot.TxConfirmed,
		TxFailed:     snapshot.TxFailed,
		CurrentTPS:   lg.currentTPS,
		TargetTPS:    int(atomic.LoadInt64(&lg.currentRate)),
		PendingCount: atomic.LoadInt64(&lg.pendingCount),
		// Block metrics
		GasUsed:    gasUsed,
		GasLimit:   gasLimit,
		BlockCount: blockCount,
		MgasPerSec: mgasPerSec, // Rolling window average for smooth chart
		FillRate:   fillRate,
		// Block timing and gas pricing (for historical mode)
		AvgBlockTimeMs: avgBlockTimeMs,
		BaseFeeGwei:    baseFeeGwei,
		GasPriceGwei:   gasPriceGwei,
	}

	lg.timeSeriesBuf = append(lg.timeSeriesBuf, point)
}

// calculateOnChainMetrics queries the chain for actual block metrics between first and last block.
// This provides ground-truth metrics independent of WebSocket events which may have been lost.
func (lg *LoadGenerator) calculateOnChainMetrics(ctx context.Context) onChainMetricsResult {
	lg.blockMetricsMu.Lock()
	firstBlock := lg.firstBlockNumber
	lastBlock := lg.lastBlockNumber
	testStartBlock := lg.testStartBlockNumber
	testEndBlock := lg.testEndBlockNumber
	lg.blockMetricsMu.Unlock()

	// Prefer testStartBlockNumber (recorded at test start via RPC) over WebSocket-based firstBlockNumber
	// This ensures we count from when the test actually started, not when WebSocket first received a block
	if testStartBlock > 0 {
		firstBlock = testStartBlock
		lg.logger.Info("using test start block for verification", "block", firstBlock)
	} else if firstBlock == 0 {
		lg.logger.Warn("no start block recorded (WebSocket and RPC both failed)")
	}

	// Use testEndBlockNumber (recorded at test stop) to ensure we only count
	// blocks up to when the test ended - excluding pending txs confirmed later
	if testEndBlock > 0 {
		lastBlock = testEndBlock
		lg.logger.Info("using recorded test end block", "block", lastBlock)
	} else if lastBlock == 0 && lg.l2Client != nil {
		// Fallback: query current block if no end block recorded
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if endBlock, err := lg.l2Client.GetBlockNumber(queryCtx); err == nil {
			lastBlock = endBlock
			lg.logger.Info("using RPC-based end block (no recorded end block)", "block", lastBlock)
		} else {
			lg.logger.Warn("failed to get end block number via RPC", "error", err)
		}
		cancel()
	}

	result := onChainMetricsResult{
		firstBlock: firstBlock,
		lastBlock:  lastBlock,
	}

	// If we don't have a valid block range, return zeros
	if firstBlock == 0 || lastBlock == 0 || lastBlock < firstBlock {
		lg.logger.Warn("no valid block range for on-chain verification",
			"firstBlock", firstBlock, "lastBlock", lastBlock)
		return result
	}

	// Query blocks from firstBlock to lastBlock
	blockCount := lastBlock - firstBlock + 1
	lg.logger.Info("calculating on-chain metrics",
		"firstBlock", firstBlock, "lastBlock", lastBlock, "blockCount", blockCount)

	// Update verification progress - fetching on-chain metrics
	lg.statusMu.Lock()
	lg.verifyPhase = types.VerifyPhaseOnChainMetrics
	lg.verifyProgress = fmt.Sprintf("Fetching on-chain metrics (0/%d blocks)...", blockCount)
	lg.blocksToVerify = int(blockCount)
	lg.blocksVerified = 0
	lg.statusMu.Unlock()

	var totalTxCount uint64
	var totalGasUsed uint64
	var firstBlockTime, lastBlockTime time.Time
	blocksProcessed := uint64(0)

	// Use batch RPC to fetch blocks efficiently
	// Batch size of 50 balances request size with RPC overhead
	const batchSize = 50
	for batchStart := firstBlock; batchStart <= lastBlock; batchStart += batchSize {
		batchEnd := batchStart + batchSize - 1
		if batchEnd > lastBlock {
			batchEnd = lastBlock
		}

		// Build block number list for this batch
		blockNums := make([]uint64, 0, batchEnd-batchStart+1)
		for bn := batchStart; bn <= batchEnd; bn++ {
			blockNums = append(blockNums, bn)
		}

		// Fetch batch of blocks in single RPC call
		blocks, err := lg.l2Client.GetBlocksByNumberFullBatch(ctx, blockNums)
		if err != nil {
			lg.logger.Warn("batch block fetch failed, falling back to individual requests",
				"batchStart", batchStart, "batchEnd", batchEnd, "error", err)
			// Fallback to individual requests
			for _, bn := range blockNums {
				block, err := lg.l2Client.GetBlockByNumberFull(ctx, bn)
				if err != nil {
					lg.logger.Debug("failed to fetch block", "block", bn, "error", err)
					continue
				}
				if block != nil {
					totalTxCount += uint64(block.UserTxCount)
					totalGasUsed += block.GasUsed
					if bn == firstBlock {
						firstBlockTime = block.Timestamp
					}
					if bn == lastBlock {
						lastBlockTime = block.Timestamp
					}
				}
			}
			continue
		}

		// Process batch results
		for i, block := range blocks {
			if block == nil {
				continue
			}
			bn := blockNums[i]

			// Use UserTxCount (excludes deposit transactions) for accurate comparison
			totalTxCount += uint64(block.UserTxCount)
			totalGasUsed += block.GasUsed

			// Track first and last block timestamps for duration calculation
			if bn == firstBlock {
				firstBlockTime = block.Timestamp
			}
			if bn == lastBlock {
				lastBlockTime = block.Timestamp
			}
		}

		// Update progress after each batch
		blocksProcessed += uint64(len(blockNums))
		lg.statusMu.Lock()
		lg.blocksVerified = int(blocksProcessed)
		lg.verifyProgress = fmt.Sprintf("Fetching on-chain metrics (%d/%d blocks)...", blocksProcessed, blockCount)
		lg.statusMu.Unlock()
	}

	result.txCount = totalTxCount
	result.gasUsed = totalGasUsed

	// Calculate duration from block timestamps
	if !firstBlockTime.IsZero() && !lastBlockTime.IsZero() {
		result.durationSecs = lastBlockTime.Sub(firstBlockTime).Seconds()
		if result.durationSecs > 0 {
			result.mgasPerSec = float64(totalGasUsed) / 1_000_000 / result.durationSecs
			result.tps = float64(totalTxCount) / result.durationSecs
		}
	}

	lg.logger.Info("on-chain verification complete",
		"txCount", result.txCount,
		"gasUsed", result.gasUsed,
		"mgasPerSec", fmt.Sprintf("%.2f", result.mgasPerSec),
		"tps", fmt.Sprintf("%.2f", result.tps),
		"durationSecs", fmt.Sprintf("%.2f", result.durationSecs))

	return result
}

// getBlockMetricsForPeriod calculates aggregated block metrics since last call.
// Uses WebSocket data if available, falls back to RPC when WebSocket is unavailable.
// Also updates cumulativeGasUsed for smooth time-series MGas/s calculation.
func (lg *LoadGenerator) getBlockMetricsForPeriod() (gasUsed, gasLimit uint64, blockCount int, mgasPerSec, fillRate float64) {
	lg.blockMetricsMu.Lock()

	wsMetricsCount := len(lg.blockMetrics)

	// If we have WebSocket-collected metrics, use them
	if wsMetricsCount > 0 {
		// Sum up metrics from collected blocks
		var totalGasUsed, totalGasLimit uint64
		var totalBlockTime float64
		count := len(lg.blockMetrics)

		for _, m := range lg.blockMetrics {
			totalGasUsed += m.gasUsed
			totalGasLimit += m.gasLimit
			totalBlockTime += m.blockTime
		}

		// NOTE: cumulativeGasUsed and rollingGasWindow are already updated in processBuilderBlockMetrics
		// when each block event arrives. We do NOT update them again here to avoid double-counting.

		// Calculate rates (per-period, still useful for fill rate)
		if totalBlockTime > 0 {
			mgasPerSec = float64(totalGasUsed) / 1_000_000 / totalBlockTime
		}
		if totalGasLimit > 0 {
			fillRate = float64(totalGasUsed) / float64(totalGasLimit) * 100
		}

		// Clear metrics for next period
		lg.blockMetrics = lg.blockMetrics[:0]
		lg.blockMetricsMu.Unlock()

		return totalGasUsed, totalGasLimit, count, mgasPerSec, fillRate
	}

	lg.blockMetricsMu.Unlock()

	// Fallback: use RPC to fetch blocks since last check
	rpcGasUsed, rpcGasLimit, rpcBlockCount, rpcMgasPerSec, rpcFillRate := lg.getBlockMetricsViaRPC()

	// Update cumulative gas and rolling window for RPC fallback too
	if rpcGasUsed > 0 {
		lg.blockMetricsMu.Lock()
		lg.cumulativeGasUsed += rpcGasUsed
		lg.rollingGasWindow = append(lg.rollingGasWindow, rollingGasPoint{
			timestamp: time.Now(),
			gasUsed:   rpcGasUsed,
		})
		// Also add to TX rolling window (approximate TX count from gas if not available)
		if rpcBlockCount > 0 {
			lg.rollingTxWindow = append(lg.rollingTxWindow, rollingTxPoint{
				timestamp: time.Now(),
				txCount:   rpcBlockCount,
			})
		}
		lg.blockMetricsMu.Unlock()
	}

	return rpcGasUsed, rpcGasLimit, rpcBlockCount, rpcMgasPerSec, rpcFillRate
}

// calculateRollingMgasPerSec calculates MGas/s using a rolling window for smooth charts.
// This avoids the startup spike and sawtooth pattern that cumulative calculation creates.
// FIX: Uses actual elapsed time in window to avoid warmup underestimate.
// FIX: Returns frozen last value when test context is cancelled to prevent sawtooth decay.
func (lg *LoadGenerator) calculateRollingMgasPerSec() float64 {
	// Check if test is ending - return frozen last value to prevent sawtooth decay
	// This happens when StopTest() cancels the context but status is still "running"
	// during the worker wait period (5s timeout + 3s grace)
	// Also handle nil context (no test running)
	if lg.ctx == nil {
		return 0
	}
	select {
	case <-lg.ctx.Done():
		lg.blockMetricsMu.Lock()
		cached := lg.lastMgasPerSec
		lg.blockMetricsMu.Unlock()
		return cached
	default:
	}

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if len(lg.rollingGasWindow) == 0 {
		return 0
	}

	now := time.Now()
	cutoff := now.Add(-rollingWindowDuration)

	// Prune old entries and sum gas in window, track oldest timestamp
	var totalGas uint64
	var oldestInWindow time.Time
	validIdx := 0

	for i, p := range lg.rollingGasWindow {
		if p.timestamp.After(cutoff) {
			// Keep entries within window
			if validIdx != i {
				lg.rollingGasWindow[validIdx] = p
			}
			// Track oldest timestamp in window
			if validIdx == 0 || p.timestamp.Before(oldestInWindow) {
				oldestInWindow = p.timestamp
			}
			validIdx++
			totalGas += p.gasUsed
		}
	}

	// Truncate the slice to remove pruned entries
	lg.rollingGasWindow = lg.rollingGasWindow[:validIdx]

	if totalGas == 0 || validIdx == 0 {
		return 0
	}

	// Calculate actual elapsed time in window (not fixed 5 seconds)
	// This fixes the warmup underestimate where first 5 seconds show low MGas/s
	actualWindowDuration := now.Sub(oldestInWindow)

	// Ensure minimum duration to avoid division by very small numbers
	if actualWindowDuration < 200*time.Millisecond {
		actualWindowDuration = 200 * time.Millisecond
	}

	// Cap at rolling window duration (don't exceed 5 seconds)
	if actualWindowDuration > rollingWindowDuration {
		actualWindowDuration = rollingWindowDuration
	}

	// Minimum window check: require at least 3 blocks or 1 second for stable startup
	// This prevents the initial spike where first block gives artificially high MGas/s
	minBlocks := 3
	minDuration := 1 * time.Second
	if validIdx < minBlocks && actualWindowDuration < minDuration {
		lg.lastMgasPerSec = 0
		return 0
	}

	// Calculate MGas/s over the actual window duration
	result := float64(totalGas) / 1_000_000 / actualWindowDuration.Seconds()

	// Cache the result for use when test ends (to prevent sawtooth decay)
	lg.lastMgasPerSec = result

	return result
}

// calculateRollingTxPerSec calculates TX/s using a rolling window aligned with MGas/s.
// This ensures both metrics use the same time window for consistent charts.
func (lg *LoadGenerator) calculateRollingTxPerSec() float64 {
	if lg.ctx == nil {
		return 0
	}
	select {
	case <-lg.ctx.Done():
		lg.blockMetricsMu.Lock()
		cached := lg.lastTxPerSec
		lg.blockMetricsMu.Unlock()
		return cached
	default:
	}

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if len(lg.rollingTxWindow) == 0 {
		return 0
	}

	now := time.Now()
	cutoff := now.Add(-rollingWindowDuration)

	var totalTxCount int
	var oldestInWindow time.Time
	validIdx := 0

	for i, p := range lg.rollingTxWindow {
		if p.timestamp.After(cutoff) {
			if validIdx != i {
				lg.rollingTxWindow[validIdx] = p
			}
			if validIdx == 0 || p.timestamp.Before(oldestInWindow) {
				oldestInWindow = p.timestamp
			}
			validIdx++
			totalTxCount += p.txCount
		}
	}

	lg.rollingTxWindow = lg.rollingTxWindow[:validIdx]

	if totalTxCount == 0 || validIdx == 0 {
		return 0
	}

	actualWindowDuration := now.Sub(oldestInWindow)

	if actualWindowDuration < 200*time.Millisecond {
		actualWindowDuration = 200 * time.Millisecond
	}

	if actualWindowDuration > rollingWindowDuration {
		actualWindowDuration = rollingWindowDuration
	}

	// Minimum window check: require at least 3 blocks or 1 second for stable startup
	// This prevents the initial spike where first block gives artificially high TX/s
	minBlocks := 3
	minDuration := 1 * time.Second
	if validIdx < minBlocks && actualWindowDuration < minDuration {
		lg.lastTxPerSec = 0
		return 0
	}

	result := float64(totalTxCount) / actualWindowDuration.Seconds()

	// Cache and update peak (atomic for thread safety)
	lg.lastTxPerSec = result
	currentTPSInt := int64(result)
	for {
		oldVal := atomic.LoadInt64(&lg.peakRate)
		if oldVal >= currentTPSInt || atomic.CompareAndSwapInt64(&lg.peakRate, oldVal, currentTPSInt) {
			break
		}
	}

	return result
}

// calculateAvgBlockTimeMs calculates average block time from recent blocks.
// Returns 0 if no block time data is available.
func (lg *LoadGenerator) calculateAvgBlockTimeMs() float64 {
	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if len(lg.blockMetrics) == 0 {
		return 0
	}

	// Use last 10 blocks for average (or all if fewer)
	startIdx := 0
	if len(lg.blockMetrics) > 10 {
		startIdx = len(lg.blockMetrics) - 10
	}

	var totalMs int64
	var count int
	for i := startIdx; i < len(lg.blockMetrics); i++ {
		if lg.blockMetrics[i].blockTimeMs > 0 {
			totalMs += lg.blockMetrics[i].blockTimeMs
			count++
		}
	}

	if count == 0 {
		return 0
	}

	return float64(totalMs) / float64(count)
}

// getBlockMetricsViaRPC fetches block metrics via RPC when WebSocket is unavailable.
// This is a fallback mechanism to ensure time series data includes block metrics.
func (lg *LoadGenerator) getBlockMetricsViaRPC() (gasUsed, gasLimit uint64, blockCount int, mgasPerSec, fillRate float64) {
	if lg.l2Client == nil {
		return 0, 0, 0, 0, 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Get current block number
	currentBlock, err := lg.l2Client.GetBlockNumber(ctx)
	if err != nil {
		lg.logger.Debug("RPC fallback: failed to get block number", "error", err)
		return 0, 0, 0, 0, 0
	}

	// Initialize rpcLastBlockNumber on first call
	lg.blockMetricsMu.Lock()
	if lg.rpcLastBlockNumber == 0 {
		lg.rpcLastBlockNumber = currentBlock
		lg.blockMetricsMu.Unlock()
		return 0, 0, 0, 0, 0
	}
	lastBlock := lg.rpcLastBlockNumber
	lg.rpcLastBlockNumber = currentBlock
	lg.blockMetricsMu.Unlock()

	// No new blocks
	if currentBlock <= lastBlock {
		return 0, 0, 0, 0, 0
	}

	// Fetch blocks from lastBlock+1 to currentBlock (limit to 10 blocks per period to avoid overload)
	maxBlocks := uint64(10)
	startBlock := lastBlock + 1
	if currentBlock-lastBlock > maxBlocks {
		startBlock = currentBlock - maxBlocks + 1
	}

	var totalGasUsed, totalGasLimit uint64
	var fetchedBlocks int
	startTime := time.Now()

	for blockNum := startBlock; blockNum <= currentBlock; blockNum++ {
		block, err := lg.l2Client.GetBlockByNumber(ctx, blockNum)
		if err != nil {
			continue
		}
		if block == nil {
			continue
		}
		totalGasUsed += block.GasUsed
		totalGasLimit += block.GasLimit
		fetchedBlocks++
	}

	if fetchedBlocks == 0 {
		return 0, 0, 0, 0, 0
	}

	lg.logger.Debug("RPC fallback: fetched blocks",
		"startBlock", startBlock,
		"endBlock", currentBlock,
		"fetchedBlocks", fetchedBlocks,
		"totalGasUsed", totalGasUsed,
		"totalGasLimit", totalGasLimit)

	// Calculate rates
	// Estimate time based on blocks fetched (using config block time or 1s default)
	blockTimeMs := lg.cfg.BlockTimeMS
	if blockTimeMs == 0 {
		blockTimeMs = 1000
	}
	estimatedTime := float64(fetchedBlocks) * float64(blockTimeMs) / 1000.0

	// Alternatively, use actual time elapsed if blocks span the sample period
	elapsed := time.Since(startTime).Seconds()
	if elapsed > 0 && elapsed < estimatedTime*2 {
		// Use estimated time based on block production rate
	}

	if estimatedTime > 0 {
		mgasPerSec = float64(totalGasUsed) / 1_000_000 / estimatedTime
	}
	if totalGasLimit > 0 {
		fillRate = float64(totalGasUsed) / float64(totalGasLimit) * 100
	}

	return totalGasUsed, totalGasLimit, fetchedBlocks, mgasPerSec, fillRate
}