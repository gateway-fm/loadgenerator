package loadgen

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
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

	// Check if we should use realistic TX generation (mixed types and random tips)
	// Both "realistic" and "adaptive-realistic" patterns use this
	useRealisticTxGen := lg.testConfig.Pattern == types.PatternRealistic ||
		lg.testConfig.Pattern == types.PatternAdaptiveRealistic

	// Get realistic config - use provided config or defaults for adaptive-realistic
	var realisticCfg *types.RealisticTestConfig
	if useRealisticTxGen {
		if lg.testConfig.RealisticConfig != nil {
			realisticCfg = lg.testConfig.RealisticConfig
		} else {
			// Use defaults for adaptive-realistic pattern
			realisticCfg = workload.DefaultRealisticConfig()
		}
	}

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
				if sendErr != nil {
					nonceVal.Rollback()
					metrics.AtomicSubSaturating(&lg.pendingCount, 1)
					// A context cancellation means the test ended while this send
					// was in flight — the request was aborted at the boundary, not
					// rejected by the chain/proxy. Don't count it as a failure or
					// trip the circuit breaker; it falls into "discarded" instead.
					if errors.Is(sendErr, context.Canceled) {
						lg.logger.Debug("async send canceled at shutdown", "txHash", txHash.Hex())
						return
					}
					lg.metricsCol.RecordTxFailed("send")
					atomic.AddInt64(&lg.recentFails, 1) // Circuit breaker tracking
					lg.logger.Debug("async send failed", "error", sendErr, "txHash", txHash.Hex())
				} else {
					nonceVal.Commit()
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
			// Send the batch
			queued := lg.sender.SendBatchAsync(lg.ctx, batchData, batchCallbacks)

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
				lg.StopTest()
				return
			}
		}
	}
}