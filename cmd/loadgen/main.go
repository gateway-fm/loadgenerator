package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/gorilla/websocket"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/contract"
	"github.com/gateway-fm/loadgenerator/internal/execnode"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/pattern"
	"github.com/gateway-fm/loadgenerator/internal/ratelimit"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/sender"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/transport"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	"github.com/gateway-fm/loadgenerator/internal/verification"
	"github.com/gateway-fm/loadgenerator/internal/workload"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Memory thresholds for TX logging
const (
	txLogEntrySize = 150               // Estimated bytes per TxLogEntry
	maxTxLogMemory = 200 * 1024 * 1024 // 200MB threshold
)

// LoadGenerator orchestrates high-throughput transaction generation.
// It implements transport.LoadGeneratorAPI.
type LoadGenerator struct {
	cfg           *config.Config
	builderClient rpc.Client
	l2Client      rpc.Client
	accountMgr    *account.Manager
	patternReg    *pattern.Registry
	txBuilderReg  *txbuilder.Registry
	metricsCol    metrics.Collector
	deployer      *contract.Deployer
	storage       storage.Storage
	cacheStorage  storage.CacheStorage

	// Contract addresses
	erc20Contract       common.Address
	gasConsumerContract common.Address
	contractsDeployed   bool
	contractsMu         sync.RWMutex

	// Current test state
	status    types.TestStatus
	statusMu  sync.RWMutex
	testError string

	// Initialization progress tracking (for async startup)
	initPhase          types.InitPhase
	initProgress       string
	initAccountsTotal  int
	initAccountsGen    int
	initFundingTotal   int
	initFundingSent    int
	initContractsTotal int
	initContractsDone  int

	// Verification progress tracking (for post-test verification)
	verifyPhase      types.VerifyPhase
	verifyProgress   string
	blocksToVerify   int
	blocksVerified   int
	receiptsToSample int
	receiptsSampled  int

	// Warnings (non-fatal issues surfaced to user)
	warnings   []string
	warningsMu sync.RWMutex

	// Current test config
	currentPattern pattern.Pattern
	currentTxType  types.TransactionType
	testConfig     types.StartTestRequest

	// Current test tracking
	currentTestID    string
	txLoggingEnabled bool

	// In-memory buffers (written to DB AFTER test completes)
	timeSeriesBuf []storage.TimeSeriesPoint
	txLogBuf      []storage.TxLogEntry
	txLogBufMu    sync.Mutex
	pendingTxs    sync.Map // common.Hash -> *storage.TxLogEntry (uses [32]byte key, avoids hex string alloc)

	// Timing
	startTime       time.Time
	currentDuration time.Duration
	lastSentCount   uint64
	lastCheckTime   time.Time
	currentTPS      float64

	// Rate control
	currentRate    int64              // atomic
	peakRate       int64              // atomic
	pendingCount   int64              // atomic
	discardedCount uint64             // set at test end - transactions still pending that were discarded
	rateLimiter    *ratelimit.Limiter // Token bucket rate limiter for smooth traffic

	// EIP-1559 gas pricing (set at test start)
	gasTipCap *big.Int // Priority fee (tip)
	gasFeeCap *big.Int // Max fee per gas

	// Circuit breaker for failure detection
	recentSends       int64 // atomic - sends in current window
	recentFails       int64 // atomic - failures in current window
	recentRevocations int64 // atomic - preconf revocations in current window
	circuitOpen       int32 // atomic - 1 if circuit breaker is open
	preCircuitRate    int64 // atomic - rate before circuit opened (ceiling for AIMD recovery)

	// Backpressure monitoring
	builderPressure    float64 // last known block builder pressure (0.0-1.0)
	latestBaseFeeGwei  float64 // latest block's baseFeePerGas in gwei
	latestGasPriceGwei float64 // latest eth_gasPrice from L2 node in gwei
	latestGasUsed      uint64  // latest block's gasUsed
	// HSM / block attestation state from builder /status
	blockAttestationEnabled bool
	hsmProvider             string
	hsmKeyIDActive          string
	hsmFailoverEnabled      bool
	builderPressureMu       sync.RWMutex
	nonceResyncNeeded       int32 // atomic - 1 if nonces need resync after recovery

	// Preconfirmation WebSocket
	preconfWsConn   *websocket.Conn
	preconfWsConnMu sync.Mutex

	// Builder block metrics WebSocket (per-block timing, rejections, fill rate)
	builderMetricsWsConn   *websocket.Conn
	builderMetricsWsConnMu sync.Mutex

	// Preconf latency tracking
	preconfLatencies *metrics.StreamingLatencyStats

	// Preconf sequence tracking for gap detection
	lastPreconfSeqNum uint64 // atomic - last received sequence number
	preconfGaps       uint64 // atomic - count of detected gaps (missed events)

	// Control
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopping int32 // atomic

	// Async transaction sender with backpressure
	sender *sender.Sender

	// Test history (in-memory cache for backwards compatibility)
	testHistory   []types.TestResult
	testHistoryMu sync.RWMutex

	// Block metrics tracking (from L2 newHeads subscription)
	l2WsConn           *websocket.Conn
	l2WsConnMu         sync.Mutex
	blockMetrics       []blockMetricsPoint
	blockMetricsMu     sync.Mutex
	cumulativeGasUsed  uint64            // Cumulative gas used since test start
	cumulativeGasLimit uint64            // Cumulative gas limit since test start (for avg fill rate)
	rollingGasWindow   []rollingGasPoint // Rolling window for smooth MGas/s chart
	rollingTxWindow    []rollingTxPoint  // Rolling window for smooth TX/s chart (aligned with gas window)
	peakMgasPerSec     float64           // Peak rolling MGas/s observed during test
	lastMgasPerSec     float64           // Last calculated MGas/s (frozen when test ends)
	peakTxPerSec       float64           // Peak rolling TX/s observed during test
	lastTxPerSec       float64           // Last calculated TX/s (frozen when test ends)
	lastBlockTime      time.Time
	lastBlockNumber    uint64
	firstBlockNumber   uint64 // First block seen during test (from WebSocket)
	lastRecordedBlock  uint64 // Last block added to blockMetrics (for deduplication)
	totalBlockCount    int    // Total blocks produced during test

	// RPC-based block number tracking (fallback when WebSocket fails)
	testStartBlockNumber uint64 // Block number at test start (via RPC)
	testEndBlockNumber   uint64 // Block number at test end (via RPC)
	rpcLastBlockNumber   uint64 // Last block fetched via RPC (for time series fallback)

	// Incremental verification (for long-running tests)
	verifier               *verification.Verifier
	incrementalSnapshots   []storage.IncrementalVerificationSnapshot
	incrementalSnapshotsMu sync.Mutex
	recentBlockNumbers     []uint64 // Recent blocks for incremental verification
	recentBlockNumbersMu   sync.Mutex
	recentConfirmedHashes  []string // Recent confirmed TX hashes for incremental verification
	recentConfirmedMu      sync.Mutex
	incrementalStopCh      chan struct{} // Stop channel for incremental verification goroutine
	txOrdering             string        // Builder's TX ordering mode (fifo, tip_desc, tip_asc)
	includeDepositTx       bool          // Whether builder includes deposit TXs in blocks

	// Logger
	logger *slog.Logger
}


// NewLoadGenerator creates a new LoadGenerator with all dependencies wired.
func NewLoadGenerator(cfg *config.Config, store storage.Storage, logger *slog.Logger) (*LoadGenerator, error) {
	chainID := big.NewInt(cfg.ChainID)
	gasPrice := big.NewInt(cfg.GasPrice)

	// Create RPC clients
	builderCfg := rpc.DefaultClientConfig(cfg.BuilderRPCURL)
	builderCfg.Logger = logger
	builderClient := rpc.NewHTTPClient(builderCfg)

	l2Cfg := rpc.DefaultClientConfig(cfg.L2RPCURL)
	l2Cfg.Logger = logger
	l2Client := rpc.NewHTTPClient(l2Cfg)

	// Create account manager
	useLegacy := cfg.Capabilities != nil && cfg.Capabilities.RequiresLegacyTx
	accountMgr, err := account.NewManager(chainID, gasPrice, useLegacy, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create account manager: %w", err)
	}

	// Create registries
	patternReg := pattern.NewRegistry()
	// Use the first account as default recipient for ETH transfers
	accounts := accountMgr.GetAccounts()
	var recipient common.Address
	if len(accounts) > 0 {
		recipient = accounts[0].Address
	}
	txBuilderReg := txbuilder.NewDefaultRegistry(recipient)

	// Create metrics collector
	metricsCol := metrics.NewInMemoryCollector(true)

	// Create contract deployer
	deployer := contract.NewDeployer(builderClient, chainID, gasPrice, logger)
	deployer.SetUseLegacy(useLegacy)

	// Create async sender with backpressure
	// Concurrency must be high enough to saturate target TPS:
	// required = target_tps × avg_rpc_latency_sec (e.g., 30k × 0.02 = 600 minimum)
	snd := sender.New(sender.Config{
		Client:      builderClient,
		Concurrency: 2000, // Max concurrent in-flight sends
		Logger:      logger,
	})

	lg := &LoadGenerator{
		cfg:              cfg,
		builderClient:    builderClient,
		l2Client:         l2Client,
		accountMgr:       accountMgr,
		patternReg:       patternReg,
		txBuilderReg:     txBuilderReg,
		metricsCol:       metricsCol,
		deployer:         deployer,
		storage:          store,
		status:           types.StatusIdle,
		preconfLatencies: metrics.NewStreamingLatencyStats(),
		testHistory:      make([]types.TestResult, 0),
		sender:           snd,
		logger:           logger,
	}

	// Wire cache storage if the store supports it
	if cs, ok := store.(storage.CacheStorage); ok {
		lg.cacheStorage = cs
	}

	return lg, nil
}

// shouldLogTransactions determines if TX logging should be enabled based on estimated memory usage.
func (lg *LoadGenerator) shouldLogTransactions(duration time.Duration, targetTPS int) bool {
	estimatedTxs := int(duration.Seconds()) * targetTPS
	estimatedMemory := estimatedTxs * txLogEntrySize
	return estimatedMemory <= maxTxLogMemory
}

// initBuffers pre-allocates memory buffers before test starts.
func (lg *LoadGenerator) initBuffers(duration time.Duration, targetTPS int) {
	// Time-series: 5 samples/sec for duration (200ms interval)
	timeSeriesCapacity := int(duration.Seconds())*5 + 10
	lg.timeSeriesBuf = make([]storage.TimeSeriesPoint, 0, timeSeriesCapacity)

	// TX logs: only if enabled
	if lg.txLoggingEnabled {
		estimatedTxs := int(duration.Seconds()) * targetTPS
		lg.txLogBuf = make([]storage.TxLogEntry, 0, estimatedTxs+1000)
	} else {
		lg.txLogBuf = nil
	}

	// Clear pending TX map
	lg.pendingTxs = sync.Map{}
}

// StartTest starts a new load test with the given configuration.
// This method returns immediately and runs initialization in the background.
// Check GetMetrics() for initialization progress (status=initializing).
func (lg *LoadGenerator) StartTest(req types.StartTestRequest) error {
	lg.statusMu.Lock()
	if lg.status == types.StatusRunning || lg.status == types.StatusInitializing {
		lg.statusMu.Unlock()
		return fmt.Errorf("test already running or initializing")
	}
	lg.status = types.StatusInitializing
	lg.testError = ""
	lg.initPhase = types.InitPhaseNone
	lg.initProgress = "Starting initialization..."
	lg.initAccountsTotal = 0
	lg.initAccountsGen = 0
	lg.initFundingTotal = 0
	lg.initFundingSent = 0
	lg.initContractsTotal = 0
	lg.initContractsDone = 0
	lg.statusMu.Unlock()

	// Store config
	lg.testConfig = req

	// Validate realistic config if provided
	if req.Pattern == types.PatternRealistic && req.RealisticConfig != nil {
		if err := workload.ValidateTxTypeRatios(req.RealisticConfig.TxTypeRatios); err != nil {
			lg.setError(fmt.Sprintf("invalid realistic config: %v", err))
			return err
		}
	}

	// Run initialization in background
	go lg.runInitialization(req)

	return nil
}

// runInitialization performs the async initialization and starts the test.
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
			// Uniswap V3: 7 steps (WETH9, USDC, Factory, SwapRouter, NFTManager, Pool, Liquidity) + 2 base
			lg.initContractsTotal = 9
		} else {
			lg.initContractsTotal = 2 // ERC20, GasConsumer
		}
	}

	if err := lg.ensureContractsDeployed(txTypeForDeploy); err != nil {
		lg.setError(fmt.Sprintf("failed to deploy contracts: %v", err))
		return
	}
	// Note: initContractsDone is now updated incrementally via progress callbacks

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
			CustomName:       &defaultName,
		}
		if err := lg.storage.CreateTestRun(context.Background(), testRun); err != nil {
			lg.logger.Error("failed to create test run in storage", "error", err)
			// Continue anyway - storage is non-critical
		}
	}

	// Create context
	lg.ctx, lg.cancel = context.WithCancel(context.Background())

	// Connect to preconf WebSocket if the execution layer supports it and URL is configured
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

// StopTest stops the currently running test.
// Uses a timeout to prevent blocking forever if workers are stuck on HTTP calls.
// After stopping workers, waits for a grace period to collect late confirmation events,
// then counts and discards any remaining pending transactions.
func (lg *LoadGenerator) StopTest() {
	lg.statusMu.RLock()
	if lg.status != types.StatusRunning {
		lg.statusMu.RUnlock()
		return
	}
	lg.statusMu.RUnlock()

	// Signal stop FIRST - this lets workers know to exit
	atomic.StoreInt32(&lg.stopping, 1)

	// Stop incremental verification and run final snapshot
	lg.stopIncrementalVerification()

	if lg.cancel != nil {
		lg.cancel()
	}

	// Wait for workers with timeout - don't block forever
	// Workers check ctx.Done() and stopping flag, but may be stuck on HTTP calls
	const stopTimeout = 5 * time.Second
	done := make(chan struct{})
	go func() {
		lg.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		lg.logger.Info("all workers stopped gracefully")
	case <-time.After(stopTimeout):
		lg.logger.Warn("stop timeout - some workers may still be running",
			"timeout", stopTimeout)
	}

	// Record end block number NOW - at the moment we stop sending
	// This ensures on-chain verification only counts blocks up to this point,
	// excluding any pending txs that get confirmed later (even during grace period)
	if lg.l2Client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if endBlock, err := lg.l2Client.GetBlockNumber(ctx); err == nil {
			lg.blockMetricsMu.Lock()
			lg.testEndBlockNumber = endBlock
			lg.blockMetricsMu.Unlock()
			lg.logger.Info("recorded test end block number", "block", endBlock)
		}
		cancel()
	}

	// Grace period: wait for late confirmation events before closing WebSocket
	// This allows preconf events still in flight to be processed
	const confirmationGracePeriod = 3 * time.Second
	pendingBefore := lg.countPendingTxs()
	if pendingBefore > 0 {
		lg.logger.Info("waiting for late confirmations",
			"pendingTxs", pendingBefore,
			"gracePeriod", confirmationGracePeriod)
		time.Sleep(confirmationGracePeriod)
		pendingAfter := lg.countPendingTxs()
		lg.logger.Info("grace period complete",
			"confirmedDuringGrace", pendingBefore-pendingAfter,
			"stillPending", pendingAfter)
	}

	// Close preconf connection
	lg.preconfWsConnMu.Lock()
	if lg.preconfWsConn != nil {
		lg.preconfWsConn.Close()
		lg.preconfWsConn = nil
	}
	lg.preconfWsConnMu.Unlock()

	// Close builder metrics WebSocket connection
	lg.builderMetricsWsConnMu.Lock()
	if lg.builderMetricsWsConn != nil {
		lg.builderMetricsWsConn.Close()
		lg.builderMetricsWsConn = nil
	}
	lg.builderMetricsWsConnMu.Unlock()

	// Close L2 WebSocket connection
	lg.l2WsConnMu.Lock()
	if lg.l2WsConn != nil {
		lg.l2WsConn.Close()
		lg.l2WsConn = nil
	}
	lg.l2WsConnMu.Unlock()

	// Finalize pending transactions: mark remaining as discarded
	lg.discardedCount = lg.finalizePendingTxs()
	if lg.discardedCount > 0 {
		lg.logger.Info("discarded pending transactions at test end",
			"discardedCount", lg.discardedCount)
	}

	// Set status to verifying before on-chain verification
	lg.statusMu.Lock()
	lg.status = types.StatusVerifying
	lg.statusMu.Unlock()
	lg.logger.Info("test finished, starting verification")

	// Save result (includes on-chain verification)
	lg.saveTestResult()

	lg.statusMu.Lock()
	lg.status = types.StatusCompleted
	lg.statusMu.Unlock()

	lg.logger.Info("test stopped")
}

// countPendingTxs counts transactions still in pending state.
func (lg *LoadGenerator) countPendingTxs() int64 {
	var count int64
	lg.pendingTxs.Range(func(_, value interface{}) bool {
		if entry, ok := value.(*storage.TxLogEntry); ok && entry.Status == "pending" {
			count++
		}
		return true
	})
	return count
}

// finalizePendingTxs marks all remaining pending transactions as "discarded"
// and returns the count of discarded transactions.
func (lg *LoadGenerator) finalizePendingTxs() uint64 {
	var discardedCount uint64
	lg.pendingTxs.Range(func(key, value interface{}) bool {
		if entry, ok := value.(*storage.TxLogEntry); ok && entry.Status == "pending" {
			entry.Status = "discarded"
			discardedCount++
		}
		return true
	})
	return discardedCount
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
					lg.metricsCol.RecordTxFailed("send")
					metrics.AtomicSubSaturating(&lg.pendingCount, 1)
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

// setError sets the test error state.
func (lg *LoadGenerator) setError(msg string) {
	lg.statusMu.Lock()
	lg.status = types.StatusError
	lg.testError = msg
	lg.statusMu.Unlock()
	lg.logger.Error("test error", "error", msg)
}

func main() {
	// Parse flags - preserving exact same interface as original
	builderURL := flag.String("builder", getEnvOrDefault("BUILDER_RPC_URL", "http://localhost:13000"), "Block builder RPC URL")
	l2URL := flag.String("l2", getEnvOrDefault("L2_RPC_URL", "http://localhost:13000"), "L2 RPC URL")
	preconfWS := flag.String("preconf-ws", getEnvOrDefault("PRECONF_WS_URL", ""), "Preconfirmation WebSocket URL")
	chainID := flag.Int64("chainid", 42069, "Chain ID")
	gasPrice := flag.Int64("gasprice", 1000000000, "Gas price in wei (default 1 gwei)")
	gasLimit := flag.Uint64("gaslimit", 21000, "Gas limit per transaction")
	listenAddr := flag.String("listen", getEnvOrDefault("LISTEN_ADDR", ":3001"), "HTTP API listen address")
	databasePath := flag.String("database", getEnvOrDefault("DATABASE_PATH", "./data/loadgen.db"), "SQLite database path")

	// Block time for account scaling (must match block-builder BLOCK_TIME_MS)
	blockTimeMS := flag.Int("block-time-ms", getEnvIntOrDefault("BLOCK_TIME_MS", config.DefaultBlockTimeMS), "Block time in milliseconds (for account scaling)")

	// Execution layer selection
	executionLayer := flag.String("execution-layer", getEnvOrDefault("EXECUTION_LAYER", "reth"), "Execution layer (reth, op-reth, gravity-reth, cdk-erigon)")

	// CLI mode flags
	patternFlag := flag.String("pattern", "constant", "Load pattern (constant, ramp, spike, max)")
	targetTPS := flag.Int("tps", 0, "Target TPS for constant pattern (CLI mode - if set, runs single test)")
	duration := flag.Duration("duration", 30*time.Second, "Test duration (CLI mode)")
	numAccounts := flag.Int("accounts", 0, "Number of accounts (0=auto-calculate based on TPS)")

	// Logging
	logLevel := flag.String("log-level", getEnvOrDefault("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")

	flag.Parse()

	// Setup logger
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// Start pprof server on localhost only (not reachable from outside the container)
	go func() {
		logger.Info("pprof listening", "addr", "localhost:6061")
		if err := http.ListenAndServe("localhost:6061", nil); err != nil {
			logger.Error("pprof server failed", "error", err)
		}
	}()

	// Initialize storage
	store, err := storage.NewSQLiteStorage(*databasePath)
	if err != nil {
		logger.Error("failed to initialize storage", "error", err, "path", *databasePath)
		os.Exit(1)
	}
	defer store.Close()
	logger.Info("initialized storage", "path", *databasePath)

	// Build config
	cfg := &config.Config{
		BuilderRPCURL:  *builderURL,
		L2RPCURL:       *l2URL,
		PreconfWSURL:   *preconfWS,
		ChainID:        *chainID,
		GasPrice:       *gasPrice,
		GasTipCap:      *gasPrice, // Use gasPrice as tip cap for EIP-1559 compatibility
		GasLimit:       *gasLimit,
		ListenAddr:     *listenAddr,
		DatabasePath:   *databasePath,
		BlockTimeMS:    *blockTimeMS,
		ExecutionLayer: *executionLayer,
	}

	// Resolve execution layer capabilities
	cfg.Capabilities = execnode.DefaultRegistry().Get(cfg.ExecutionLayer)
	if cfg.Capabilities == nil {
		logger.Error("unknown execution layer", "layer", cfg.ExecutionLayer,
			"supported", []string{"reth", "op-reth", "gravity-reth", "cdk-erigon"})
		os.Exit(1)
	}
	logger.Info("resolved execution layer capabilities",
		"layer", cfg.ExecutionLayer,
		"hasBlockBuilder", cfg.Capabilities.HasExternalBlockBuilder,
		"supportsPreconf", cfg.Capabilities.SupportsPreconfirmations,
		"requiresLegacyTx", cfg.Capabilities.RequiresLegacyTx)

	// Create load generator
	lg, err := NewLoadGenerator(cfg, store, logger)
	if err != nil {
		logger.Error("failed to create load generator", "error", err)
		os.Exit(1)
	}

	// Handle interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// If TPS flag is set, run in CLI mode (single test)
	if *targetTPS > 0 {
		go func() {
			<-sigChan
			lg.StopTest()
		}()

		err := lg.StartTest(types.StartTestRequest{
			Pattern:      types.LoadPattern(*patternFlag),
			DurationSec:  int(duration.Seconds()),
			NumAccounts:  *numAccounts,
			ConstantRate: *targetTPS,
		})
		if err != nil {
			logger.Error("failed to start test", "error", err)
			os.Exit(1)
		}

		// Wait for completion
		for {
			metrics := lg.GetMetrics()
			if metrics.Status == types.StatusCompleted || metrics.Status == types.StatusError {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Print final metrics
		finalMetrics := lg.GetMetrics()
		logger.Info("test completed",
			"txSent", finalMetrics.TxSent,
			"txConfirmed", finalMetrics.TxConfirmed,
			"txFailed", finalMetrics.TxFailed,
			"avgTPS", finalMetrics.AverageTPS,
		)
		return
	}

	// Server mode - start HTTP API
	go func() {
		<-sigChan
		logger.Info("shutting down...")
		lg.StopTest()
		os.Exit(0)
	}()

	// Create HTTP server
	server := transport.NewServer(lg, lg, logger, cfg.CORSAllowedOrigins)
	mux := server.Handler()

	logger.Info("starting HTTP server", "addr", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
