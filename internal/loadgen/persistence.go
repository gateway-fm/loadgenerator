package loadgen

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	"github.com/gateway-fm/loadgenerator/internal/verification"
	"github.com/gateway-fm/loadgenerator/internal/workload"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func (lg *LoadGenerator) recordTxSent(txHash common.Hash, sentAt time.Time, account int, nonce uint64, tipGwei float64) {
	if !lg.txLoggingEnabled {
		return
	}

	entry := &storage.TxLogEntry{
		TxHash:      txHash.Hex(),
		SentAtMs:    sentAt.UnixMilli(),
		Status:      "pending",
		FromAccount: account,
		Nonce:       nonce,
		GasTipGwei:  tipGwei,
	}

	// Store in map for later confirmation tracking.
	// Use common.Hash ([32]byte) as key instead of txHash.Hex() string to avoid
	// 66-byte string allocation per TX (~94 MB savings at high throughput).
	lg.pendingTxs.Store(txHash, entry)

	// Append to buffer
	lg.txLogBufMu.Lock()
	lg.txLogBuf = append(lg.txLogBuf, *entry)
	lg.txLogBufMu.Unlock()
}

// recordTxConfirmed updates a transaction as confirmed (non-blocking).
func (lg *LoadGenerator) recordTxConfirmed(txHash common.Hash, confirmedAt time.Time) {
	if !lg.txLoggingEnabled {
		return
	}

	if entry, ok := lg.pendingTxs.Load(txHash); ok {
		e := entry.(*storage.TxLogEntry)
		e.ConfirmedAtMs = confirmedAt.UnixMilli()
		e.ConfirmLatencyMs = e.ConfirmedAtMs - e.SentAtMs
		e.Status = "confirmed"
		// NOTE: Don't delete from pendingTxs here - we need confirmed entries for
		// TX receipt verification at test end. The map is cleared in initBuffers.
	}
}

// resolvePendingViaReceipts resolves each still-pending transaction by its
// on-chain receipt, independent of the throughput block-window. A tx with a
// successful receipt landed on-chain (typically a block or two after the window
// closed) and is reclassified as confirmed; one with no receipt stays pending
// and is later discarded. This separates per-tx success (receipt lookup) from
// throughput measurement (the [start,end] block range), so late-but-included
// txs are not mislabeled as discarded. Returns the number reclassified.
//
// Safe to count: a still-"pending" map entry was never touched by the poller (the
// poller marks anything it sees "confirmed"), so its sent-time is still tracked
// and RecordTxConfirmed increments the confirmed counter.
func (lg *LoadGenerator) resolvePendingViaReceipts() uint64 {
	if !lg.txLoggingEnabled || lg.l2Client == nil {
		return 0
	}

	var pending []common.Hash
	lg.pendingTxs.Range(func(key, value any) bool {
		if e, ok := value.(*storage.TxLogEntry); ok && e.Status == "pending" {
			if h, ok2 := key.(common.Hash); ok2 {
				pending = append(pending, h)
			}
		}
		return true
	})
	if len(pending) == 0 {
		return 0
	}

	lg.logger.Info("resolving pending txs via on-chain receipts", "count", len(pending))
	lg.statusMu.Lock()
	lg.verifyProgress = fmt.Sprintf("Resolving %d pending transactions via receipts...", len(pending))
	lg.statusMu.Unlock()

	const workers = 16
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var resolved, notFound, errored, reverted uint64
	now := time.Now()

	for _, h := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(hash common.Hash) {
			defer wg.Done()
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			receipt, err := lg.l2Client.GetTransactionReceipt(ctx, hash.Hex())
			switch {
			case err != nil:
				atomic.AddUint64(&errored, 1)
				return // lookup failed -> stays pending -> discarded
			case receipt == nil:
				atomic.AddUint64(&notFound, 1)
				return // not on-chain -> genuinely pending/dropped -> discarded
			case receipt.Status != 1:
				atomic.AddUint64(&reverted, 1)
				return // landed but reverted -> not a success
			}
			// On-chain and successful: count it as a (late) confirmation, mirroring
			// the chain poller's confirmation path.
			lg.metricsCol.RecordTxConfirmed(hash, now)
			lg.recordTxConfirmed(hash, now)
			metrics.AtomicSubSaturating(&lg.pendingCount, 1)
			atomic.AddUint64(&resolved, 1)
		}(h)
	}
	wg.Wait()
	lg.logger.Info("pending-tx receipt resolution complete",
		"checked", len(pending),
		"confirmedLate", resolved,
		"notOnChain", notFound,
		"reverted", reverted,
		"lookupErrors", errored)
	return resolved
}

// recordTxPreconfirmed updates a transaction with preconfirmation time (non-blocking).
func (lg *LoadGenerator) recordTxPreconfirmed(txHash common.Hash, preconfAt time.Time) {
	if !lg.txLoggingEnabled {
		return
	}

	if entry, ok := lg.pendingTxs.Load(txHash); ok {
		e := entry.(*storage.TxLogEntry)
		e.PreconfAtMs = preconfAt.UnixMilli()
		e.PreconfLatencyMs = e.PreconfAtMs - e.SentAtMs
	}
}

func (lg *LoadGenerator) saveTestResult() {
	snapshot := lg.metricsCol.GetSnapshot()
	elapsed := time.Since(lg.startTime)

	var avgTPS float64
	if elapsed.Seconds() > 0 {
		avgTPS = float64(snapshot.TxSent) / elapsed.Seconds()
	}

	latencyStats := lg.metricsCol.GetLatencyStats()
	preconfStats := lg.preconfLatencies.GetStats()

	// Get flow stats
	var flowStats *types.TxFlowStats
	if fs := lg.metricsCol.GetFlowStats(); fs != nil {
		flowStats = &types.TxFlowStats{
			DirectConfirmed:  fs.DirectConfirmed,
			PendingConfirmed: fs.PendingConfirmed,
			PreconfConfirmed: fs.PreconfConfirmed,
			DroppedRequeued:  fs.DroppedRequeued,
			RevokedFlow:      fs.RevokedFlow,
			FailedFlow:       fs.FailedFlow,
			TotalTracked:     fs.TotalTracked,
			AvgStageCount:    fs.AvgStageCount,
		}
	}

	result := types.TestResult{
		ID:              lg.currentTestID,
		StartedAt:       lg.startTime,
		CompletedAt:     time.Now(),
		Pattern:         lg.testConfig.Pattern,
		TransactionType: lg.currentTxType,
		DurationMs:      elapsed.Milliseconds(),
		TxSent:          snapshot.TxSent,
		TxConfirmed:     snapshot.TxConfirmed,
		TxFailed:        snapshot.TxFailed,
		TxDiscarded:     lg.discardedCount,
		AverageTPS:      avgTPS,
		PeakTPS:         int(atomic.LoadInt64(&lg.peakRate)),
		Latency:         latencyStats,
		PreconfLatency:  preconfStats,
		FlowStats:       flowStats,
		Config:          lg.testConfig,
	}

	// Add to in-memory cache (backwards compatibility)
	lg.testHistoryMu.Lock()
	lg.testHistory = append(lg.testHistory, result)
	// Keep only last 100 results in memory
	if len(lg.testHistory) > 100 {
		lg.testHistory = lg.testHistory[len(lg.testHistory)-100:]
	}
	lg.testHistoryMu.Unlock()

	// Persist to storage (all writes happen here, AFTER test completes)
	if lg.storage != nil {
		lg.persistTestData(snapshot, avgTPS, latencyStats, preconfStats)
	}
}

// persistTestData writes all test data to storage (called after test completes).
func (lg *LoadGenerator) persistTestData(snapshot metrics.Snapshot, avgTPS float64, latencyStats, preconfStats *types.LatencyStats) {
	// Use a timeout context for verification to prevent hanging indefinitely
	// if L2 RPC is slow or unresponsive
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Calculate aggregate block metrics from time series buffer
	var totalGasUsed, totalGasLimit uint64
	var totalBlocks int
	var peakMgasPerSec float64
	var fillRateSum float64
	var fillRateCount int

	for _, p := range lg.timeSeriesBuf {
		totalGasUsed += p.GasUsed
		totalGasLimit += p.GasLimit
		totalBlocks += p.BlockCount
		if p.MgasPerSec > peakMgasPerSec {
			peakMgasPerSec = p.MgasPerSec
		}
		if p.FillRate > 0 {
			fillRateSum += p.FillRate
			fillRateCount++
		}
	}

	var avgMgasPerSec, avgFillRate float64
	// Calculate average MGas/s from total gas and actual duration.
	// Don't use sum(per-period MgasPerSec)/count - that's wrong because many
	// time series samples have 0 MGas/s (no blocks in that sampling period).
	durationSec := time.Since(lg.startTime).Seconds()
	if durationSec > 0 && totalGasUsed > 0 {
		avgMgasPerSec = float64(totalGasUsed) / 1_000_000 / durationSec
	}
	if fillRateCount > 0 {
		avgFillRate = fillRateSum / float64(fillRateCount)
	}

	// Get on-chain verification metrics by querying blocks. Skipped on a force
	// stop — the user aborted the run, so we don't scan the chain; the live
	// counts are persisted as-is.
	var onChainMetrics onChainMetricsResult
	if atomic.LoadInt32(&lg.forceStop) == 1 {
		lg.logger.Info("force stop: skipping on-chain verification")
	} else {
		onChainMetrics = lg.calculateOnChainMetrics(ctx)
	}

	// Fallback: If time series block metrics are all 0 (WebSocket failed), use on-chain metrics
	if totalBlocks == 0 && totalGasUsed == 0 && onChainMetrics.firstBlock > 0 && onChainMetrics.lastBlock >= onChainMetrics.firstBlock {
		totalBlocks = int(onChainMetrics.lastBlock - onChainMetrics.firstBlock + 1)
		totalGasUsed = onChainMetrics.gasUsed
		peakMgasPerSec = onChainMetrics.mgasPerSec // Use avg as peak when we only have aggregates
		avgMgasPerSec = onChainMetrics.mgasPerSec
		// Can't calculate fill rate without gasLimit, leave at 0
		lg.logger.Info("using on-chain metrics as fallback for block metrics (WebSocket was unavailable)",
			"blockCount", totalBlocks, "gasUsed", totalGasUsed, "mgasPerSec", avgMgasPerSec)
	}

	// Fetch environment snapshot (builder + load-gen config)
	environment := lg.fetchBuilderConfig()

	// Collect confirmed TX hashes for receipt sampling
	var confirmedTxHashes []string
	lg.pendingTxs.Range(func(key, value any) bool {
		entry := value.(*storage.TxLogEntry)
		if entry.Status == "confirmed" {
			confirmedTxHashes = append(confirmedTxHashes, entry.TxHash)
		}
		return true
	})

	// Run verification
	// Compare on-chain tx count against txSent - the on-chain blocks contain all TXs we sent,
	// including those that were "pending" from our tracking perspective (confirmation notification
	// not yet received when test stopped). If we sent N TXs and on-chain shows N, that's a match.
	var verificationResult *storage.VerificationResult
	if lg.l2Client != nil && atomic.LoadInt32(&lg.forceStop) == 0 {
		// Check if we have incremental verification snapshots
		incrementalSnapshots := lg.getIncrementalSnapshots()

		txOrdering := ""
		includeDepositTx := false
		if environment != nil {
			txOrdering = environment.BuilderTxOrdering
			includeDepositTx = environment.BuilderIncludeDepositTx
		}

		if len(incrementalSnapshots) > 0 {
			// Use incremental verification results (much faster for long tests)
			lg.logger.Info("using incremental verification",
				"snapshots", len(incrementalSnapshots),
				"onChainTxCount", onChainMetrics.txCount,
				"txConfirmed", snapshot.TxConfirmed)

			// Update progress - aggregating incremental snapshots
			lg.statusMu.Lock()
			lg.verifyPhase = types.VerifyPhaseAggregating
			lg.verifyProgress = fmt.Sprintf("Aggregating %d verification snapshots...", len(incrementalSnapshots))
			lg.blocksToVerify = len(incrementalSnapshots)
			lg.blocksVerified = 0
			lg.statusMu.Unlock()

			verifier := verification.NewVerifierWithConfig(lg.l2Client, lg.logger, includeDepositTx)
			verificationResult = verifier.AggregateSnapshots(
				incrementalSnapshots,
				onChainMetrics.txCount,
				snapshot.TxConfirmed,
				txOrdering,
			)

			// Update progress - aggregation complete
			lg.statusMu.Lock()
			lg.blocksVerified = len(incrementalSnapshots)
			lg.verifyProgress = "Aggregation complete, finalizing results..."
			lg.statusMu.Unlock()
		} else {
			// Fall back to full verification for short tests
			lg.logger.Info("using full verification (no incremental snapshots)")

			// Create progress callback to update verification state
			progressCb := func(p verification.VerificationProgress) {
				lg.statusMu.Lock()
				lg.verifyPhase = p.Phase
				lg.verifyProgress = p.Message
				lg.blocksToVerify = p.BlocksTotal
				lg.blocksVerified = p.BlocksVerified
				lg.receiptsToSample = p.ReceiptsTotal
				lg.receiptsSampled = p.ReceiptsSampled
				lg.statusMu.Unlock()
			}

			verifier := verification.NewVerifierWithConfig(lg.l2Client, lg.logger, includeDepositTx)
			verificationResult = verifier.VerifyTestResultsWithProgress(
				ctx,
				onChainMetrics.txCount,
				snapshot.TxConfirmed,
				onChainMetrics.firstBlock,
				onChainMetrics.lastBlock,
				txOrdering,
				confirmedTxHashes,
				progressCb,
			)
		}
	}

	// Fetch signed header attestations for the on-chain block range when HSM/attestation is enabled.
	if verificationResult != nil &&
		environment != nil &&
		environment.BuilderBlockAttestationEnabled &&
		onChainMetrics.firstBlock > 0 &&
		onChainMetrics.lastBlock >= onChainMetrics.firstBlock {
		expected := int(onChainMetrics.lastBlock - onChainMetrics.firstBlock + 1)
		headerAttestations := lg.fetchHeaderAttestations(ctx, onChainMetrics.firstBlock, onChainMetrics.lastBlock)
		if len(headerAttestations) > 0 {
			verificationResult.HeaderAttestationExpected = expected
			verificationResult.HeaderAttestationFound = len(headerAttestations)
			verificationResult.HeaderAttestations = headerAttestations
		}
	}

	// 1. Complete the test run with final stats
	testRun := &storage.TestRun{
		ID:             lg.currentTestID,
		TxSent:         snapshot.TxSent,
		TxConfirmed:    snapshot.TxConfirmed,
		TxFailed:       snapshot.TxFailed,
		TxDiscarded:    lg.discardedCount,
		AverageTPS:     avgTPS,
		PeakTPS:        int(atomic.LoadInt64(&lg.peakRate)),
		LatencyStats:   latencyStats,
		PreconfLatency: preconfStats,
		PendingLatency: lg.metricsCol.GetPendingLatencyStats(),
		Status:         "completed",
		// Block metrics (from time series samples)
		BlockCount:     totalBlocks,
		TotalGasUsed:   totalGasUsed,
		AvgFillRate:    avgFillRate,
		PeakMgasPerSec: peakMgasPerSec,
		AvgMgasPerSec:  avgMgasPerSec,
		// On-chain verification metrics
		OnChainFirstBlock:   onChainMetrics.firstBlock,
		OnChainLastBlock:    onChainMetrics.lastBlock,
		OnChainTxCount:      onChainMetrics.txCount,
		OnChainGasUsed:      onChainMetrics.gasUsed,
		OnChainMgasPerSec:   onChainMetrics.mgasPerSec,
		OnChainTps:          onChainMetrics.tps,
		OnChainDurationSecs: onChainMetrics.durationSecs,
		// Environment and verification
		Environment:  environment,
		Verification: verificationResult,
	}

	// Add deployed contracts info
	lg.contractsMu.RLock()
	if lg.contractsDeployed {
		testRun.DeployedContracts = []storage.DeployedContract{}
		if lg.erc20Contract != (common.Address{}) {
			testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
				Name:    "ERC20",
				Address: lg.erc20Contract.Hex(),
			})
		}
		if lg.gasConsumerContract != (common.Address{}) {
			testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
				Name:    "GasConsumer",
				Address: lg.gasConsumerContract.Hex(),
			})
		}
		if lg.nftContract != (common.Address{}) {
			testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
				Name:    "NFT",
				Address: lg.nftContract.Hex(),
			})
		}
		// Add Uniswap V3 contracts if deployed
		if cb, ok := lg.txBuilderReg.GetComplexBuilder(types.TxTypeUniswapSwap); ok {
			if uniswapBuilder, ok := cb.(*txbuilder.UniswapV3SwapBuilder); ok && uniswapBuilder.IsDeployed() {
				contracts := uniswapBuilder.GetContracts()
				if contracts.WETH9 != (common.Address{}) {
					testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
						Name:    "WETH9",
						Address: contracts.WETH9.Hex(),
					})
				}
				if contracts.USDC != (common.Address{}) {
					testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
						Name:    "USDC",
						Address: contracts.USDC.Hex(),
					})
				}
				if contracts.Factory != (common.Address{}) {
					testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
						Name:    "UniswapV3Factory",
						Address: contracts.Factory.Hex(),
					})
				}
				if contracts.SwapRouter != (common.Address{}) {
					testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
						Name:    "SwapRouter",
						Address: contracts.SwapRouter.Hex(),
					})
				}
				if contracts.NonfungiblePositionManager != (common.Address{}) {
					testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
						Name:    "NonfungiblePositionManager",
						Address: contracts.NonfungiblePositionManager.Hex(),
					})
				}
				if contracts.Pool != (common.Address{}) {
					testRun.DeployedContracts = append(testRun.DeployedContracts, storage.DeployedContract{
						Name:    "WETH/USDC Pool",
						Address: contracts.Pool.Hex(),
					})
				}
			}
		}
	}
	lg.contractsMu.RUnlock()

	// Add test accounts info
	builtInAccounts := lg.accountMgr.GetAccounts()
	dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
	fundedCount := lg.accountMgr.GetAccountsFunded()

	testRun.TestAccounts = &storage.TestAccountsInfo{
		TotalCount:   len(builtInAccounts) + len(dynamicAccounts),
		DynamicCount: len(dynamicAccounts),
		FundedCount:  fundedCount,
	}

	// Add funder address (first built-in account acts as deployer)
	if len(builtInAccounts) > 0 {
		testRun.TestAccounts.FunderAddress = builtInAccounts[0].Address.Hex()
	}

	// Build AllAccounts with roles
	// Account 0: deployer (reserved for contract deployment)
	// Accounts 1-9: funder (used to fund dynamic accounts)
	// Dynamic accounts: funded (generated and funded for test)
	allAccountsWithRoles := make([]storage.AccountInfo, 0, len(builtInAccounts)+len(dynamicAccounts))

	for i, acc := range builtInAccounts {
		var role storage.AccountRole
		if i == 0 {
			role = storage.AccountRoleDeployer
		} else {
			role = storage.AccountRoleFunder
		}
		allAccountsWithRoles = append(allAccountsWithRoles, storage.AccountInfo{
			Address: acc.Address.Hex(),
			Role:    role,
			Index:   i,
		})
	}

	for i, acc := range dynamicAccounts {
		allAccountsWithRoles = append(allAccountsWithRoles, storage.AccountInfo{
			Address: acc.Address.Hex(),
			Role:    storage.AccountRoleFunded,
			Index:   i,
		})
	}

	testRun.TestAccounts.AllAccounts = allAccountsWithRoles

	// Legacy: keep first 100 addresses for backwards compatibility
	maxAccounts := 100
	totalLegacy := len(builtInAccounts) + len(dynamicAccounts)
	if totalLegacy > maxAccounts {
		totalLegacy = maxAccounts
	}
	testRun.TestAccounts.Accounts = make([]string, 0, totalLegacy)
	for i := 0; i < len(builtInAccounts) && len(testRun.TestAccounts.Accounts) < maxAccounts; i++ {
		testRun.TestAccounts.Accounts = append(testRun.TestAccounts.Accounts, builtInAccounts[i].Address.Hex())
	}
	for i := 0; i < len(dynamicAccounts) && len(testRun.TestAccounts.Accounts) < maxAccounts; i++ {
		testRun.TestAccounts.Accounts = append(testRun.TestAccounts.Accounts, dynamicAccounts[i].Address.Hex())
	}

	// Add realistic test specific metrics if applicable (both realistic and adaptive-realistic)
	if lg.testConfig.Pattern == types.PatternRealistic || lg.testConfig.Pattern == types.PatternAdaptiveRealistic {
		// Get the realistic config (use provided or defaults for adaptive-realistic)
		realisticCfg := lg.testConfig.RealisticConfig
		if realisticCfg == nil {
			realisticCfg = workload.DefaultRealisticConfig()
		}
		testRun.TipHistogram = lg.metricsCol.GetTipHistogram(realisticCfg)
		testRun.TxTypeMetrics = lg.metricsCol.GetTxTypeMetrics()
		testRun.AccountsActive = len(lg.accountMgr.GetDynamicAccounts())
		testRun.AccountsFunded = lg.accountMgr.GetAccountsFunded()
	}

	if err := lg.storage.CompleteTestRun(ctx, lg.currentTestID, testRun); err != nil {
		lg.logger.Error("failed to complete test run in storage", "error", err)
	}

	// 2. Bulk insert time-series data
	if len(lg.timeSeriesBuf) > 0 {
		if err := lg.storage.BulkInsertTimeSeries(ctx, lg.currentTestID, lg.timeSeriesBuf); err != nil {
			lg.logger.Error("failed to persist time-series", "error", err, "count", len(lg.timeSeriesBuf))
		} else {
			lg.logger.Info("persisted time-series data", "count", len(lg.timeSeriesBuf))
		}
	}

	// 3. Bulk insert TX logs ASYNCHRONOUSLY (only if enabled)
	// TX logs can be large (100k+ rows) and slow to write, so we do this in the background
	// to avoid blocking the stop operation. The test is already "complete" at this point.
	if lg.txLoggingEnabled && len(lg.txLogBuf) > 0 {
		// Update TX log entries with final status from pendingTxs map
		lg.txLogBufMu.Lock()
		for i := range lg.txLogBuf {
			if entry, ok := lg.pendingTxs.Load(common.HexToHash(lg.txLogBuf[i].TxHash)); ok {
				e := entry.(*storage.TxLogEntry)
				lg.txLogBuf[i].ConfirmedAtMs = e.ConfirmedAtMs
				lg.txLogBuf[i].PreconfAtMs = e.PreconfAtMs
				lg.txLogBuf[i].ConfirmLatencyMs = e.ConfirmLatencyMs
				lg.txLogBuf[i].PreconfLatencyMs = e.PreconfLatencyMs
				lg.txLogBuf[i].Status = e.Status
			}
		}
		// Copy the slice to avoid race conditions with the async goroutine
		txLogs := make([]storage.TxLogEntry, len(lg.txLogBuf))
		copy(txLogs, lg.txLogBuf)
		lg.txLogBufMu.Unlock()

		// Persist TX logs asynchronously - don't block the stop operation
		testID := lg.currentTestID
		storage := lg.storage
		logger := lg.logger
		go func() {
			start := time.Now()
			if err := storage.BulkInsertTxLogs(context.Background(), testID, txLogs); err != nil {
				logger.Error("failed to persist TX logs", "error", err, "count", len(txLogs))
			} else {
				logger.Info("persisted TX logs (async)", "count", len(txLogs), "duration", time.Since(start))
			}
		}()
		lg.logger.Info("TX log persistence started in background", "count", len(txLogs))
	}
}
