package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/websocket"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/contract"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/pattern"
	"github.com/gateway-fm/loadgenerator/internal/ratelimit"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/sender"
	"github.com/gateway-fm/loadgenerator/internal/storage"
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

// StopTest gracefully stops the running test, waits for confirmations,
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

// setError sets the test error state.
func (lg *LoadGenerator) setError(msg string) {
	lg.statusMu.Lock()
	lg.status = types.StatusError
	lg.testError = msg
	lg.statusMu.Unlock()
	lg.logger.Error("test error", "error", msg)
}

