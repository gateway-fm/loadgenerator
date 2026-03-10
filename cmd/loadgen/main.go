package main

import (
	"context"
	stdjson "encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
	"github.com/gateway-fm/loadgenerator/internal/uniswapv3"
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

// recordTxSent records a sent transaction (non-blocking).
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

// resyncAllNonces resyncs nonces for all accounts from the chain.
// Called when circuit breaker opens due to high revocation rate.
// Uses builderClient to query pending nonces from the block builder (via eth_getPendingNonce).
// This ensures load generator stays in sync with the builder's view of nonces.
// Falls back to l2Client (chain "latest") if builder is unavailable.
func (lg *LoadGenerator) resyncAllNonces() {
	lg.logger.Info("resyncing all account nonces from builder...")

	accounts := lg.accountMgr.GetAccounts()
	resyncedCount := 0
	failedCount := 0
	fallbackCount := 0

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use builderClient to get pending nonces (via eth_getPendingNonce) - ensures sync with builder
	// Falls back to l2Client (chain confirmed) if builder is unavailable
	for _, acc := range accounts {
		// First try builder's view (preferred - stays in sync)
		if err := acc.Resync(ctx, lg.builderClient); err != nil {
			// Builder failed - fall back to chain confirmed nonce
			lg.logger.Debug("builder nonce failed, falling back to chain",
				"address", acc.Address.Hex()[:10],
				"error", err)
			if err := acc.ResyncFromChain(ctx, lg.l2Client); err != nil {
				lg.logger.Warn("failed to resync nonce for account",
					"address", acc.Address.Hex()[:10],
					"error", err)
				failedCount++
			} else {
				fallbackCount++
			}
		} else {
			resyncedCount++
		}
	}

	atomic.StoreInt32(&lg.nonceResyncNeeded, 0)
	lg.logger.Info("nonce resync complete",
		"resynced", resyncedCount,
		"fallback", fallbackCount,
		"failed", failedCount)
}

// resetBuilderNonces calls the block builder's /reset-nonces endpoint to clear
// its nonce cache and pending pool. This is critical before funding new accounts
// to prevent stale cached nonces from causing "nonce ahead" rejections.
// Skipped when no external block builder is used (e.g., cdk-erigon mode).
func (lg *LoadGenerator) resetBuilderNonces() error {
	if !lg.cfg.Capabilities.HasExternalBlockBuilder {
		lg.logger.Info("skipping builder nonce reset (no external block builder)")
		return nil
	}
	lg.logger.Info("resetting block builder nonce cache...")

	// Extract base URL from builder RPC URL (remove any path)
	baseURL := lg.cfg.BuilderRPCURL
	resetURL := baseURL + "/reset-nonces"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", resetURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to POST /reset-nonces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reset-nonces returned status %d", resp.StatusCode)
	}

	lg.logger.Info("block builder nonce cache reset successfully")
	return nil
}

// backpressureMonitor periodically fetches block builder status and updates pressure.
func (lg *LoadGenerator) backpressureMonitor() {
	defer lg.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-lg.ctx.Done():
			return
		case <-ticker.C:
			lg.fetchBuilderPressure()
		}
	}
}

// fetchBuilderPressure fetches block builder status via HTTP GET and updates pressure and gas metrics.
func (lg *LoadGenerator) fetchBuilderPressure() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Make HTTP GET request to /status endpoint (not JSON-RPC)
	statusURL := lg.cfg.BuilderRPCURL + "/status"
	req, err := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		lg.builderPressureMu.Lock()
		lg.builderPressure = 0
		lg.builderPressureMu.Unlock()
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		lg.builderPressureMu.Lock()
		lg.builderPressure = 0
		lg.builderPressureMu.Unlock()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		lg.builderPressureMu.Lock()
		lg.builderPressure = 0
		lg.builderPressureMu.Unlock()
		return
	}

	// Parse response to get pending count, max txs, and gas metrics
	var status struct {
		PendingTxCount    int64   `json:"pendingTxCount"`
		MaxTxsPerBlock    int64   `json:"maxTxsPerBlock"`
		PendingPoolSize   int64   `json:"pendingPoolSize"`
		LatestBaseFeeGwei float64 `json:"latestBaseFeeGwei"`
		LatestGasUsed     uint64  `json:"latestGasUsed"`
		// HSM / block attestation metadata (optional)
		BlockAttestationEnabled bool   `json:"blockAttestationEnabled"`
		HSMProvider             string `json:"hsmProvider"`
		HSMKeyIDActive          string `json:"hsmKeyIdActive"`
		HSMFailoverEnabled      bool   `json:"hsmFailoverEnabled"`
	}

	if err := stdjson.NewDecoder(resp.Body).Decode(&status); err != nil {
		return
	}

	// Calculate pressure as ratio of pending to capacity (2 * maxTxsPerBlock)
	capacity := status.MaxTxsPerBlock * 2
	if capacity <= 0 {
		capacity = 50000 // default
	}

	pressure := float64(status.PendingTxCount) / float64(capacity)
	if pressure > 1.0 {
		pressure = 1.0
	}

	// Also fetch gas metrics from L2 node (non-blocking, best-effort)
	// This is more reliable than block builder status for baseFee
	var gasPriceGwei, baseFeeGwei float64
	if lg.l2Client != nil {
		// Fetch eth_gasPrice (includes baseFee + suggested tip)
		if gasPrice, err := lg.l2Client.GetGasPrice(ctx); err == nil {
			gasPriceGwei = float64(gasPrice) / 1e9 // Convert wei to gwei
		}
		// Fetch baseFee directly from latest block
		if baseFee, err := lg.l2Client.GetBaseFee(ctx); err == nil {
			baseFeeGwei = float64(baseFee) / 1e9 // Convert wei to gwei
		}
	}

	// Use L2 node baseFee if available, otherwise fall back to block builder status
	if baseFeeGwei == 0 {
		baseFeeGwei = status.LatestBaseFeeGwei
	}

	lg.builderPressureMu.Lock()
	lg.builderPressure = pressure
	lg.latestBaseFeeGwei = baseFeeGwei
	lg.latestGasPriceGwei = gasPriceGwei
	lg.latestGasUsed = status.LatestGasUsed
	lg.blockAttestationEnabled = status.BlockAttestationEnabled
	lg.hsmProvider = status.HSMProvider
	lg.hsmKeyIDActive = status.HSMKeyIDActive
	lg.hsmFailoverEnabled = status.HSMFailoverEnabled
	lg.builderPressureMu.Unlock()
}

// fetchBuilderConfig fetches the complete builder configuration for environment snapshot.
func (lg *LoadGenerator) fetchBuilderConfig() *storage.EnvironmentSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Initialize environment snapshot with load-gen config
	env := &storage.EnvironmentSnapshot{
		LoadGenGasTipCapGwei:  float64(lg.cfg.GasTipCap) / 1e9, // wei to gwei
		LoadGenGasFeeCapGwei:  float64(lg.cfg.GasFeeCap) / 1e9, // wei to gwei
		LoadGenExecutionLayer: lg.cfg.ExecutionLayer,
		// Node identification - set defaults based on execution layer
		NodeName:        lg.cfg.Capabilities.Name,
		UseBlockBuilder: lg.cfg.Capabilities.HasExternalBlockBuilder,
	}

	// Fetch node info from L2 RPC (works for all execution layers)
	env.NodeVersion, env.ChainID = lg.fetchNodeInfo(ctx)

	// Derive NodeName from version string if more specific info available
	if env.NodeVersion != "" {
		env.NodeName = lg.parseNodeName(env.NodeVersion, lg.cfg.ExecutionLayer)
	}

	// Try to fetch builder status (only available when using external block-builder)
	if !lg.cfg.Capabilities.SupportsBuilderStatusAPI {
		lg.logger.Debug("skipping builder status fetch (not supported by execution layer)")
		return env
	}

	statusURL := lg.cfg.BuilderRPCURL + "/status"
	req, err := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		lg.logger.Debug("failed to create builder status request", "error", err)
		return env
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		lg.logger.Debug("failed to fetch builder config", "error", err)
		return env
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		lg.logger.Debug("builder status returned non-OK", "status", resp.StatusCode)
		return env
	}

	// Parse full response for environment snapshot
	var builderStatus struct {
		BlockTimeMs            int    `json:"blockTimeMs"`
		GasLimit               uint64 `json:"gasLimit"`
		MaxTxsPerBlock         int    `json:"maxTxsPerBlock"`
		TxOrdering             string `json:"txOrdering"`
		EnablePreconfirmations bool   `json:"enablePreconfirmations"`
		SkipEmptyBlocks        bool   `json:"skipEmptyBlocks"`
		IncludeDepositTx       bool   `json:"includeDepositTx"`
		// HSM / block attestation metadata (optional)
		BlockAttestationEnabled bool   `json:"blockAttestationEnabled"`
		HSMProvider             string `json:"hsmProvider"`
		HSMKeyIDActive          string `json:"hsmKeyIdActive"`
		HSMFailoverEnabled      bool   `json:"hsmFailoverEnabled"`
	}

	if err := stdjson.NewDecoder(resp.Body).Decode(&builderStatus); err != nil {
		lg.logger.Debug("failed to decode builder config", "error", err)
		return env
	}

	// Store builder config for incremental verification
	lg.txOrdering = builderStatus.TxOrdering
	lg.includeDepositTx = builderStatus.IncludeDepositTx

	// Update environment with builder config
	env.BuilderBlockTimeMs = builderStatus.BlockTimeMs
	env.BuilderGasLimit = builderStatus.GasLimit
	env.BuilderMaxTxsPerBlock = builderStatus.MaxTxsPerBlock
	env.BuilderTxOrdering = builderStatus.TxOrdering
	env.BuilderEnablePreconfs = builderStatus.EnablePreconfirmations
	env.BuilderSkipEmptyBlocks = builderStatus.SkipEmptyBlocks
	env.BuilderIncludeDepositTx = builderStatus.IncludeDepositTx
	env.BuilderBlockAttestationEnabled = builderStatus.BlockAttestationEnabled
	env.BuilderHSMProvider = builderStatus.HSMProvider
	env.BuilderHSMKeyIDActive = builderStatus.HSMKeyIDActive
	env.BuilderHSMFailoverEnabled = builderStatus.HSMFailoverEnabled

	return env
}

// fetchHeaderAttestations retrieves signed header attestations for the test block range.
func (lg *LoadGenerator) fetchHeaderAttestations(ctx context.Context, firstBlock, lastBlock uint64) []storage.HeaderAttestation {
	if firstBlock == 0 || lastBlock == 0 || lastBlock < firstBlock {
		return nil
	}
	if !lg.cfg.Capabilities.SupportsBuilderStatusAPI {
		return nil
	}

	result := make([]storage.HeaderAttestation, 0, int(lastBlock-firstBlock+1))
	client := &http.Client{Timeout: 1200 * time.Millisecond}

	for blockNum := firstBlock; blockNum <= lastBlock; blockNum++ {
		select {
		case <-ctx.Done():
			return result
		default:
		}

		reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		url := lg.cfg.BuilderRPCURL + "/block-attestations/" + strconv.FormatUint(blockNum, 10)
		req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		if err != nil {
			cancel()
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			cancel()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			cancel()
			continue
		}

		var payload builderHeaderAttestationResponse
		if err := stdjson.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			cancel()
			continue
		}
		resp.Body.Close()
		cancel()

		result = append(result, storage.HeaderAttestation{
			SchemaVersion: payload.SchemaVersion,
			Status:        payload.Status,
			BlockNumber:   payload.Commitment.BlockNumber,
			BlockHash:     payload.Commitment.BlockHash,
			ParentHash:    payload.Commitment.ParentHash,
			StateRoot:     payload.Commitment.StateRoot,
			ReceiptsRoot:  payload.Commitment.ReceiptsRoot,
			Timestamp:     payload.Commitment.Timestamp,
			GasUsed:       payload.Commitment.GasUsed,
			BaseFeeWei:    fmt.Sprint(payload.Commitment.BaseFeePerGasWei),
			Sequencer:     payload.Commitment.SequencerAddress,
			DigestHex:     payload.DigestHex,
			SignatureHex:  payload.SignatureHex,
			RHex:          payload.RHex,
			SHex:          payload.SHex,
			V:             payload.V,
			KeyID:         payload.KeyID,
			Provider:      payload.Provider,
			Failover:      payload.Failover,
			Error:         payload.Error,
			SignedAt:      payload.SignedAt,
		})
	}

	return result
}

// fetchNodeInfo fetches node version and chain ID from L2 RPC.
func (lg *LoadGenerator) fetchNodeInfo(ctx context.Context) (version string, chainID uint64) {
	if lg.l2Client == nil {
		return "", 0
	}

	// Fetch web3_clientVersion
	versionResp, err := lg.l2Client.Call(ctx, "web3_clientVersion", nil)
	if err == nil && versionResp != nil {
		var v string
		if err := stdjson.Unmarshal(versionResp, &v); err == nil {
			version = v
		}
	} else {
		lg.logger.Debug("failed to fetch web3_clientVersion", "error", err)
	}

	// Fetch eth_chainId
	chainIDResp, err := lg.l2Client.Call(ctx, "eth_chainId", nil)
	if err == nil && chainIDResp != nil {
		var hexChainID string
		if err := stdjson.Unmarshal(chainIDResp, &hexChainID); err == nil {
			// Parse hex string (e.g., "0xa455") to uint64
			if len(hexChainID) > 2 && hexChainID[:2] == "0x" {
				if parsed, err := parseHexUint64(hexChainID); err == nil {
					chainID = parsed
				}
			}
		}
	} else {
		lg.logger.Debug("failed to fetch eth_chainId", "error", err)
	}

	return version, chainID
}

// parseNodeName derives a user-friendly node name from the version string.
func (lg *LoadGenerator) parseNodeName(version, executionLayer string) string {
	// Common version string formats:
	// op-reth: "reth/v1.9.3-op/..."
	// gravity-reth: "reth/v0.4.1-gravity/..." or "gravity-reth/v0.4.1/..."
	// cdk-erigon: "erigon/..."
	// standard reth: "reth/v1.x.x/..."

	lowerVersion := strings.ToLower(version)

	// Check for gravity-reth signatures
	if strings.Contains(lowerVersion, "gravity") {
		return "gravity-reth"
	}

	// Check for op-reth signatures
	if strings.Contains(lowerVersion, "-op") || strings.Contains(lowerVersion, "optimism") {
		return "op-reth"
	}

	// Check for cdk-erigon
	if strings.Contains(lowerVersion, "erigon") || strings.Contains(lowerVersion, "cdk") {
		return "cdk-erigon"
	}

	// Check for standard reth
	if strings.Contains(lowerVersion, "reth") {
		// Could be gravity-reth or op-reth without clear markers - use execution layer hint
		if executionLayer == "gravity-reth" {
			return "gravity-reth"
		}
		return "op-reth" // Default to op-reth for reth-based nodes
	}

	// Fall back to execution layer config
	return executionLayer
}



// ensureContractsDeployed deploys required contracts if not already deployed.
func (lg *LoadGenerator) ensureContractsDeployed(txType types.TransactionType) error {
	lg.contractsMu.Lock()
	defer lg.contractsMu.Unlock()

	// Only deploy if needed
	if txType == types.TxTypeEthTransfer {
		return nil
	}

	// Check if Uniswap contracts are needed but not yet deployed
	uniswapNeeded := false
	var uniswapBuilder *txbuilder.UniswapV3SwapBuilder
	if cb, ok := lg.txBuilderReg.GetComplexBuilder(txType); ok && cb.IsComplex() {
		if ub, ok := cb.(*txbuilder.UniswapV3SwapBuilder); ok {
			uniswapBuilder = ub
			uniswapNeeded = !ub.IsDeployed()
		}
	}

	// If base contracts deployed AND uniswap not needed (or already deployed), we're done
	if lg.contractsDeployed && !uniswapNeeded {
		return nil
	}

	lg.logger.Info("deploying contracts", slog.Bool("uniswapNeeded", uniswapNeeded), slog.Bool("baseDeployed", lg.contractsDeployed))

	// Get deployer account
	accounts := lg.accountMgr.GetAccounts()
	if len(accounts) == 0 {
		return fmt.Errorf("no accounts available for deployment")
	}
	deployerAcc := accounts[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cacheChainID := lg.cfg.ChainID

	// Try to restore contracts from cache
	if lg.cacheStorage != nil && !lg.contractsDeployed {
		if lg.tryRestoreCachedContracts(ctx, cacheChainID, uniswapNeeded, uniswapBuilder) {
			return nil
		}
	}

	// Deploy Uniswap V3 contracts if needed (and not already deployed)
	if uniswapNeeded && uniswapBuilder != nil {
		lg.logger.Info("deploying Uniswap V3 contracts (complex builder)")

		chainID := big.NewInt(lg.cfg.ChainID)
		gasPrice := big.NewInt(lg.cfg.GasPrice)

		uniswapProgress := func(name string, done, total int) {
			lg.statusMu.Lock()
			lg.initContractsDone = done
			lg.initProgress = fmt.Sprintf("Deploying %s (%d/%d)...", name, done, total)
			lg.statusMu.Unlock()
		}

		if err := uniswapBuilder.DeployContractsWithProgress(ctx, lg.builderClient, deployerAcc, chainID, gasPrice, lg.logger, uniswapProgress); err != nil {
			return fmt.Errorf("failed to deploy Uniswap V3 contracts: %w", err)
		}

		// Set up all accounts for Uniswap swapping
		dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
		totalAccounts := len(accounts) + len(dynamicAccounts)
		lg.logger.Info("setting up accounts for Uniswap swaps...", slog.Int("builtIn", len(accounts)), slog.Int("dynamic", len(dynamicAccounts)))
		allAccounts := make([]any, 0, totalAccounts)
		for _, acc := range accounts {
			allAccounts = append(allAccounts, acc)
		}
		for _, acc := range dynamicAccounts {
			allAccounts = append(allAccounts, acc)
		}
		if err := uniswapBuilder.SetupAccounts(ctx, allAccounts, lg.builderClient, chainID, gasPrice); err != nil {
			return fmt.Errorf("failed to setup accounts for Uniswap: %w", err)
		}

		lg.logger.Info("Uniswap V3 contracts deployed and accounts setup")
		lg.logger.Info("waiting for Uniswap transactions to settle...")
		time.Sleep(5 * time.Second)

		// Cache Uniswap contract addresses
		lg.saveUniswapContractsToCache(ctx, cacheChainID, uniswapBuilder)

		// Mark all dynamic accounts as uniswap-ready in cache
		if lg.cacheStorage != nil {
			dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
			addresses := make([]string, len(dynamicAccounts))
			for i, acc := range dynamicAccounts {
				addresses[i] = acc.Address.Hex()
			}
			if err := lg.cacheStorage.MarkAccountsUniswapReady(ctx, cacheChainID, addresses); err != nil {
				lg.logger.Warn("failed to mark accounts as uniswap-ready", "error", err)
			}
		}
	}

	// Deploy base contracts (ERC20, GasConsumer, etc) if not already deployed
	if !lg.contractsDeployed {
		lg.logger.Info("Deploying base contracts...")

		baseContractOffset := 0
		if uniswapNeeded {
			baseContractOffset = 7
		}
		baseProgress := func(name string, done, total int) {
			lg.statusMu.Lock()
			lg.initContractsDone = baseContractOffset + done
			lg.initProgress = fmt.Sprintf("Deploying %s (%d/%d)...", name, baseContractOffset+done, lg.initContractsTotal)
			lg.statusMu.Unlock()
		}

		results, err := lg.deployer.DeployAllWithProgress(ctx, deployerAcc, baseProgress)
		if err != nil {
			return fmt.Errorf("failed to deploy contracts: %w", err)
		}

		lg.erc20Contract = results["ERC20"]
		lg.gasConsumerContract = results["GasConsumer"]
		lg.contractsDeployed = true

		if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Transfer); err == nil {
			builder.SetContractAddress(lg.erc20Contract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Approve); err == nil {
			builder.SetContractAddress(lg.erc20Contract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeStorageWrite); err == nil {
			builder.SetContractAddress(lg.gasConsumerContract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeHeavyCompute); err == nil {
			builder.SetContractAddress(lg.gasConsumerContract)
		}

		lg.logger.Info("contracts deployed",
			"erc20", lg.erc20Contract.Hex(),
			"gasConsumer", lg.gasConsumerContract.Hex(),
		)

		// Cache base contract addresses
		lg.saveBaseContractsToCache(ctx, cacheChainID)
	}

	return nil
}

// tryRestoreCachedContracts attempts to restore contracts from cache.
// Returns true if all needed contracts were restored successfully.
func (lg *LoadGenerator) tryRestoreCachedContracts(ctx context.Context, chainID int64, uniswapNeeded bool, uniswapBuilder *txbuilder.UniswapV3SwapBuilder) bool {
	cached, err := lg.cacheStorage.LoadCachedContracts(ctx, chainID)
	if err != nil {
		lg.logger.Warn("failed to load cached contracts", "error", err)
		return false
	}
	if len(cached) == 0 {
		return false
	}

	// Build name→address map for validation
	cachedMap := make(map[string]string, len(cached))
	for _, c := range cached {
		cachedMap[c.Name] = c.Address
	}

	// Validate all cached contracts still exist on-chain
	valid, invalid := lg.deployer.ValidateCachedContracts(ctx, cachedMap)
	if len(invalid) > 0 {
		lg.logger.Info("some cached contracts are invalid, deploying fresh",
			slog.Int("valid", len(valid)),
			slog.Int("invalid", len(invalid)),
		)
		lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
		return false
	}

	// Restore base contracts
	erc20Addr, hasERC20 := valid["ERC20"]
	gasConsumerAddr, hasGasConsumer := valid["GasConsumer"]
	if !hasERC20 || !hasGasConsumer {
		lg.logger.Info("base contracts not in cache, deploying fresh")
		lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
		return false
	}

	lg.erc20Contract = erc20Addr
	lg.gasConsumerContract = gasConsumerAddr
	lg.contractsDeployed = true

	if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Transfer); err == nil {
		builder.SetContractAddress(lg.erc20Contract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Approve); err == nil {
		builder.SetContractAddress(lg.erc20Contract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeStorageWrite); err == nil {
		builder.SetContractAddress(lg.gasConsumerContract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeHeavyCompute); err == nil {
		builder.SetContractAddress(lg.gasConsumerContract)
	}

	lg.logger.Info("restored cached base contracts",
		"erc20", lg.erc20Contract.Hex(),
		"gasConsumer", lg.gasConsumerContract.Hex(),
	)

	// Restore Uniswap contracts if needed
	if uniswapNeeded && uniswapBuilder != nil {
		weth9, hasWETH9 := valid["uniswap:WETH9"]
		usdc, hasUSDC := valid["uniswap:USDC"]
		factory, hasFactory := valid["uniswap:Factory"]
		swapRouter, hasRouter := valid["uniswap:SwapRouter"]
		nftManager, hasNFT := valid["uniswap:NonfungiblePositionManager"]
		pool, hasPool := valid["uniswap:Pool"]

		if !hasWETH9 || !hasUSDC || !hasFactory || !hasRouter || !hasNFT || !hasPool {
			lg.logger.Info("Uniswap contracts not fully cached, deploying fresh")
			lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
			// Reset base contract state so they get re-cached with uniswap
			lg.contractsDeployed = false
			return false
		}

		contracts := &uniswapv3.DeployedContracts{
			WETH9:                      weth9,
			USDC:                       usdc,
			Factory:                    factory,
			SwapRouter:                 swapRouter,
			NonfungiblePositionManager: nftManager,
			Pool:                       pool,
		}
		uniswapBuilder.RestoreContracts(contracts)

		lg.logger.Info("restored cached Uniswap V3 contracts",
			"pool", pool.Hex(),
			"swapRouter", swapRouter.Hex(),
		)

		// Setup accounts that aren't yet uniswap-ready
		lg.setupUniswapAccountsFromCache(ctx, chainID, uniswapBuilder)
	}

	return true
}

// setupUniswapAccountsFromCache sets up only accounts that aren't marked as uniswap-ready.
func (lg *LoadGenerator) setupUniswapAccountsFromCache(ctx context.Context, chainID int64, uniswapBuilder *txbuilder.UniswapV3SwapBuilder) {
	// Load cached accounts to check uniswap_ready flag
	cached, err := lg.cacheStorage.LoadCachedAccounts(ctx, chainID)
	if err != nil {
		lg.logger.Warn("failed to load cached accounts for uniswap check", "error", err)
		return
	}

	readySet := make(map[string]bool, len(cached))
	for _, ca := range cached {
		if ca.UniswapReady {
			readySet[ca.Address] = true
		}
	}

	// Collect accounts that need Uniswap setup
	allBuiltIn := lg.accountMgr.GetAccounts()
	dynamicAccounts := lg.accountMgr.GetDynamicAccounts()

	var needSetup []any
	for _, acc := range allBuiltIn {
		if !readySet[acc.Address.Hex()] {
			needSetup = append(needSetup, acc)
		}
	}
	for _, acc := range dynamicAccounts {
		if !readySet[acc.Address.Hex()] {
			needSetup = append(needSetup, acc)
		}
	}

	if len(needSetup) == 0 {
		lg.logger.Info("all accounts already uniswap-ready from cache")
		return
	}

	lg.logger.Info("setting up non-ready accounts for Uniswap",
		slog.Int("needSetup", len(needSetup)),
		slog.Int("alreadyReady", len(readySet)),
	)

	bigChainID := big.NewInt(lg.cfg.ChainID)
	gasPrice := big.NewInt(lg.cfg.GasPrice)
	if err := uniswapBuilder.SetupAccounts(ctx, needSetup, lg.builderClient, bigChainID, gasPrice); err != nil {
		lg.logger.Warn("failed to setup accounts for Uniswap", "error", err)
		return
	}

	// Mark newly-setup accounts as uniswap-ready in cache
	newAddresses := make([]string, 0, len(needSetup))
	for _, a := range needSetup {
		if acc, ok := a.(*account.Account); ok {
			newAddresses = append(newAddresses, acc.Address.Hex())
		}
	}
	if err := lg.cacheStorage.MarkAccountsUniswapReady(ctx, chainID, newAddresses); err != nil {
		lg.logger.Warn("failed to mark accounts as uniswap-ready", "error", err)
	}
}

// saveBaseContractsToCache persists base contract addresses to cache.
func (lg *LoadGenerator) saveBaseContractsToCache(ctx context.Context, chainID int64) {
	if lg.cacheStorage == nil {
		return
	}

	now := time.Now()
	contracts := []storage.CachedContract{
		{Name: "ERC20", Address: lg.erc20Contract.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "GasConsumer", Address: lg.gasConsumerContract.Hex(), ChainID: chainID, CreatedAt: now},
	}
	for _, c := range contracts {
		if err := lg.cacheStorage.SaveCachedContract(ctx, c); err != nil {
			lg.logger.Warn("failed to cache contract", "name", c.Name, "error", err)
		}
	}
	lg.logger.Info("cached base contract addresses")
}

// saveUniswapContractsToCache persists Uniswap contract addresses to cache.
func (lg *LoadGenerator) saveUniswapContractsToCache(ctx context.Context, chainID int64, ub *txbuilder.UniswapV3SwapBuilder) {
	if lg.cacheStorage == nil {
		return
	}

	contracts := ub.GetContracts()
	if contracts == nil {
		return
	}

	now := time.Now()
	entries := []storage.CachedContract{
		{Name: "uniswap:WETH9", Address: contracts.WETH9.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:USDC", Address: contracts.USDC.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:Factory", Address: contracts.Factory.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:SwapRouter", Address: contracts.SwapRouter.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:NonfungiblePositionManager", Address: contracts.NonfungiblePositionManager.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:Pool", Address: contracts.Pool.Hex(), ChainID: chainID, CreatedAt: now},
	}
	for _, c := range entries {
		if err := lg.cacheStorage.SaveCachedContract(ctx, c); err != nil {
			lg.logger.Warn("failed to cache uniswap contract", "name", c.Name, "error", err)
		}
	}
	lg.logger.Info("cached Uniswap V3 contract addresses")
}

// tryWarmStartAccounts attempts to load cached accounts for warm start.
// Returns true if warm start succeeded (accounts are ready to use).
func (lg *LoadGenerator) tryWarmStartAccounts(dynamicCount int, chainID int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cached, err := lg.cacheStorage.LoadCachedAccounts(ctx, chainID)
	if err != nil {
		lg.logger.Warn("failed to load cached accounts", "error", err)
		return false
	}
	if len(cached) == 0 {
		lg.logger.Info("no cached accounts found, cold start required")
		return false
	}

	lg.logger.Info("loaded cached accounts, validating balances...",
		slog.Int("cached", len(cached)),
		slog.Int("needed", dynamicCount),
	)

	// Reconstruct Account objects from cached hex keys
	cachedAccounts := make([]*account.Account, 0, len(cached))
	for _, ca := range cached {
		acc, err := account.NewAccountFromHex(ca.PrivateKeyHex)
		if err != nil {
			lg.logger.Warn("invalid cached key, discarding cache", "error", err)
			lg.cacheStorage.DeleteCachedAccounts(ctx, chainID)
			return false
		}
		cachedAccounts = append(cachedAccounts, acc)
	}

	// Validate balances (1 ETH minimum)
	minBalance, _ := new(big.Int).SetString("1000000000000000000", 10) // 1 ETH
	lg.initPhase = types.InitPhaseGeneratingAccts
	lg.initProgress = "Validating cached account balances..."

	funded, unfunded := lg.accountMgr.ValidateBalances(ctx, lg.l2Client, cachedAccounts, minBalance)

	// If ALL accounts have zero balance, assume re-genesis
	if len(funded) == 0 {
		lg.logger.Info("all cached accounts have zero balance (re-genesis detected), wiping cache")
		lg.cacheStorage.DeleteCachedAccounts(ctx, chainID)
		lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
		return false
	}

	lg.logger.Info("cached account balance validation complete",
		slog.Int("funded", len(funded)),
		slog.Int("unfunded", len(unfunded)),
		slog.Int("needed", dynamicCount),
	)

	// Determine which accounts to use
	var allAccounts []*account.Account
	needNew := 0

	if len(funded) >= dynamicCount {
		// Have enough funded accounts — use first N
		allAccounts = funded[:dynamicCount]
	} else {
		// Start with all funded accounts
		allAccounts = funded

		// Re-fund the unfunded subset
		if len(unfunded) > 0 {
			toRefund := unfunded
			if len(funded)+len(toRefund) > dynamicCount {
				toRefund = toRefund[:dynamicCount-len(funded)]
			}

			if len(toRefund) > 0 {
				lg.initPhase = types.InitPhaseFundingAccts
				lg.initProgress = fmt.Sprintf("Re-funding %d cached accounts...", len(toRefund))

				if err := lg.resetBuilderNonces(); err != nil {
					lg.logger.Warn("failed to reset builder nonces for re-funding", "error", err)
					return false
				}

				if err := lg.accountMgr.FundAccounts(ctx, lg.builderClient, lg.l2Client, toRefund); err != nil {
					lg.logger.Warn("failed to re-fund cached accounts", "error", err)
				} else {
					allAccounts = append(allAccounts, toRefund...)
				}
			}
		}

		// Generate + fund additional accounts if still short
		if len(allAccounts) < dynamicCount {
			needNew = dynamicCount - len(allAccounts)
			lg.initPhase = types.InitPhaseGeneratingAccts
			lg.initProgress = fmt.Sprintf("Generating %d additional accounts...", needNew)

			if err := lg.accountMgr.GenerateDynamicAccounts(needNew); err != nil {
				lg.logger.Warn("failed to generate additional accounts", "error", err)
				// Continue with what we have
			} else {
				newAccounts := lg.accountMgr.GetDynamicAccounts()

				lg.initPhase = types.InitPhaseFundingAccts
				lg.initProgress = fmt.Sprintf("Funding %d new accounts...", needNew)

				if err := lg.resetBuilderNonces(); err != nil {
					lg.logger.Warn("failed to reset builder nonces", "error", err)
				}

				if err := lg.accountMgr.FundDynamicAccounts(ctx, lg.builderClient, lg.l2Client); err != nil {
					lg.logger.Warn("failed to fund new accounts", "error", err)
				}

				allAccounts = append(allAccounts, newAccounts...)
			}
		}
	}

	// Set the accounts on the manager
	lg.accountMgr.SetDynamicAccounts(allAccounts)
	lg.initAccountsGen = len(allAccounts)
	lg.initFundingSent = len(allAccounts)

	// Init nonces for cached accounts
	lg.initPhase = types.InitPhaseInitNonces
	lg.initProgress = "Initializing nonces for cached accounts..."
	if err := lg.accountMgr.InitializeDynamicNonces(ctx, lg.builderClient); err != nil {
		lg.logger.Warn("failed to initialize cached account nonces", "error", err)
	}

	// Persist any newly generated accounts back to cache
	if needNew > 0 {
		lg.saveDynamicAccountsToCache(chainID)
	}

	lg.logger.Info("warm start complete",
		slog.Int("totalAccounts", len(allAccounts)),
		slog.Int("fromCache", len(allAccounts)-needNew),
		slog.Int("newlyGenerated", needNew),
	)
	return true
}

// saveDynamicAccountsToCache persists current dynamic accounts to the cache.
func (lg *LoadGenerator) saveDynamicAccountsToCache(chainID int64) {
	if lg.cacheStorage == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pairs := lg.accountMgr.ExportDynamicAccountKeys()
	cached := make([]storage.CachedAccount, len(pairs))
	now := time.Now()
	for i, p := range pairs {
		cached[i] = storage.CachedAccount{
			Address:       p.Address,
			PrivateKeyHex: p.PrivateKeyHex,
			ChainID:       chainID,
			CreatedAt:     now,
		}
	}

	if err := lg.cacheStorage.SaveCachedAccounts(ctx, cached); err != nil {
		lg.logger.Warn("failed to save accounts to cache", "error", err)
	} else {
		lg.logger.Info("saved accounts to cache", slog.Int("count", len(cached)))
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

// saveTestResult saves the completed test to history and persists to storage.
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

	// Get on-chain verification metrics by querying blocks
	onChainMetrics := lg.calculateOnChainMetrics(ctx)

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
	if lg.l2Client != nil {
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


// startIncrementalVerification starts the background incremental verification goroutine.
// This runs every 5 minutes during long tests to avoid massive verification at the end.
func (lg *LoadGenerator) startIncrementalVerification() {
	lg.incrementalStopCh = make(chan struct{})

	// Initialize verifier if not already done
	if lg.verifier == nil {
		lg.verifier = verification.NewVerifierWithConfig(lg.l2Client, lg.logger, lg.includeDepositTx)
	}

	// Clear previous snapshots
	lg.incrementalSnapshotsMu.Lock()
	lg.incrementalSnapshots = nil
	lg.incrementalSnapshotsMu.Unlock()

	lg.wg.Add(1)
	go func() {
		defer lg.wg.Done()

		// Run every 5 minutes
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		lg.logger.Info("started incremental verification", "interval", "5m")

		for {
			select {
			case <-lg.incrementalStopCh:
				lg.logger.Info("stopped incremental verification")
				return
			case <-lg.ctx.Done():
				return
			case <-ticker.C:
				lg.runIncrementalVerification()
			}
		}
	}()
}

// runIncrementalVerification performs a single incremental verification snapshot.
func (lg *LoadGenerator) runIncrementalVerification() {
	// Get recent blocks
	lg.recentBlockNumbersMu.Lock()
	recentBlocks := make([]uint64, len(lg.recentBlockNumbers))
	copy(recentBlocks, lg.recentBlockNumbers)
	lg.recentBlockNumbers = nil // Clear after copying
	lg.recentBlockNumbersMu.Unlock()

	// Get recent confirmed TX hashes
	lg.recentConfirmedMu.Lock()
	recentHashes := make([]string, len(lg.recentConfirmedHashes))
	copy(recentHashes, lg.recentConfirmedHashes)
	lg.recentConfirmedHashes = nil // Clear after copying
	lg.recentConfirmedMu.Unlock()

	// Debug: Log what data we have
	lg.logger.Info("incremental verification data collected",
		"recentBlocks", len(recentBlocks),
		"recentHashes", len(recentHashes))

	if len(recentBlocks) == 0 && len(recentHashes) == 0 {
		lg.logger.Debug("incremental verification skipped - no recent data")
		return
	}

	// Create verification context with timeout
	ctx, cancel := context.WithTimeout(lg.ctx, 30*time.Second)
	defer cancel()

	// Run incremental verification
	snapshot := lg.verifier.VerifyIncremental(
		ctx,
		recentBlocks,
		recentHashes,
		lg.txOrdering, // "fifo", "tip_desc", or "tip_asc"
		10,            // blocks to sample for tip ordering
		10,            // receipts to sample
	)

	// Store snapshot
	lg.incrementalSnapshotsMu.Lock()
	lg.incrementalSnapshots = append(lg.incrementalSnapshots, *snapshot)
	lg.incrementalSnapshotsMu.Unlock()

	lg.logger.Info("incremental verification snapshot",
		"firstBlock", snapshot.FirstBlock,
		"lastBlock", snapshot.LastBlock,
		"blocksSampled", snapshot.BlocksSampled,
		"receiptsSampled", snapshot.ReceiptsSampled,
		"violations", snapshot.Violations,
		"receiptsReverted", snapshot.ReceiptsReverted,
		"totalSnapshots", len(lg.incrementalSnapshots))
}

// stopIncrementalVerification stops the background verification and runs a final snapshot.
func (lg *LoadGenerator) stopIncrementalVerification() {
	if lg.incrementalStopCh != nil {
		close(lg.incrementalStopCh)
		lg.incrementalStopCh = nil
	}

	// Run one final incremental verification to capture any remaining data
	lg.runIncrementalVerification()
}

// getIncrementalSnapshots returns the collected incremental verification snapshots.
func (lg *LoadGenerator) getIncrementalSnapshots() []storage.IncrementalVerificationSnapshot {
	lg.incrementalSnapshotsMu.Lock()
	defer lg.incrementalSnapshotsMu.Unlock()
	result := make([]storage.IncrementalVerificationSnapshot, len(lg.incrementalSnapshots))
	copy(result, lg.incrementalSnapshots)
	return result
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
