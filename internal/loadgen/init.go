package loadgen

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/pattern"
	"github.com/gateway-fm/loadgenerator/internal/ratelimit"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/sender"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func (lg *LoadGenerator) runInitialization(req types.StartTestRequest) {
	// Defer error handling - if we panic or error, set status to error
	defer func() {
		if r := recover(); r != nil {
			lg.setError(fmt.Sprintf("initialization panic: %v", r))
		}
	}()

	// Clear any warnings from previous test
	lg.warningsMu.Lock()
	lg.warnings = nil
	lg.warningsMu.Unlock()

	// Set defaults
	if req.TransactionType == "" {
		req.TransactionType = types.TxTypeEthTransfer
	}
	lg.currentTxType = req.TransactionType

	// Auto-calculate required accounts based on target TPS
	// Formula: accounts = targetTPS * blockTimeSec * safetyMargin
	numAccounts := req.NumAccounts
	// For realistic tests, use the numAccounts from the realistic config
	if numAccounts <= 0 && req.RealisticConfig != nil && req.RealisticConfig.NumAccounts > 0 {
		numAccounts = req.RealisticConfig.NumAccounts
	}
	if numAccounts <= 0 {
		// Determine target TPS for account calculation
		targetTPSForAccounts := req.ConstantRate
		if targetTPSForAccounts == 0 {
			targetTPSForAccounts = req.RampEnd
		}
		if targetTPSForAccounts == 0 {
			targetTPSForAccounts = req.SpikeRate
		}
		if targetTPSForAccounts == 0 && req.RealisticConfig != nil {
			targetTPSForAccounts = req.RealisticConfig.TargetTPS
		}

		// For "max" pattern, use a default of 500 accounts or scale from initial rate
		if req.Pattern == types.PatternAdaptive {
			if req.AdaptiveInitialRate > 0 {
				numAccounts = config.CalculateRequiredAccounts(req.AdaptiveInitialRate*2, lg.cfg.BlockTimeMS)
			} else {
				numAccounts = config.MinAccountsForAdaptive
			}
		} else if targetTPSForAccounts > 0 {
			numAccounts = config.CalculateRequiredAccounts(targetTPSForAccounts, lg.cfg.BlockTimeMS)
		} else {
			numAccounts = 10 // Fallback default
		}

		lg.logger.Info("auto-calculated accounts based on TPS",
			"targetTPS", targetTPSForAccounts,
			"blockTimeMS", lg.cfg.BlockTimeMS,
			"accounts", numAccounts)
	} else {
		// User specified numAccounts - check if sufficient for target TPS
		targetTPS := req.ConstantRate
		if targetTPS == 0 {
			targetTPS = req.RampEnd
		}
		if targetTPS == 0 {
			targetTPS = req.SpikeRate
		}
		if targetTPS == 0 && req.RealisticConfig != nil {
			targetTPS = req.RealisticConfig.TargetTPS
		}
		if targetTPS == 0 && req.AdaptiveInitialRate > 0 {
			targetTPS = req.AdaptiveInitialRate * 2 // Adaptive can double
		}

		if warning := config.CheckAccountSufficiency(numAccounts, targetTPS, lg.cfg.BlockTimeMS); warning != "" {
			lg.warningsMu.Lock()
			lg.warnings = append(lg.warnings, warning)
			lg.warningsMu.Unlock()
			lg.logger.Warn("insufficient accounts for target TPS",
				"numAccounts", numAccounts,
				"targetTPS", targetTPS,
				"recommended", config.CalculateRequiredAccounts(targetTPS, lg.cfg.BlockTimeMS),
				"achievableTPS", config.EstimateMaxTPS(numAccounts, lg.cfg.BlockTimeMS))
		}
	}

	// Update init progress
	lg.initAccountsTotal = numAccounts

	accounts := lg.accountMgr.GetAccounts()
	if numAccounts > len(accounts) {
		dynamicCount := numAccounts - len(accounts)
		chainID := lg.cfg.ChainID

		// Try warm start from cached accounts
		warmStartOK := false
		if lg.cacheStorage != nil {
			warmStartOK = lg.tryWarmStartAccounts(dynamicCount, chainID)
		}

		if !warmStartOK {
			// Cold start: generate + fund from scratch
			lg.initPhase = types.InitPhaseGeneratingAccts
			lg.initProgress = fmt.Sprintf("Generating %d accounts...", dynamicCount)
			lg.logger.Info("generating dynamic accounts", "count", dynamicCount)

			if err := lg.accountMgr.GenerateDynamicAccounts(dynamicCount); err != nil {
				lg.setError(fmt.Sprintf("failed to generate accounts: %v", err))
				return
			}
			lg.initAccountsGen = dynamicCount

			if err := lg.resetBuilderNonces(); err != nil {
				lg.setError(fmt.Sprintf("failed to reset builder nonces: %v (cannot start test with stale cache)", err))
				return
			}

			lg.initPhase = types.InitPhaseFundingAccts
			lg.initFundingTotal = dynamicCount
			lg.initProgress = fmt.Sprintf("Funding %d accounts from faucet...", dynamicCount)
			lg.logger.Info("funding dynamic accounts from faucet")

			fundCtx, fundCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer fundCancel()

			if err := lg.accountMgr.FundDynamicAccounts(fundCtx, lg.builderClient, lg.l2Client); err != nil {
				lg.logger.Warn("failed to fund some dynamic accounts", "error", err)
			}
			lg.initFundingSent = lg.accountMgr.GetAccountsFunded()
			if lg.initFundingSent == 0 {
				lg.setError(fmt.Sprintf("failed to fund any accounts (0/%d funded) - cannot start test", dynamicCount))
				return
			}

			lg.initPhase = types.InitPhaseWaitingForFunding
			fundedCount := lg.accountMgr.GetAccountsFunded()
			blocksNeeded := (fundedCount / 4000) + 3
			waitTime := time.Duration(blocksNeeded) * time.Second
			if waitTime < 5*time.Second {
				waitTime = 5 * time.Second
			}
			lg.initProgress = fmt.Sprintf("Waiting for %d funding TXs to be included (~%ds)...", fundedCount, int(waitTime.Seconds()))
			lg.logger.Info("waiting for funding transactions to be included",
				"fundedAccounts", fundedCount,
				"blocksNeeded", blocksNeeded,
				"waitTime", waitTime)
			time.Sleep(waitTime)

			lg.initPhase = types.InitPhaseInitNonces
			lg.initProgress = "Initializing nonces for dynamic accounts..."
			if err := lg.accountMgr.InitializeDynamicNonces(fundCtx, lg.builderClient); err != nil {
				lg.logger.Warn("failed to initialize dynamic nonces", "error", err)
			}

			// Persist newly generated accounts for future reuse
			lg.saveDynamicAccountsToCache(chainID)
		}
	}

	// Initialize nonces for built-in accounts
	lg.initPhase = types.InitPhaseInitNonces
	lg.initProgress = "Initializing nonces for built-in accounts..."
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CRITICAL: Use builderClient to sync through builder (eth_getPendingNonce)
	// This ensures load generator and builder have the same nonce view, preventing
	// "nonce ahead" errors when builder has cached nonces from previous tests
	if err := lg.accountMgr.InitializeNonces(ctx, lg.builderClient, min(numAccounts, len(accounts))); err != nil {
		lg.setError(fmt.Sprintf("failed to initialize nonces: %v", err))
		return
	}

	// Set up EIP-1559 gas pricing
	lg.gasTipCap = big.NewInt(lg.cfg.GasTipCap)
	if lg.cfg.GasFeeCap > 0 {
		// Explicit fee cap configured
		lg.gasFeeCap = big.NewInt(lg.cfg.GasFeeCap)
	} else {
		// Auto-calculate from chain's gas price (query L2 node)
		gasPrice, err := lg.l2Client.GetGasPrice(ctx)
		if err != nil {
			lg.logger.Warn("failed to query gas price, using 2x tip as fee cap", "error", err)
			lg.gasFeeCap = new(big.Int).Mul(lg.gasTipCap, big.NewInt(2))
		} else {
			// Use 2x queried price for headroom against base fee fluctuation
			lg.gasFeeCap = new(big.Int).Mul(big.NewInt(int64(gasPrice)), big.NewInt(2))
		}
	}

	// CRITICAL: Ensure gasFeeCap is above current baseFee to avoid silent rejections
	// Query baseFee directly from latest block and ensure we're at least 2x above it
	if baseFee, err := lg.l2Client.GetBaseFee(ctx); err == nil && baseFee > 0 {
		minFeeCap := new(big.Int).Mul(big.NewInt(int64(baseFee)), big.NewInt(2))
		if lg.gasFeeCap.Cmp(minFeeCap) < 0 {
			lg.logger.Warn("gasFeeCap below 2x baseFee, adjusting to prevent rejections",
				"oldFeeCap", lg.gasFeeCap,
				"newFeeCap", minFeeCap,
				"baseFee", baseFee,
				"baseFeeGwei", float64(baseFee)/1e9,
			)
			lg.gasFeeCap = minFeeCap
		}
		lg.logger.Info("gas pricing configured",
			"gasTipCap", lg.gasTipCap,
			"gasFeeCap", lg.gasFeeCap,
			"baseFee", baseFee,
			"baseFeeGwei", float64(baseFee)/1e9,
		)
	} else {
		lg.logger.Info("gas pricing configured (baseFee query failed, using calculated values)",
			"gasTipCap", lg.gasTipCap,
			"gasFeeCap", lg.gasFeeCap,
			"baseFeeError", err,
		)
	}

	// Deploy contracts if needed for non-ETH-transfer types
	// For realistic mode, check if any non-ETH tx types are configured
	txTypeForDeploy := req.TransactionType
	if req.Pattern == types.PatternRealistic && req.RealisticConfig != nil {
		ratios := req.RealisticConfig.TxTypeRatios
		if ratios.UniswapSwap > 0 {
			// Uniswap needs special complex builder deployment
			txTypeForDeploy = types.TxTypeUniswapSwap
		} else if ratios.ERC20Transfer > 0 || ratios.ERC20Approve > 0 ||
			ratios.StorageWrite > 0 || ratios.HeavyCompute > 0 {
			// Other contract types use standard deployment
			txTypeForDeploy = types.TxTypeERC20Transfer
		}
	}

	// Phase: Deploying contracts
	if txTypeForDeploy != types.TxTypeEthTransfer {
		lg.initPhase = types.InitPhaseDeployingContracts
		lg.initProgress = "Deploying test contracts..."
		if txTypeForDeploy == types.TxTypeUniswapSwap {
			// Uniswap V3: 7 steps (WETH9, USDC, Factory, SwapRouter, NFTManager, Pool, Liquidity) + 3 base
			lg.initContractsTotal = 10
		} else {
			lg.initContractsTotal = 3 // ERC20, GasConsumer, NFT
		}
	}

	if err := lg.ensureContractsDeployed(txTypeForDeploy); err != nil {
		lg.setError(fmt.Sprintf("failed to deploy contracts: %v", err))
		return
	}
	// Note: initContractsDone is now updated incrementally via progress callbacks

	// Pre-mint NFTs for erc721-transfer load tests (setup-only, not load-test traffic).
	if req.TransactionType == types.TxTypeERC721Transfer && req.Erc721PreMint > 0 {
		lg.contractsMu.RLock()
		nftAddr := lg.nftContract
		lg.contractsMu.RUnlock()

		if nftAddr == (common.Address{}) {
			lg.setError("nft contract address unavailable for pre-mint")
			return
		}

		accounts := lg.accountMgr.GetAccounts()
		if len(accounts) == 0 {
			lg.setError("no accounts available for pre-mint")
			return
		}
		minter := accounts[0]

		lg.statusMu.Lock()
		lg.initProgress = fmt.Sprintf("Pre-minting NFTs (0/%d)...", req.Erc721PreMint)
		lg.statusMu.Unlock()

		preMintProgress := func(minted, total int) {
			lg.statusMu.Lock()
			lg.initProgress = fmt.Sprintf("Pre-minting NFTs (%d/%d)...", minted, total)
			lg.statusMu.Unlock()
		}

		preMintCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if err := lg.deployer.PreMintNFTs(preMintCtx, minter, nftAddr, req.Erc721PreMint, preMintProgress); err != nil {
			cancel()
			lg.setError(fmt.Sprintf("failed to pre-mint NFTs: %v", err))
			return
		}
		cancel()
	}

	// Create pattern
	patternCfg := pattern.Config{
		Duration:            time.Duration(req.DurationSec) * time.Second,
		ConstantRate:        req.ConstantRate,
		RampStart:           req.RampStart,
		RampEnd:             req.RampEnd,
		BaselineRate:        req.BaselineRate,
		SpikeRate:           req.SpikeRate,
		SpikeDuration:       time.Duration(req.SpikeDuration) * time.Second,
		SpikeInterval:       time.Duration(req.SpikeInterval) * time.Second,
		AdaptiveInitialRate: req.AdaptiveInitialRate,
	}

	// Set stress rate from stress config if provided
	if req.RealisticConfig != nil && req.RealisticConfig.TargetTPS > 0 {
		patternCfg.RealisticRate = req.RealisticConfig.TargetTPS
	}

	pat, err := lg.patternReg.Get(req.Pattern, patternCfg)
	if err != nil {
		lg.setError(fmt.Sprintf("invalid pattern: %v", err))
		return
	}
	lg.currentPattern = pat

	// Set initial rate
	initialRate := float64(pat.GetRate(0))
	if initialRate <= 0 {
		initialRate = 100 // Fallback minimum
	}
	atomic.StoreInt64(&lg.currentRate, int64(initialRate))
	lg.rateLimiter = ratelimit.New(initialRate)
	atomic.StoreInt64(&lg.peakRate, 0)
	atomic.StoreInt64(&lg.pendingCount, 0)
	atomic.StoreInt32(&lg.stopping, 0)

	// Reset metrics
	lg.metricsCol.Reset()
	lg.preconfLatencies.Reset()
	atomic.StoreUint64(&lg.lastPreconfSeqNum, 0)
	atomic.StoreUint64(&lg.preconfGaps, 0)

	// Initialize realistic test metrics tracking
	if req.Pattern == types.PatternRealistic {
		lg.metricsCol.InitRealisticMetrics()
	}

	// Set timing
	lg.startTime = time.Now()
	lg.currentDuration = time.Duration(req.DurationSec) * time.Second
	lg.lastSentCount = 0
	lg.lastCheckTime = lg.startTime
	lg.currentTPS = 0

	// Determine target TPS for buffer allocation
	targetTPS := req.ConstantRate
	if targetTPS == 0 {
		targetTPS = req.RampEnd
	}
	if targetTPS == 0 {
		targetTPS = req.SpikeRate
	}
	if targetTPS == 0 && req.RealisticConfig != nil {
		targetTPS = req.RealisticConfig.TargetTPS
	}
	if targetTPS == 0 {
		targetTPS = 1000 // Default estimate
	}

	// Determine if TX logging should be enabled
	lg.txLoggingEnabled = lg.shouldLogTransactions(lg.currentDuration, targetTPS)
	if !lg.txLoggingEnabled {
		lg.logger.Warn("TX logging disabled due to estimated memory usage",
			"duration", lg.currentDuration,
			"targetTPS", targetTPS,
			"estimatedMemoryMB", (int(lg.currentDuration.Seconds())*targetTPS*txLogEntrySize)/(1024*1024))
	}

	// Initialize memory buffers
	lg.initBuffers(lg.currentDuration, targetTPS)

	// Create test run ID and persist to storage
	lg.currentTestID = fmt.Sprintf("test-%d", time.Now().UnixNano())
	if lg.storage != nil {
		defaultName := fmt.Sprintf("%s test - %s", req.Pattern, lg.startTime.Format("Jan 2 3:04 PM"))
		testRun := &storage.TestRun{
			ID:               lg.currentTestID,
			StartedAt:        lg.startTime,
			Pattern:          req.Pattern,
			TransactionType:  lg.currentTxType,
			DurationMs:       lg.currentDuration.Milliseconds(),
			Config:           &req,
			Status:           "running",
			TxLoggingEnabled: lg.txLoggingEnabled,
			ExecutionLayer:   lg.cfg.ExecutionLayer, // Track which execution layer was used
			PrivacyMode:      req.PrivacyMode,
			CustomName:       &defaultName,
		}
		if err := lg.storage.CreateTestRun(context.Background(), testRun); err != nil {
			lg.logger.Error("failed to create test run in storage", "error", err)
			// Continue anyway - storage is non-critical
		}
	}

	// Create context
	lg.ctx, lg.cancel = context.WithCancel(context.Background())

	// Connect to preconf WebSocket if the execution layer supports it and URL is configured.
	// The chain-poller fallback (for execution layers without a preconf channel) is
	// started later, after `testStartBlockNumber` has been recorded — otherwise the
	// poller would have no anchor and would scan from block 0.
	if lg.cfg.Capabilities.SupportsPreconfirmations && lg.cfg.PreconfWSURL != "" {
		go lg.connectPreconfWS()
	}

	// Connect to builder block metrics WebSocket if supported
	if lg.cfg.Capabilities.SupportsBlockMetricsWS && lg.cfg.PreconfWSURL != "" {
		go lg.connectBuilderMetricsWS()
	}

	// Connect to L2 WebSocket for block metrics (always try, will warn if fails)
	go lg.connectL2WS()

	// Clear block metrics from previous test
	lg.blockMetricsMu.Lock()
	lg.blockMetrics = nil
	lg.cumulativeGasUsed = 0
	lg.cumulativeGasLimit = 0
	lg.rollingGasWindow = nil // Clear rolling window for smooth MGas/s chart
	lg.rollingTxWindow = nil  // Clear rolling window for smooth TX/s chart
	lg.peakMgasPerSec = 0
	lg.lastMgasPerSec = 0 // Reset cached MGas/s value
	lg.peakTxPerSec = 0
	lg.lastTxPerSec = 0 // Reset cached TX/s value
	lg.lastBlockTime = time.Time{}
	lg.lastBlockNumber = 0
	lg.firstBlockNumber = 0
	lg.lastRecordedBlock = 0 // Reset deduplication tracking
	lg.totalBlockCount = 0
	lg.testStartBlockNumber = 0
	lg.testEndBlockNumber = 0
	lg.rpcLastBlockNumber = 0
	lg.blockMetricsMu.Unlock()

	// Clear incremental verification data from previous test
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

	// Query starting block number via RPC (fallback for when WebSocket fails)
	if lg.l2Client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if startBlock, err := lg.l2Client.GetBlockNumber(ctx); err == nil {
			lg.blockMetricsMu.Lock()
			lg.testStartBlockNumber = startBlock
			lg.blockMetricsMu.Unlock()
			lg.logger.Info("recorded test start block number", "block", startBlock)
		} else {
			lg.logger.Warn("failed to get start block number", "error", err)
		}
		cancel()
	}

	// Spawn the receipt-polling fallback for execution layers that don't
	// provide a preconfirmation stream. Done here (not earlier) so the poller
	// can anchor at `testStartBlockNumber` rather than scanning historical
	// blocks.
	if !lg.cfg.Capabilities.SupportsPreconfirmations {
		go lg.connectChainPoller()
	}

	// Swap sender to privacy-routed client if privacy mode requested
	if req.PrivacyMode && lg.cfg.PrivacyRPCURL != "" {
		// Always re-read token file (token may have been refreshed between tests)
		{
			privacyCfg := rpc.DefaultClientConfig(lg.cfg.PrivacyRPCURL)
			privacyCfg.Logger = lg.logger
			if lg.cfg.PrivacyAuthTokenFile != "" {
				tokenBytes, err := os.ReadFile(lg.cfg.PrivacyAuthTokenFile)
				if err != nil {
					lg.logger.Error("failed to read privacy auth token file", "path", lg.cfg.PrivacyAuthTokenFile, "error", err)
					lg.setError(fmt.Sprintf("privacy mode requires auth token: %v", err))
					return
				}
				privacyCfg.AuthToken = strings.TrimSpace(string(tokenBytes))
			}
			// Wrap in NoBatchClient — privacy proxy rejects JSON-RPC batch requests
			lg.privacyBuilderClient = rpc.NewNoBatchClient(rpc.NewHTTPClient(privacyCfg))
		}
		lg.sender = sender.New(sender.Config{
			Client:      lg.privacyBuilderClient,
			Concurrency: 2000,
			Logger:      lg.logger,
		})
		lg.logger.Info("using privacy proxy for this test", "url", lg.cfg.PrivacyRPCURL)
	} else {
		// Ensure we use the default sender (restore after a previous privacy test)
		lg.sender = lg.defaultSender
	}

	// Phase: Starting workers
	lg.initPhase = types.InitPhaseStartingWorkers
	lg.initProgress = "Starting test workers..."

	// Get all accounts (built-in + dynamic)
	allAccounts := lg.accountMgr.GetAccounts()
	dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
	allAccounts = append(allAccounts, dynamicAccounts...)

	// Start sender workers
	// More workers = better parallelism, but must not exceed semaphore capacity
	numWorkers := len(allAccounts)
	if numWorkers > 500 {
		numWorkers = 500 // Cap workers (should be <= sender concurrency / 4)
	}
	if numWorkers > numAccounts {
		numWorkers = numAccounts
	}

	for i := 0; i < numWorkers; i++ {
		lg.wg.Add(1)
		go lg.senderWorker(i, allAccounts)
	}

	// Start TPS calculator
	lg.wg.Add(1)
	go lg.tpsCalculator()

	// Start adaptive controller if needed
	if pat.NeedsAdaptiveController() {
		lg.wg.Add(1)
		go lg.adaptiveController()
	}

	// Start backpressure monitor to check block builder status
	lg.wg.Add(1)
	go lg.backpressureMonitor()

	// Start completion watcher (NOT in WaitGroup because it calls StopTest which waits on wg)
	go lg.completionWatcher()

	// Initialization complete - transition to running
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.initPhase = types.InitPhaseNone
	lg.initProgress = ""
	lg.statusMu.Unlock()

	// Fetch builder config to get txOrdering for incremental verification
	if env := lg.fetchBuilderConfig(); env != nil {
		lg.txOrdering = env.BuilderTxOrdering
		lg.logger.Info("fetched builder tx ordering for verification", "ordering", lg.txOrdering)
	}

	// Start incremental verification for long-running tests
	lg.startIncrementalVerification()

	lg.logger.Info("test started",
		"pattern", req.Pattern,
		"txType", lg.currentTxType,
		"duration", lg.currentDuration,
		"accounts", numAccounts,
	)
}