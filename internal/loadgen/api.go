package loadgen

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// GetMetrics returns current test metrics.
func (lg *LoadGenerator) GetMetrics() types.TestMetrics {
	lg.statusMu.RLock()
	status := lg.status
	testError := lg.testError
	lg.statusMu.RUnlock()

	// Get gas metrics from builder status (updated by backpressure monitor)
	lg.builderPressureMu.RLock()
	latestBaseFeeGwei := lg.latestBaseFeeGwei
	latestGasPriceGwei := lg.latestGasPriceGwei
	latestGasUsed := lg.latestGasUsed
	blockAttestationEnabled := lg.blockAttestationEnabled
	hsmProvider := lg.hsmProvider
	hsmKeyIDActive := lg.hsmKeyIDActive
	hsmFailoverEnabled := lg.hsmFailoverEnabled
	lg.builderPressureMu.RUnlock()

	// Get aggregate block metrics
	lg.blockMetricsMu.Lock()
	totalGasUsed := lg.cumulativeGasUsed
	totalGasLimit := lg.cumulativeGasLimit
	peakMgasPerSec := lg.peakMgasPerSec
	blockCount := lg.totalBlockCount
	lg.blockMetricsMu.Unlock()

	snapshot := lg.metricsCol.GetSnapshot()

	var elapsed, duration int64
	if !lg.startTime.IsZero() {
		elapsed = time.Since(lg.startTime).Milliseconds()
		duration = lg.currentDuration.Milliseconds()
	}

	var avgTPS float64
	if elapsed > 0 {
		avgTPS = float64(snapshot.TxSent) / (float64(elapsed) / 1000.0)
	}

	// Calculate average Mgas/s and fill rate
	var avgMgasPerSec, avgFillRate float64
	if elapsed > 0 && totalGasUsed > 0 {
		avgMgasPerSec = float64(totalGasUsed) / 1_000_000 / (float64(elapsed) / 1000.0)
	}
	if totalGasLimit > 0 {
		avgFillRate = float64(totalGasUsed) / float64(totalGasLimit) * 100
	}

	// Calculate current rolling Mgas/s for live chart (same as time series samples)
	currentMgasPerSec := lg.calculateRollingMgasPerSec()

	// Get current fill rate from latest block metrics
	var currentFillRate float64
	lg.blockMetricsMu.Lock()
	if len(lg.blockMetrics) > 0 {
		lastBlock := lg.blockMetrics[len(lg.blockMetrics)-1]
		if lastBlock.gasLimit > 0 {
			currentFillRate = float64(lastBlock.gasUsed) / float64(lastBlock.gasLimit) * 100
		}
	}
	lg.blockMetricsMu.Unlock()

	result := types.TestMetrics{
		Status:          status,
		TxSent:          snapshot.TxSent,
		TxConfirmed:     snapshot.TxConfirmed,
		TxFailed:        snapshot.TxFailed,
		CurrentTPS:      lg.currentTPS,
		AverageTPS:      avgTPS,
		ElapsedMs:       elapsed,
		DurationMs:      duration,
		TargetTPS:       int(atomic.LoadInt64(&lg.currentRate)),
		Pattern:         lg.testConfig.Pattern,
		TransactionType: lg.currentTxType,
		Error:           testError,
		PeakTPS:         int(atomic.LoadInt64(&lg.peakRate)),
		// Preconfirmation stage counters (Flashblocks-compliant)
		TxPending:      lg.metricsCol.GetTxPending(),
		TxPreconfirmed: lg.metricsCol.GetTxPreconfirmed(),
		TxRevoked:      lg.metricsCol.GetTxRevoked(),
		TxDropped:      lg.metricsCol.GetTxDropped(),
		TxRequeued:     lg.metricsCol.GetTxRequeued(),
		// Latency statistics
		Latency:        lg.metricsCol.GetLatencyStats(),
		PreconfLatency: lg.metricsCol.GetPreconfLatencyStats(),
		PendingLatency: lg.metricsCol.GetPendingLatencyStats(),
		// Block gas metrics (from block builder status)
		LatestBaseFeeGwei:  latestBaseFeeGwei,
		LatestGasPriceGwei: latestGasPriceGwei,
		LatestGasUsed:      latestGasUsed,
		// Aggregate block metrics (for live dashboard - matches history metrics)
		TotalGasUsed:   totalGasUsed,
		BlockCount:     blockCount,
		PeakMgasPerSec: peakMgasPerSec,
		AvgMgasPerSec:  avgMgasPerSec,
		AvgFillRate:    avgFillRate,
		// Current rolling metrics (for live chart - sampled at 200ms)
		CurrentMgasPerSec: currentMgasPerSec,
		CurrentFillRate:   currentFillRate,
		// HSM / block attestation metadata (if provided by builder status)
		BlockAttestationEnabled: blockAttestationEnabled,
		HSMProvider:             hsmProvider,
		HSMKeyIDActive:          hsmKeyIDActive,
		HSMFailoverEnabled:      hsmFailoverEnabled,
	}

	// Include TX flow stats if available
	if flowStats := lg.metricsCol.GetFlowStats(); flowStats != nil {
		result.FlowStats = &types.TxFlowStats{
			DirectConfirmed:  flowStats.DirectConfirmed,
			PendingConfirmed: flowStats.PendingConfirmed,
			PreconfConfirmed: flowStats.PreconfConfirmed,
			DroppedRequeued:  flowStats.DroppedRequeued,
			RevokedFlow:      flowStats.RevokedFlow,
			FailedFlow:       flowStats.FailedFlow,
			TotalTracked:     flowStats.TotalTracked,
			AvgStageCount:    flowStats.AvgStageCount,
		}
	}

	// Include realistic test metrics if pattern is realistic
	if lg.testConfig.Pattern == types.PatternRealistic {
		result.TipHistogram = lg.metricsCol.GetTipHistogram(lg.testConfig.RealisticConfig)
		result.TxTypeMetrics = lg.metricsCol.GetTxTypeMetrics()
		result.AccountsFunded = lg.accountMgr.GetAccountsFunded()
		result.AccountsActive = len(lg.accountMgr.GetDynamicAccounts())
	}

	// Include initialization progress if initializing
	if status == types.StatusInitializing {
		result.InitPhase = lg.initPhase
		result.InitProgress = lg.initProgress
		result.AccountsTotal = lg.initAccountsTotal
		result.AccountsGenerated = lg.initAccountsGen
		result.FundingTxsSent = lg.initFundingSent
		result.FundingTxsTotal = lg.initFundingTotal
		result.ContractsDeployed = lg.initContractsDone
		result.ContractsTotal = lg.initContractsTotal
	}

	// Include verification progress if verifying
	if status == types.StatusVerifying {
		lg.statusMu.RLock()
		result.VerifyPhase = lg.verifyPhase
		result.VerifyProgress = lg.verifyProgress
		result.BlocksToVerify = lg.blocksToVerify
		result.BlocksVerified = lg.blocksVerified
		result.ReceiptsToSample = lg.receiptsToSample
		result.ReceiptsSampled = lg.receiptsSampled
		lg.statusMu.RUnlock()
	}

	// Include warnings if any
	lg.warningsMu.RLock()
	if len(lg.warnings) > 0 {
		result.Warnings = make([]string, len(lg.warnings))
		copy(result.Warnings, lg.warnings)
	}
	lg.warningsMu.RUnlock()

	return result
}

// GetHistory returns the history of completed tests.
func (lg *LoadGenerator) GetHistory() []types.TestResult {
	lg.testHistoryMu.RLock()
	defer lg.testHistoryMu.RUnlock()

	result := make([]types.TestResult, len(lg.testHistory))
	copy(result, lg.testHistory)
	return result
}

// GetHistoryPaginated returns paginated test history from storage.
func (lg *LoadGenerator) GetHistoryPaginated(limit, offset int) (*storage.PaginatedTestRuns, error) {
	if lg.storage == nil {
		return &storage.PaginatedTestRuns{Runs: []storage.TestRun{}, Total: 0, Limit: limit, Offset: offset}, nil
	}
	return lg.storage.ListTestRuns(context.Background(), limit, offset)
}

// GetTestRunDetail returns a single test run with time-series data.
func (lg *LoadGenerator) GetTestRunDetail(id string) (*storage.TestRunDetail, error) {
	if lg.storage == nil {
		return nil, nil
	}

	run, err := lg.storage.GetTestRun(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil
	}

	timeSeries, err := lg.storage.GetTimeSeries(context.Background(), id)
	if err != nil {
		return nil, err
	}

	return &storage.TestRunDetail{
		Run:        run,
		TimeSeries: timeSeries,
	}, nil
}

// GetTestRunTransactions returns paginated transaction logs for a test run.
func (lg *LoadGenerator) GetTestRunTransactions(id string, limit, offset int) (*storage.PaginatedTxLogs, error) {
	if lg.storage == nil {
		return &storage.PaginatedTxLogs{Transactions: []storage.TxLogEntry{}, Total: 0, Limit: limit, Offset: offset}, nil
	}
	return lg.storage.GetTxLogs(context.Background(), id, limit, offset)
}

// DeleteTestRun deletes a test run and all associated data.
func (lg *LoadGenerator) DeleteTestRun(id string) error {
	if lg.storage == nil {
		return nil
	}
	return lg.storage.DeleteTestRun(context.Background(), id)
}

// UpdateTestRunMetadata updates the custom name and/or favorite status of a test run.
func (lg *LoadGenerator) UpdateTestRunMetadata(id string, update *storage.TestRunMetadataUpdate) error {
	if lg.storage == nil {
		return nil
	}
	return lg.storage.UpdateTestRunMetadata(context.Background(), id, update)
}

// CheckL2RPC checks L2 RPC connectivity.
func (lg *LoadGenerator) CheckL2RPC() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := lg.l2Client.GetBlockNumber(ctx)
	return err
}

// CheckBuilderRPC checks builder RPC connectivity.
func (lg *LoadGenerator) CheckBuilderRPC() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := lg.builderClient.GetBlockNumber(ctx)
	return err
}

// Reset resets the test state.
func (lg *LoadGenerator) Reset() {
	lg.statusMu.Lock()
	if lg.status == types.StatusRunning {
		lg.statusMu.Unlock()
		return
	}
	lg.status = types.StatusIdle
	lg.testError = ""
	lg.statusMu.Unlock()

	lg.metricsCol.Reset()
	lg.preconfLatencies.Reset()
	atomic.StoreInt64(&lg.currentRate, 0)
	atomic.StoreInt64(&lg.peakRate, 0)
	atomic.StoreInt64(&lg.pendingCount, 0)
	atomic.StoreUint64(&lg.lastPreconfSeqNum, 0)
	atomic.StoreUint64(&lg.preconfGaps, 0)
	lg.discardedCount = 0
	lg.startTime = time.Time{}
	lg.currentDuration = 0
	lg.currentTPS = 0

	// Clear warnings
	lg.warningsMu.Lock()
	lg.warnings = nil
	lg.warningsMu.Unlock()

	// Reset circuit breaker state
	atomic.StoreInt64(&lg.recentSends, 0)
	atomic.StoreInt64(&lg.recentFails, 0)
	atomic.StoreInt64(&lg.recentRevocations, 0)
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.preCircuitRate, 0)
	atomic.StoreInt32(&lg.nonceResyncNeeded, 0)

	// Reset backpressure monitoring
	lg.builderPressureMu.Lock()
	lg.builderPressure = 0
	lg.latestBaseFeeGwei = 0
	lg.latestGasPriceGwei = 0
	lg.latestGasUsed = 0
	lg.builderPressureMu.Unlock()

	// Reset gas pricing
	lg.gasTipCap = nil
	lg.gasFeeCap = nil

	// Reset block metrics
	lg.blockMetricsMu.Lock()
	lg.blockMetrics = nil
	lg.cumulativeGasUsed = 0
	lg.cumulativeGasLimit = 0
	lg.rollingGasWindow = nil
	lg.rollingTxWindow = nil
	lg.peakMgasPerSec = 0
	lg.lastMgasPerSec = 0 // Reset cached MGas/s value
	lg.peakTxPerSec = 0
	lg.lastTxPerSec = 0 // Reset cached TX/s value
	lg.lastBlockTime = time.Time{}
	lg.lastBlockNumber = 0
	lg.firstBlockNumber = 0
	lg.lastRecordedBlock = 0
	lg.totalBlockCount = 0
	lg.testStartBlockNumber = 0
	lg.testEndBlockNumber = 0
	lg.rpcLastBlockNumber = 0
	lg.blockMetricsMu.Unlock()

	// Reset incremental verification state
	lg.incrementalSnapshotsMu.Lock()
	lg.incrementalSnapshots = nil
	lg.incrementalSnapshotsMu.Unlock()
	lg.recentBlockNumbersMu.Lock()
	lg.recentBlockNumbers = nil
	lg.recentBlockNumbersMu.Unlock()
	lg.recentConfirmedMu.Lock()
	lg.recentConfirmedHashes = nil
	lg.recentConfirmedMu.Unlock()
	lg.txOrdering = ""

	// Clear pending transactions map
	// NOTE: sync.Map explicitly supports Delete during Range - this is safe per Go docs:
	// "if the value for any key is stored or deleted concurrently (including by f),
	// Range may reflect any mapping for that key from any point during the Range call"
	lg.pendingTxs.Range(func(key, _ any) bool {
		lg.pendingTxs.Delete(key)
		return true
	})

	lg.logger.Info("test reset")
}

// RecycleFunds sends remaining funds from dynamic accounts back to faucets.
func (lg *LoadGenerator) RecycleFunds() (int, error) {
	lg.statusMu.RLock()
	status := lg.status
	lg.statusMu.RUnlock()

	if status == types.StatusRunning {
		return 0, fmt.Errorf("cannot recycle funds while test is running")
	}

	return lg.accountMgr.RecycleFunds(context.Background(), lg.builderClient)
}