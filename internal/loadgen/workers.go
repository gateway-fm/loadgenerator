package loadgen

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/pattern"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	"github.com/gateway-fm/loadgenerator/internal/workload"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// senderWorker is a goroutine that sends transactions at the target rate.
// Uses token bucket rate limiter for smooth, non-bursty traffic generation.
// Uses the ReserveNonce pattern for automatic nonce rollback on any error.
func (lg *LoadGenerator) senderWorker(id int, accounts []*account.Account) {
	defer lg.wg.Done()

	if len(accounts) == 0 {
		return
	}

	acc := accounts[id%len(accounts)]
	chainID := big.NewInt(lg.cfg.ChainID)
	signer := ethtypes.NewLondonSigner(chainID)
	rnd := account.NewRand() // Thread-safe random for realistic mode

	// Realistic TX generation (mixed types and random tips) for both "realistic" and
	// "adaptive-realistic". The config resolved here — provided, or the defaults for
	// adaptive-realistic — is the SAME one the contract-deployment decision reads, via
	// workload.EffectiveRealisticConfig. They must never diverge: when they did, the
	// deploy step saw "no mix" while these workers still selected erc20Transfer and
	// uniswapSwap, and the transactions went to the zero address.
	realisticCfg := workload.EffectiveRealisticConfig(lg.testConfig.Pattern, lg.testConfig.RealisticConfig)
	useRealisticTxGen := realisticCfg != nil

	// For non-realistic mode, get the single builder
	var defaultBuilder txbuilder.Builder
	if !useRealisticTxGen {
		var err error
		defaultBuilder, err = lg.txBuilderReg.Get(lg.currentTxType)
		if err != nil {
			lg.logger.Error("failed to get tx builder", "error", err)
			return
		}
	}

	// Batch configuration
	// maxBatchLinger prevents the "synchronized burst" problem: when many workers
	// share a rate limiter, each worker takes N seconds to fill a batch of 20,
	// then ALL workers flush simultaneously creating massive TX bursts.
	// With a linger timeout, partial batches flush quickly, spreading load evenly.
	const batchSize = 20
	const maxBatchLinger = 100 * time.Millisecond
	// One permit per allowed in-flight batch FOR THIS ACCOUNT. nil and unused at
	// depth 1, which is the default.
	var batchPermits chan struct{}
	if d := pipelineBatchDepth(lg.cfg); d > 1 {
		batchPermits = make(chan struct{}, d)
	}
	batchData := make([][]byte, 0, batchSize)
	batchCallbacks := make([]func(error), 0, batchSize)
	// We need to track nonces to rollback on failure
	batchNonces := make([]*account.Nonce, 0, batchSize)

	for {
		// Check context/stop FIRST to prevent busy-looping when shutting down
		if lg.ctx.Err() != nil {
			return
		}
		if lg.shouldStop() {
			return
		}

		// Fill the batch (with linger deadline to flush partial batches quickly)
		var batchDeadline time.Time
		for len(batchData) < batchSize {
			if lg.ctx.Err() != nil || lg.shouldStop() {
				break
			}

			// Reserve nonce - will auto-rollback if not committed
			n := acc.ReserveNonce()

			// Select tx type and tip for this transaction
			var builder txbuilder.Builder
			var tipWei *big.Int
			var txType types.TransactionType

			if useRealisticTxGen && realisticCfg != nil {
				// Realistic/adaptive-realistic mode: random tx type and tip
				txType = workload.SelectRandomTxType(realisticCfg.TxTypeRatios, rnd)
				tipWei = workload.GenerateRandomTip(realisticCfg, rnd)

				var err error
				builder, err = lg.txBuilderReg.Get(txType)
				if err != nil {
					lg.logger.Error("failed to get tx builder for type", "type", txType, "error", err)
					n.Rollback()
					continue
				}
			} else {
				// Standard mode: fixed tx type and tip
				builder = defaultBuilder
				tipWei = lg.gasTipCap
				txType = lg.currentTxType
			}

			// Calculate gas fee cap based on tip
			gasFeeCap := new(big.Int).Add(tipWei, lg.gasFeeCap)

			// Build transaction (EIP-1559 or legacy depending on execution layer)
			tx, err := builder.Build(txbuilder.TxParams{
				ChainID:   chainID,
				Nonce:     n.Value(),
				GasTipCap: tipWei,
				GasFeeCap: gasFeeCap,
				From:      acc.Address,
				UseLegacy: lg.cfg.Capabilities != nil && lg.cfg.Capabilities.RequiresLegacyTx,
				Gasless:   lg.gasless,
			})
			if err != nil {
				lg.logger.Error("failed to build tx", "error", err)
				n.Rollback()
				continue
			}

			// Sign transaction
			signedTx, err := ethtypes.SignTx(tx, signer, acc.PrivateKey)
			if err != nil {
				lg.logger.Error("failed to sign tx", "error", err)
				n.Rollback()
				continue
			}

			// Encode transaction
			txData, err := signedTx.MarshalBinary()
			if err != nil {
				lg.logger.Error("failed to encode tx", "error", err)
				n.Rollback()
				continue
			}

			// Wait for rate limiter permit AFTER build+sign+encode succeeds.
			// This prevents build failures from wasting rate tokens.
			// Use linger-aware context when batch already has items to prevent
			// all workers from accumulating for seconds before flushing.
			var waitErr error
			if len(batchData) > 0 && !batchDeadline.IsZero() {
				remaining := time.Until(batchDeadline)
				if remaining <= 0 {
					n.Rollback()
					break // Linger expired - flush partial batch
				}
				lingerCtx, lingerCancel := context.WithTimeout(lg.ctx, remaining)
				waitErr = lg.rateLimiter.Wait(lingerCtx)
				lingerCancel()
				if waitErr != nil {
					n.Rollback()
					if lg.ctx.Err() != nil {
						return // Real context cancellation
					}
					break // Linger timeout - flush partial batch
				}
			} else {
				waitErr = lg.rateLimiter.Wait(lg.ctx)
				if waitErr != nil {
					n.Rollback()
					return // context cancelled
				}
			}

			// Set batch deadline after first TX is added
			if len(batchData) == 0 {
				batchDeadline = time.Now().Add(maxBatchLinger)
			}

			// Record metrics for realistic/adaptive-realistic mode
			if useRealisticTxGen && realisticCfg != nil {
				lg.metricsCol.RecordTipWithConfig(tipWei, realisticCfg.MinTipGwei, realisticCfg.MaxTipGwei)
				lg.metricsCol.RecordTxType(txType, tipWei, true) // Mark as sent (success tracked later)
			}

			// Add to batch
			txHash := signedTx.Hash()
			sentTime := time.Now()

			batchData = append(batchData, txData)
			batchNonces = append(batchNonces, n)

			// Capture values for callback closure
			// Note: n is captured by value/copy in batchNonces, but we need closure for callback
			nonceVal := n

			callback := func(sendErr error) {
				if sendErr == nil {
					nonceVal.Commit()
					return
				}
				// "already known" / ALREADY_EXISTS: an earlier (usually auto-retried)
				// submission of this exact signed tx already reached the mempool/chain.
				// It was counted as sent and is out there awaiting inclusion — treat it
				// as a successful submit: commit the nonce and leave it pending for the
				// confirmation/receipt path to pick up. Not a failure.
				if isAlreadyKnownTx(sendErr) {
					nonceVal.Commit()
					lg.logger.Debug("async send: tx already submitted (already known), treating as sent", "txHash", txHash.Hex())
					return
				}
				// "nonce too low" means this nonce is ALREADY CONSUMED on chain -- either
				// by this very transaction (a retried submit whose first attempt landed,
				// which Nitro reports as "nonce too low" rather than geth's "already
				// known") or by a predecessor. Rolling it back returns a SPENT nonce to
				// the free list, ReserveNonce hands it straight back out, and the resend
				// fails identically -- a self-sustaining loop that burns the account's
				// send capacity on a nonce that can never succeed. Measured: 12,844 such
				// rejections in 60s, all with a gap of exactly one (tx: 1130 state: 1131),
				// driving the failure rate from 0 to 8.9% in six minutes.
				//
				// Commit instead: the nonce is spent, so the account must move past it.
				// Still counted as a failed SEND below, because from the generator's
				// point of view this submission did not place a new transaction.
				if isNonceTooLow(sendErr) {
					nonceVal.Commit()
					metrics.AtomicSubSaturating(&lg.pendingCount, 1)
					// Drop it from send tracking BEFORE counting the failure. The two
					// causes are indistinguishable from the error alone: a predecessor
					// consumed the nonce (this tx never lands), or this tx's own retried
					// submit already landed (it will get a receipt — the builder client
					// retries 429/502/503/504). In the second case the hash is still on
					// chain, and the confirmation scan gates on GetTxSentTime, so leaving
					// the entry counts one transaction in BOTH txFailed and txConfirmed
					// and decrements pendingCount twice — corrupting the headline numbers
					// and the adaptive controller's pending signal at once.
					//
					// Counted as a failed send rather than left pending on purpose:
					// leaving it pending would inflate pendingCount for the whole run
					// whenever the cause was a predecessor, and pendingCount drives the
					// rate controller.
					lg.metricsCol.DiscardTx(txHash)
					lg.metricsCol.RecordTxFailed("send")
					atomic.AddInt64(&lg.recentFails, 1)
					go func() {
						if _, rErr := acc.MaybeResyncFromChain(lg.ctx, lg.l2Client, nonceResyncInterval); rErr != nil {
							lg.logger.Debug("nonce resync failed", "addr", acc.Address.Hex(), "error", rErr)
						}
					}()
					return
				}
				nonceVal.Rollback()
				metrics.AtomicSubSaturating(&lg.pendingCount, 1)
				// A context cancellation means the test ended while this send was in
				// flight — the request was aborted at the boundary, not rejected by the
				// chain/proxy. Don't count it as a failure or trip the circuit breaker;
				// it falls into "discarded" instead.
				if errors.Is(sendErr, context.Canceled) {
					lg.logger.Debug("async send canceled at shutdown", "txHash", txHash.Hex())
					return
				}
				lg.metricsCol.RecordTxFailed("send")
				atomic.AddInt64(&lg.recentFails, 1) // Circuit breaker tracking
				lg.logger.Debug("async send failed", "error", sendErr, "txHash", txHash.Hex())

				// Nonce drift is recoverable, but only if we act on it. On a
				// chain with no mempool (Arbitrum Nitro) a rejected nonce means
				// every LATER transaction from this account is unsendable too,
				// so without this the account is dead for the rest of the run
				// and throughput decays to zero one account at a time.
				// Resyncing from confirmed chain state puts it back to work.
				//
				// Fired async: this callback runs on the sender hot path and an
				// inline RPC round trip would stall sending. Rate-limited inside
				// MaybeResyncFromChain so a flood of rejections for one account
				// collapses into a single getTransactionCount.
				if isNonceError(sendErr) {
					go func() {
						did, rErr := acc.MaybeResyncFromChain(lg.ctx, lg.l2Client, nonceResyncInterval)
						if rErr != nil {
							lg.logger.Debug("nonce resync failed", "addr", acc.Address.Hex(), "error", rErr)
						} else if did {
							lg.logger.Debug("nonce resynced after rejection",
								"addr", acc.Address.Hex(), "nonce", acc.PeekNonce())
						}
					}()
				}
			}
			batchCallbacks = append(batchCallbacks, callback)

			// Record stats immediately (assuming send will likely succeed or be tracked via callback)
			lg.metricsCol.RecordTxSent(txHash, sentTime)
			lg.metricsCol.IncTxSent()
			atomic.AddInt64(&lg.pendingCount, 1)
			atomic.AddInt64(&lg.recentSends, 1) // Circuit breaker tracking

			// Record TX log (non-blocking)
			tipGwei := float64(tipWei.Int64()) / 1e9
			lg.recordTxSent(txHash, sentTime, id, n.Value(), tipGwei)
		}

		if len(batchData) > 0 {
			// Serialise this account's batches: wait for the in-flight batch to
			// finish before the next one is built and sent.
			//
			// Without this, a worker fires SendBatchAsync and immediately starts
			// filling the next batch, so batches carrying CONSECUTIVE nonces for
			// the SAME account are in flight concurrently on different
			// connections -- and with more than one RPC replica behind the load
			// balancer, on different hosts entirely. Nonce 20 then routinely
			// reaches the sequencer before nonce 19. On a chain with a mempool
			// that is harmless; on Nitro, which has none, the early batch is
			// refused "nonce too high" and the account needs a resync to recover.
			// Measured: a 4h soak decayed 662 -> 570 tx/s over 30 minutes with a
			// steadily climbing rejection rate.
			//
			// Costs one round trip of latency per batch PER ACCOUNT, which does
			// not reduce aggregate throughput because thousands of accounts are
			// pipelined in parallel -- and it is also honest backpressure: a
			// worker cannot outrun the chain's ability to accept its own stream.
			// Acknowledgements arrive on a buffered channel sized to the batch, and the
			// worker counts them itself. A WaitGroup plus a `go wg.Wait()` waiter would
			// leak that goroutine permanently for every batch that timed out, because
			// wg.Wait() keeps blocking after the select moves on — one leaked goroutine
			// per timed-out batch, which a 24h soak accumulates. With a buffered channel
			// there is no waiter to leak: a late callback simply writes into the buffer
			// (capacity == batch size, so it can never block) and the channel is then
			// garbage. The channel is per batch on purpose — a shared one would let a
			// timed-out batch's late acks be miscounted as the next batch's.
			acks := make(chan struct{}, len(batchCallbacks))
			gated := make([]func(error), len(batchCallbacks))
			for i, cb := range batchCallbacks {
				inner := cb
				gated[i] = func(err error) {
					inner(err)
					acks <- struct{}{}
				}
			}

			// awaitAcks drains this batch's acknowledgements with a bounded wait.
			// Extracted so it can run either inline (depth 1, the historical
			// behaviour) or in a goroutine that releases a pipeline permit
			// (depth > 1). Identical semantics either way.
			awaitAcks := func(want int) {
				// BOUNDED wait. Waiting unconditionally would wedge the worker for good
				// if callbacks never fire — which a Sender implementation is not
				// contractually obliged to guarantee, and which test doubles in
				// particular do not (this hung the loadgen suite for 600s). Give up on
				// shutdown or after a bounded timeout and resync the account's nonce:
				// one resync costs a single round trip, whereas a wedged worker costs
				// the whole account for the rest of the run.
				ackTimeout := batchAckTimeoutFor(lg.cfg)
				timer := time.NewTimer(ackTimeout)
				for remaining := want; remaining > 0; {
					select {
					case <-acks:
						remaining--
					case <-lg.ctx.Done():
						remaining = 0
					case <-timer.C:
						// Warn, not Debug: a stalled batch is the difference between
						// "this run is valid" and "this account contributed nothing for
						// the whole timeout", and Debug is off in a normal run.
						lg.logger.Warn("batch ack timed out; resyncing account nonce",
							"account", acc.Address.Hex(), "size", want,
							"timeout", ackTimeout, "unacked", remaining)
						// RESYNC RATHER THAN CARRY ON BLIND. The unacked sends may or may
						// not have landed, so this account's local counter is now of
						// unknown accuracy — and on a chain with no mempool (Nitro) a
						// nonce that does not match state is REFUSED, not queued, so the
						// next batch built from a wrong counter is guaranteed to fail and
						// so is every batch after it. Carrying on without ordering is what
						// turned 4,512 ack timeouts into 53.6% refused submissions on
						// Tickr (PRST-4459).
						//
						// MaybeResyncFromChain is the rate-limited variant on purpose:
						// timeouts arrive in synchronised waves across thousands of
						// accounts, and the unthrottled resync would stampede the same
						// endpoint that is already too slow to ack. It also only ever
						// moves the counter FORWARD and keeps still-reusable free nonces,
						// so a lagging read cannot strand the account.
						if _, rErr := acc.MaybeResyncFromChain(lg.ctx, lg.l2Client, nonceResyncInterval); rErr != nil {
							lg.logger.Debug("nonce resync after ack timeout failed",
								"account", acc.Address.Hex(), "err", rErr)
						}
						remaining = 0
					}
				}
				// Release the timer promptly rather than leaving an armed timer per
				// batch (~2k live at 1500 tx/s).
				timer.Stop()
			}

			// Hand the sender its OWN slice header. SendBatchAsync retains the slice
			// for a goroutine that reads it later, while this worker reuses
			// batchData's backing array (`batchData[:0]` plus refill) on the next
			// iteration. At depth 1 the worker blocks on awaitAcks before looping so
			// they never overlap, but above 1 the wait moves to a goroutine and the
			// worker would overwrite entries the sender is still reading — submitting
			// the WRONG transaction bytes. Only the headers are copied; each payload
			// comes fresh from MarshalBinary and is never reused.
			toSend := make([][]byte, len(batchData))
			copy(toSend, batchData)

			// Take the pipeline permit BEFORE queueing, not after. Acquiring it
			// afterwards let the worker queue one more batch and only then block, so
			// L2_PIPELINE_BATCH_DEPTH=N actually allowed N+1 in flight per account.
			depth := pipelineBatchDepth(lg.cfg)
			permitHeld := false
			if depth > 1 {
				select {
				case batchPermits <- struct{}{}:
					permitHeld = true
				case <-lg.ctx.Done():
				}
			}

			queued := lg.sender.SendBatchAsync(lg.ctx, toSend, gated)

			// PIPELINE DEPTH. At depth 1 -- the default and the historical
			// behaviour -- the worker blocks here until this batch is fully
			// acknowledged, so exactly one batch per account is ever in flight and
			// consecutive nonces cannot race each other.
			//
			// Above 1, the wait moves to a goroutine holding a permit, so up to N
			// batches per account are in flight. That is safe ONLY because nitro
			// keeps a nonce-failure cache which parks a too-high nonce and retries
			// it instead of dropping it: measured on Tickr at
			// `arb_sequencer_noncefailurecache_size` 0 of 65536 with
			// `_overflow` 0, i.e. entirely unused. Depth N puts at most
			// N-1 extra batches per account into that cache.
			//
			// WATCH `arb_sequencer_noncefailurecache_overflow` ON ANY RUN WITH
			// DEPTH > 1. Non-zero means the cache dropped a nonce, the account
			// needs a resync, and the arm's numbers are not trustworthy.
			if depth > 1 {
				switch {
				case queued && permitHeld:
					want := len(gated)
					go func() {
						awaitAcks(want)
						<-batchPermits
					}()
				case permitHeld:
					// Nothing was queued, so nothing will ack: give the permit back
					// rather than holding a slot for a batch that does not exist.
					<-batchPermits
				}
			} else if queued {
				awaitAcks(len(gated))
			}

			if !queued {
				// Sender at capacity - rollback ALL nonces in the batch and retry loop
				// We drop this batch to fallback to simple retry logic, or we could spin loop
				// But dropping/rolling back is safer to avoid stale nonces if we get stuck
				lg.logger.Debug("sender at capacity, dropping batch", "size", len(batchData))
				for _, n := range batchNonces {
					n.Rollback()
					// Revert stats optimization
					metrics.AtomicSubSaturating(&lg.pendingCount, 1)
					// We don't revert IncTxSent/RecordTxSent easily, but that's acceptable for metrics noise
				}
			}

			// Reset batch buffers (keep capacity)
			batchData = batchData[:0]
			batchCallbacks = batchCallbacks[:0]
			batchNonces = batchNonces[:0]
		}
	}
}

// shouldStop checks all termination conditions.
func (lg *LoadGenerator) shouldStop() bool {
	select {
	case <-lg.ctx.Done():
		return true
	default:
	}

	if atomic.LoadInt32(&lg.stopping) != 0 {
		return true
	}

	return time.Since(lg.startTime) >= lg.currentDuration
}

// tpsCalculator periodically calculates current TPS and records time-series.
func (lg *LoadGenerator) tpsCalculator() {
	defer lg.wg.Done()

	tpsTicker := time.NewTicker(200 * time.Millisecond) // Match timeSeriesTicker for consistent chart updates
	defer tpsTicker.Stop()

	// Time-series recording at 200ms intervals
	timeSeriesTicker := time.NewTicker(200 * time.Millisecond)
	defer timeSeriesTicker.Stop()

	// Rate update for time-based patterns (ramp, spike) at 100ms intervals
	rateTicker := time.NewTicker(100 * time.Millisecond)
	defer rateTicker.Stop()

	for {
		select {
		case <-lg.ctx.Done():
			return
		case <-timeSeriesTicker.C:
			// Record time-series point (non-blocking)
			lg.recordTimeSeriesPoint()
		case <-rateTicker.C:
			// Update rate for time-based patterns (ramp, spike)
			// Skip for patterns with adaptive controller (max) - they manage their own rate
			if lg.currentPattern != nil && !lg.currentPattern.NeedsAdaptiveController() {
				elapsed := time.Since(lg.startTime)
				newRate := lg.currentPattern.GetRate(elapsed)
				atomic.StoreInt64(&lg.currentRate, int64(newRate))
				lg.rateLimiter.SetRate(float64(newRate))
			}
		case <-tpsTicker.C:
			snapshot := lg.metricsCol.GetSnapshot()
			now := time.Now()

			// Use rolling window calculation aligned with MGas/s for smooth, consistent charts
			lg.currentTPS = lg.calculateRollingTxPerSec()

			lg.lastSentCount = snapshot.TxSent
			lg.lastCheckTime = now

			// Update peak rate from rolling calculation (atomic for thread safety)
			currentTPSInt := int64(lg.currentTPS)
			atomic.CompareAndSwapInt64(&lg.peakRate, atomic.LoadInt64(&lg.peakRate), currentTPSInt)
			for {
				oldVal := atomic.LoadInt64(&lg.peakRate)
				if oldVal >= currentTPSInt || atomic.CompareAndSwapInt64(&lg.peakRate, oldVal, currentTPSInt) {
					break
				}
			}
		}
	}
}

// adaptiveController adjusts rate for max pattern.
// Features:
// - Exponential backoff when pending is very high (not just linear decrease)
// - Circuit breaker that halts sending when failure rate > 50%
// - Faster reaction time (500ms instead of 1s)
func (lg *LoadGenerator) adaptiveController() {
	defer lg.wg.Done()

	// Faster tick rate for more responsive control
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Get target values once at start
	targetPending := int64(lg.testConfig.AdaptiveTargetPending)
	if targetPending <= 0 {
		targetPending = 1000 // default
	}
	step := int64(lg.testConfig.AdaptiveRateStep)
	if step <= 0 {
		step = 100 // default
	}

	// Circuit breaker thresholds
	const (
		minSamplesForCircuit    = int64(100) // Need at least 100 sends to evaluate
		minSamplesForRecovery   = int64(20)  // Lower threshold when circuit is open (at low rates)
		failureRateThreshold    = 0.30       // Open circuit if >30% send failures
		revocationRateThreshold = 0.10       // Open circuit if >10% revocations (lower - revocations are worse)
		recoveryRateThreshold   = 0.05       // Close circuit if <5% combined failures
		backpressureThreshold   = 0.8        // Slow down if builder pressure > 80%
	)

	lg.logger.Info("adaptive controller started",
		"targetPending", targetPending,
		"rateStep", step,
		"configTargetPending", lg.testConfig.AdaptiveTargetPending,
		"configRateStep", lg.testConfig.AdaptiveRateStep)

	for {
		select {
		case <-lg.ctx.Done():
			lg.logger.Info("adaptive controller stopped")
			return
		case <-ticker.C:
			if lg.currentPattern == nil {
				continue
			}

			// Only for max pattern
			maxPat, ok := lg.currentPattern.(*pattern.Adaptive)
			if !ok {
				continue
			}

			// Check circuit breaker - read and reset counters atomically
			sends := atomic.SwapInt64(&lg.recentSends, 0)
			fails := atomic.SwapInt64(&lg.recentFails, 0)
			revocations := atomic.SwapInt64(&lg.recentRevocations, 0)
			currentRate := atomic.LoadInt64(&lg.currentRate)
			pending := atomic.LoadInt64(&lg.pendingCount)
			circuitOpen := atomic.LoadInt32(&lg.circuitOpen) != 0

			// Check block builder backpressure
			lg.builderPressureMu.RLock()
			pressure := lg.builderPressure
			lg.builderPressureMu.RUnlock()

			// Evaluate circuit breaker (lower sample threshold when circuit is open for faster recovery)
			minSamples := minSamplesForCircuit
			if circuitOpen {
				minSamples = minSamplesForRecovery
			}
			if sends >= minSamples {
				failureRate := float64(fails) / float64(sends)
				revocationRate := float64(revocations) / float64(sends)
				combinedFailureRate := failureRate + revocationRate

				// Open circuit on high failure rate OR high revocation rate
				if !circuitOpen && (failureRate > failureRateThreshold || revocationRate > revocationRateThreshold) {
					// Open circuit - gradual backoff (halve rate, floor 50)
					atomic.StoreInt32(&lg.circuitOpen, 1)
					atomic.StoreInt32(&lg.nonceResyncNeeded, 1) // Flag for nonce resync
					atomic.StoreInt64(&lg.preCircuitRate, currentRate)
					newRate := currentRate / 2
					if newRate < 50 {
						newRate = 50
					}
					atomic.StoreInt64(&lg.currentRate, newRate)
					lg.rateLimiter.SetRate(float64(newRate))
					maxPat.SetCurrentRate(int(newRate))
					lg.logger.Warn("circuit breaker OPEN - high failure/revocation rate",
						"failureRate", failureRate,
						"revocationRate", revocationRate,
						"sends", sends,
						"fails", fails,
						"revocations", revocations,
						"oldRate", currentRate,
						"newRate", newRate)
					// Trigger async nonce resync
					go lg.resyncAllNonces()
					continue
				} else if circuitOpen && combinedFailureRate < recoveryRateThreshold {
					// Close circuit - allow recovery
					atomic.StoreInt32(&lg.circuitOpen, 0)
					lg.logger.Info("circuit breaker CLOSED - failure rate recovered",
						"failureRate", failureRate,
						"revocationRate", revocationRate)
					circuitOpen = false
				}
			}

			// Backpressure check - slow down if builder is overloaded
			if pressure > backpressureThreshold && !circuitOpen {
				newRate := currentRate / 2
				if newRate < 100 {
					newRate = 100
				}
				atomic.StoreInt64(&lg.currentRate, newRate)
				lg.rateLimiter.SetRate(float64(newRate))
				maxPat.SetCurrentRate(int(newRate))
				lg.logger.Warn("backpressure throttle - builder overloaded",
					"pressure", pressure,
					"oldRate", currentRate,
					"newRate", newRate)
				continue
			}

			// If circuit is open, probe-based recovery (AIMD: increase 50% per tick)
			// If failures are still high, circuit re-trips and rate halves again.
			// This creates natural oscillation toward equilibrium instead of permanent death.
			if circuitOpen {
				newRate := currentRate + (currentRate / 2)
				ceiling := atomic.LoadInt64(&lg.preCircuitRate)
				if ceiling > 0 && newRate > ceiling {
					newRate = ceiling
				}
				atomic.StoreInt64(&lg.currentRate, newRate)
				lg.rateLimiter.SetRate(float64(newRate))
				maxPat.SetCurrentRate(int(newRate))
				lg.logger.Info("circuit open - probing recovery (AIMD)",
					"oldRate", currentRate,
					"newRate", newRate,
					"ceiling", ceiling)
				continue
			}

			// Adaptive rate control with exponential backoff for high overload
			if pending < targetPending {
				// Room to increase (only if not recovering from circuit open)
				newRate := currentRate + step
				atomic.StoreInt64(&lg.currentRate, newRate)
				lg.rateLimiter.SetRate(float64(newRate))
				maxPat.SetCurrentRate(int(newRate))
				lg.logger.Debug("adaptive: increasing rate",
					"pending", pending,
					"target", targetPending,
					"oldRate", currentRate,
					"newRate", newRate)
			} else if pending > targetPending*4 {
				// CRITICAL overload: exponential backoff (halve the rate)
				newRate := currentRate / 2
				if newRate < 10 {
					newRate = 10
				}
				atomic.StoreInt64(&lg.currentRate, newRate)
				lg.rateLimiter.SetRate(float64(newRate))
				maxPat.SetCurrentRate(int(newRate))
				lg.logger.Warn("adaptive: CRITICAL backoff (halving rate)",
					"pending", pending,
					"target", targetPending,
					"oldRate", currentRate,
					"newRate", newRate)
			} else if pending > targetPending*2 {
				// High overload: aggressive linear decrease (2x step)
				newRate := currentRate - step*2
				if newRate < 10 {
					newRate = 10
				}
				atomic.StoreInt64(&lg.currentRate, newRate)
				lg.rateLimiter.SetRate(float64(newRate))
				maxPat.SetCurrentRate(int(newRate))
				lg.logger.Debug("adaptive: aggressive decrease",
					"pending", pending,
					"target", targetPending,
					"oldRate", currentRate,
					"newRate", newRate)
			} else if pending > targetPending {
				// Moderate overload: normal linear decrease
				newRate := currentRate - step
				if newRate < 10 {
					newRate = 10
				}
				atomic.StoreInt64(&lg.currentRate, newRate)
				lg.rateLimiter.SetRate(float64(newRate))
				maxPat.SetCurrentRate(int(newRate))
				lg.logger.Debug("adaptive: decreasing rate",
					"pending", pending,
					"target", targetPending,
					"oldRate", currentRate,
					"newRate", newRate)
			}
		}
	}
}

// completionWatcher watches for test completion.
func (lg *LoadGenerator) completionWatcher() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-lg.ctx.Done():
			return
		case <-ticker.C:
			elapsed := time.Since(lg.startTime)
			if elapsed >= lg.currentDuration {
				lg.stopTest() // natural completion: run full confirmation/verification
				return
			}
		}
	}
}

// isAlreadyKnownTx reports whether a send error means the identical signed tx was
// already submitted (in the mempool or already on-chain) — geth's "already known"
// / JSON-RPC "-32000 ALREADY_EXISTS". This happens when an internal RPC retry
// resends a tx whose first attempt already reached the node (common over a remote
// proxy). The transaction itself succeeded, so the resend is not a failure.
func isAlreadyKnownTx(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already known") || strings.Contains(s, "already_exists")
}

// nonceResyncInterval is the minimum gap between in-test nonce resyncs for a
// single account. Short enough that a stalled account rejoins the run within a
// couple of blocks, long enough that thousands of rejections per second across
// hundreds of accounts cannot turn into an eth_getTransactionCount flood that
// moves the bottleneck onto the RPC node.
const nonceResyncInterval = 750 * time.Millisecond

// maxSenderWorkers caps the sender goroutine pool. Each worker is PINNED to one
// account for the whole run (accounts[id%len(accounts)]), so this is also the
// effective size of the sender pool: raising numAccounts above it funds and sets
// up accounts that never send a single transaction.
//
// Raised from 500 for PRST-4262. The pool size sets each account's nonce
// VELOCITY, and on a chain with no mempool that is what decides whether a run is
// stable: 1500 tx/s across 500 accounts is 3 tx/s per account, each worker firing
// batches of 20 consecutive nonces, so any reordering or loss strands the rest of
// that batch and the account needs a resync to recover. Spreading the same rate
// over 2000 accounts cuts per-account velocity 4x and makes drift far less likely.
//
// Raised again to 4000 for PRST-4453, for the same reason and one rung further
// up the rate ladder. 2000 accounts is enough up to ~1500 tx/s, but a 3,000 tx/s
// arm puts each account at 1.5 tx/s and PRST-4367 measured that as a PERSISTENT
// ~1.4% send-failure drizzle that never clears -- not a transient. Spreading the
// same 3,000 tx/s over 4000 accounts halves per-account velocity to 0.75 tx/s,
// which took the identical arm to 3 failures in 2,699,771 sends (0.0001%).
//
// PRST-4367 made this change locally and never pushed it, so the fix was carried
// only by an image tag; it is committed here so the next campaign does not have
// to rediscover it.
//
// Kept at sender concurrency / 4, so the semaphore still absorbs a full round of
// concurrent batch sends -- concurrency is raised to 16000 alongside this.
//
// BUT THIS DEFAULT IS WRONG FOR A HIGH-LATENCY PATH, AND BY A LOT. Everything
// above argues for MORE accounts, to cut per-account nonce velocity. There is an
// opposing force that only shows up once a slow hop is in front of the chain, and
// on Tickr through its public proxy edge it dominates (PRST-4459, measured
// 2026-08-22):
//
// A worker holds ONE batch in flight and blocks on its ack, so in-flight work is
// `workers x batchSize` and ack latency is that divided by the throughput the
// path can actually serve. At 4000 workers into a path serving ~3,000 tx/s that
// is 80,000 transactions in flight and an ack latency near 27 s -- against a
// batchAckTimeout of 30 s. The result is not a slowdown but a collapse: 4,512
// batches timed out in a 300 s arm, 53.6% of all submissions were refused, and
// two stretches of 56 s and 9 s produced zero transactions.
//
// Lowering the pool to 300 on the identical rig, changing nothing else, gave
// 2,913.8 tx/s against 2,054.8 (+41.8%), ZERO failed submissions out of 528,392,
// and no outages. So on this path lower is strictly better, and it does not cost
// the stability the paragraphs above bought: per-account velocity at 300 accounts
// is ~10 tx/s, far above the 0.75 tx/s PRST-4367 aimed for, and the failure rate
// was still zero. The reason is that nitro's eth_sendRawTransaction does not
// return until the transaction is sequenced and a JSON-RPC batch is served
// serially, so each account's nonces cannot overtake each other regardless of
// velocity -- the strict one-batch-per-account serialisation below is what makes
// that safe.
//
// The corollary is the useful part: throughput is
// `concurrently-submitting accounts x block rate`, because each account lands one
// transaction per block. 300 workers x ~9.85 blocks/s = ~2,955 tx/s, measured
// 2,899-2,914. Raising the pool raises the CEILING (600 workers reached 601
// transactions per block and 3,729 tx/s peaks) but past ~300 the average stops
// improving and the edge starts refusing -- 3.3% at 600 through the proxy, 0%
// direct to the sequencer.
//
// So: tune this per path rather than trusting the default. Size it as
// `target_tx_per_s / block_rate` and check ack latency stays well inside
// L2_BATCH_ACK_TIMEOUT. Do not raise it to chase throughput.
const maxSenderWorkers = 4000

// senderWorkerPool returns the effective worker-pool size: cfg override when set,
// otherwise the maxSenderWorkers default above.
//
// PRST-4459 made this overridable because the pool, not the chain and not the
// proxy, is what bounds throughput on a no-mempool chain: workers are pinned
// one-per-account and gated on their own account's ack, so aggregate is
// `workers / ack_latency`. 4000 workers at the ~1.8s ack latency of an HTTPS edge
// predicts 2,222 tx/s and 2,218 was measured -- while the edge sat at 16.79% of a
// core and the sequencer closed blocks for lack of work. Raising the target rate
// cannot beat this; only more workers or a shorter ack can.
func senderWorkerPool(cfg *config.Config) int {
	if cfg != nil && cfg.L2MaxSenderWorkers > 0 {
		return cfg.L2MaxSenderWorkers
	}
	return maxSenderWorkers
}

// senderConcurrency returns the send semaphore size for a given pool.
//
// The invariant is 4x the pool and it is load-bearing: each worker holds one slot
// per BATCH, so at 1x a single round of concurrent batches saturates the semaphore
// and serialises sending -- which presents as a chain-side throughput ceiling and
// is entirely client-side. Raising the pool without this would reintroduce it.
func senderConcurrency(cfg *config.Config) int {
	return senderWorkerPool(cfg) * 4
}

// defaultBatchAckTimeout bounds how long a worker waits for its in-flight batch
// to be acknowledged before sending the next one for the same account. Generous
// relative to a normal submit (sub-millisecond to low tens of ms) so it only
// fires when something is genuinely wrong.
//
// It is GENEROUS TO A FAULT against a rate-limited proxy edge, which is why it is
// now overridable. A worker is pinned to one account and blocks here for the whole
// timeout, so the cost of one slow ack is that account contributing nothing for
// 30 seconds — and because timeouts arrive in synchronised waves, the aggregate
// effect is a stall, not a slowdown. Measured on Tickr through the proxy
// (PRST-4459): 4,512 timeouts in a 300s arm, two dead stretches of 56s and 9s
// with zero transactions on chain, and a sawtooth between 187 and 5,622 tx/s.
const defaultBatchAckTimeout = 30 * time.Second

// batchAckTimeoutFor resolves the per-batch ack timeout. Unset (zero) keeps the
// historical 30s so existing runs are unchanged; config rejects non-positive
// values, so anything non-zero reaching here is deliberate.
func batchAckTimeoutFor(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.L2BatchAckTimeout > 0 {
		return cfg.L2BatchAckTimeout
	}
	return defaultBatchAckTimeout
}

// pipelineBatchDepth returns how many batches may be in flight per account.
//
// 1 (the default) is strict serialisation: the worker waits for every ack before
// building the next batch, so consecutive nonces for one account can never race.
// That is the right default on a chain with no mempool, where a nonce arriving
// early is refused rather than queued.
//
// Above 1 it relies on nitro's nonce-failure cache to park a too-high nonce and
// retry it. On Tickr that cache measured 0 of 65536 entries used with zero
// overflow, so there is real headroom -- but `arb_sequencer_noncefailurecache_overflow`
// must be watched on any run above 1, because a non-zero value means a nonce was
// dropped, the account needs a resync, and the arm is not trustworthy.
func pipelineBatchDepth(cfg *config.Config) int {
	if cfg != nil && cfg.L2PipelineBatchDepth > 1 {
		return cfg.L2PipelineBatchDepth
	}
	return 1
}

// isNonceTooLow reports whether the sender's nonce was already consumed on chain.
// Distinguished from the generic nonce error because the two need OPPOSITE
// handling: a too-HIGH nonce was not consumed and must be rolled back for reuse,
// while a too-LOW one is spent and must be committed so the account moves past it.
func isNonceTooLow(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nonce too low") ||
		strings.Contains(s, "nonce has already been used")
}

// isNonceError reports whether a send was refused because the sender's nonce did
// not match chain state — either ahead of it ("nonce too high", no predecessor to
// follow) or behind it ("nonce too low", already consumed).
//
// Both are recoverable by resyncing from the chain, and both are FATAL to an
// account if left alone on a chain with no mempool: Nitro will not hold a
// transaction whose predecessor is missing, so every later nonce from that
// account is refused too. See Account.MaybeResyncFromChain.
func isNonceError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nonce too low") ||
		strings.Contains(s, "nonce too high") ||
		strings.Contains(s, "invalid nonce") ||
		strings.Contains(s, "nonce has already been used")
}
