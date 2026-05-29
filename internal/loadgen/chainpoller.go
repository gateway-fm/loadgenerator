package loadgen

import (
	"context"
	"log/slog"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/metrics"
)

// connectChainPoller is the receipt-polling confirmation fallback used when
// the execution layer doesn't expose a preconfirmation WebSocket (e.g.
// gravity-reth, cdk-erigon, our `reth-ext-native` mode).
//
// It polls `eth_blockNumber` on a short interval, fetches each new block's
// transaction hash list via `eth_getBlockByNumber(N, false)`, and marks every
// hash that the load-generator previously sent (`metricsCol.GetTxSentTime`) as
// confirmed via the same path the preconf WS uses. This keeps the dashboard's
// "Confirmation Rate" and per-tx confirm latencies accurate without depending
// on a builder-side preconfirmation stream.
//
// Lifetime: started from init.go when capabilities indicate no preconf source;
// terminates when lg.ctx is cancelled (test stop / reset).
func (lg *LoadGenerator) connectChainPoller() {
	defer func() {
		if r := recover(); r != nil {
			lg.logger.Error("chain poller goroutine PANIC", "panic", r)
		}
	}()

	if lg.l2Client == nil {
		lg.logger.Warn("chain poller: no l2Client configured, confirmation tracking disabled")
		return
	}

	// Anchor: never confirm anything mined before the test started. We seed
	// `nextBlock` once `testStartBlockNumber` is populated by init.go (which
	// runs right before this goroutine is spawned).
	nextBlock := lg.testStartBlockNumber + 1
	if nextBlock == 0 {
		nextBlock = 1
	}

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	lg.logger.Info("chain poller: confirmation tracking via eth_getBlockByNumber", "startBlock", nextBlock)

	for {
		select {
		case <-lg.ctx.Done():
			lg.logger.Debug("chain poller: stopping")
			return
		case <-tick.C:
		}

		head, err := lg.fetchHeadBlock()
		if err != nil {
			lg.logger.Debug("chain poller: head fetch failed", "error", err)
			continue
		}
		if head < nextBlock {
			continue
		}

		// Cap how many blocks we fetch per tick to avoid stalling forever if
		// the chain has run far ahead of the poller (e.g. test paused).
		const maxBlocksPerTick = 50
		end := head
		if end-nextBlock+1 > maxBlocksPerTick {
			end = nextBlock + maxBlocksPerTick - 1
		}

		for n := nextBlock; n <= end; n++ {
			if err := lg.scanBlockForConfirmations(n); err != nil {
				lg.logger.Debug("chain poller: block scan failed", "block", n, "error", err)
				// Don't advance past a failed block — retry next tick.
				break
			}
			nextBlock = n + 1
		}
	}
}

// fetchHeadBlock returns the current chain head, with a short per-call timeout
// so a stuck node doesn't wedge the poller.
func (lg *LoadGenerator) fetchHeadBlock() (uint64, error) {
	ctx, cancel := context.WithTimeout(lg.ctx, 3*time.Second)
	defer cancel()
	return lg.l2Client.GetBlockNumber(ctx)
}

// scanBlockForConfirmations fetches the tx-hash list for `blockNum` and
// records confirmations for any hash we previously sent.
func (lg *LoadGenerator) scanBlockForConfirmations(blockNum uint64) error {
	ctx, cancel := context.WithTimeout(lg.ctx, 5*time.Second)
	defer cancel()

	block, err := lg.l2Client.GetBlockByNumber(ctx, blockNum)
	if err != nil {
		return err
	}
	if block == nil || len(block.Transactions) == 0 {
		return nil
	}

	// Check if this block is beyond the on-chain verification window — match
	// the same semantic the preconf WS handler uses for grace-period arrivals.
	lg.blockMetricsMu.Lock()
	endBlock := lg.testEndBlockNumber
	lg.blockMetricsMu.Unlock()
	beyondTestEnd := endBlock > 0 && blockNum > endBlock

	now := time.Now()
	matched := 0
	for _, hashHex := range block.Transactions {
		hash := common.HexToHash(hashHex)
		if _, sent := lg.metricsCol.GetTxSentTime(hash); !sent {
			continue
		}
		if beyondTestEnd {
			lg.metricsCol.RecordTxConfirmedFlowOnly(hash, now)
		} else {
			lg.metricsCol.RecordTxConfirmed(hash, now)
		}
		metrics.AtomicSubSaturating(&lg.pendingCount, 1)
		lg.recordTxConfirmed(hash, now)

		if !beyondTestEnd {
			lg.recentConfirmedMu.Lock()
			lg.recentConfirmedHashes = append(lg.recentConfirmedHashes, hash.Hex())
			if len(lg.recentConfirmedHashes) > 1000 {
				lg.recentConfirmedHashes = lg.recentConfirmedHashes[len(lg.recentConfirmedHashes)-1000:]
			}
			lg.recentConfirmedMu.Unlock()
		}
		matched++
	}

	if matched > 0 {
		lg.logger.Debug(
			"chain poller: recorded confirmations",
			slog.Uint64("block", blockNum),
			slog.Int("blockTxs", len(block.Transactions)),
			slog.Int("matched", matched),
		)
	}
	return nil
}
