// Package types contains public API types for the load generator.
// These types form the external interface and must remain backwards-compatible.
package types

import "time"

// LoadPattern represents the type of load pattern.
type LoadPattern string

const (
	PatternConstant          LoadPattern = "constant"
	PatternRamp              LoadPattern = "ramp"
	PatternSpike             LoadPattern = "spike"
	PatternAdaptive          LoadPattern = "adaptive"
	PatternRealistic         LoadPattern = "realistic"
	PatternAdaptiveRealistic LoadPattern = "adaptive-realistic"
)

// TransactionType represents the type of transaction to generate.
type TransactionType string

const (
	TxTypeEthTransfer   TransactionType = "eth-transfer"
	TxTypeERC20Transfer TransactionType = "erc20-transfer"
	TxTypeERC20Approve  TransactionType = "erc20-approve"
	TxTypeUniswapSwap   TransactionType = "uniswap-swap"
	TxTypeStorageWrite  TransactionType = "storage-write"
	TxTypeHeavyCompute  TransactionType = "heavy-compute"
)

// TestStatus represents the current test state.
type TestStatus string

const (
	StatusIdle         TestStatus = "idle"
	StatusInitializing TestStatus = "initializing" // Test is being set up (accounts, contracts)
	StatusRunning      TestStatus = "running"
	StatusVerifying    TestStatus = "verifying" // Test finished, verifying results on-chain
	StatusCompleted    TestStatus = "completed"
	StatusError        TestStatus = "error"
)

// InitPhase represents the current initialization phase.
type InitPhase string

const (
	InitPhaseNone               InitPhase = ""
	InitPhaseGeneratingAccts    InitPhase = "generating_accounts"
	InitPhaseFundingAccts       InitPhase = "funding_accounts"
	InitPhaseWaitingForFunding  InitPhase = "waiting_for_funding"
	InitPhaseInitNonces         InitPhase = "initializing_nonces"
	InitPhaseDeployingContracts InitPhase = "deploying_contracts"
	InitPhaseStartingWorkers    InitPhase = "starting_workers"
)

// VerifyPhase represents the current verification phase.
type VerifyPhase string

const (
	VerifyPhaseNone           VerifyPhase = ""
	VerifyPhaseOnChainMetrics VerifyPhase = "on_chain_metrics" // Fetching blocks from chain
	VerifyPhaseAggregating    VerifyPhase = "aggregating"      // Aggregating incremental snapshots
	VerifyPhaseTxCount        VerifyPhase = "tx_count"
	VerifyPhaseTipOrder       VerifyPhase = "tip_ordering"
	VerifyPhaseReceipts       VerifyPhase = "receipts"
)

// TipDistribution represents how tips are distributed in realistic tests.
type TipDistribution string

const (
	TipDistExponential TipDistribution = "exponential"
	TipDistPowerLaw    TipDistribution = "power-law"
	TipDistUniform     TipDistribution = "uniform"
)

// LatencyBucket represents a latency histogram bucket.
type LatencyBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// LatencyStats holds latency statistics.
type LatencyStats struct {
	Count   int             `json:"count"`
	Min     float64         `json:"min"`     // ms
	Max     float64         `json:"max"`     // ms
	Avg     float64         `json:"avg"`     // ms
	P50     float64         `json:"p50"`     // ms
	P75     float64         `json:"p75"`     // ms
	P90     float64         `json:"p90"`     // ms
	P95     float64         `json:"p95"`     // ms
	P99     float64         `json:"p99"`     // ms
	Buckets []LatencyBucket `json:"buckets"` // histogram
}

// TxTypeRatio defines the percentage for each transaction type (must sum to 100).
type TxTypeRatio struct {
	EthTransfer   int `json:"ethTransfer"`
	ERC20Transfer int `json:"erc20Transfer"`
	ERC20Approve  int `json:"erc20Approve"`
	UniswapSwap   int `json:"uniswapSwap"`
	StorageWrite  int `json:"storageWrite"`
	HeavyCompute  int `json:"heavyCompute"`
}

// RealisticTestConfig holds realistic test specific configuration.
type RealisticTestConfig struct {
	NumAccounts     int             `json:"numAccounts"`     // 10-10000
	TargetTPS       int             `json:"targetTps"`       // target transactions per second
	MinTipGwei      float64         `json:"minTipGwei"`      // minimum tip in Gwei
	MaxTipGwei      float64         `json:"maxTipGwei"`      // maximum tip in Gwei
	TipDistribution TipDistribution `json:"tipDistribution"` // exponential, power-law, uniform
	TxTypeRatios    TxTypeRatio     `json:"txTypeRatios"`    // percentage per tx type
}

// TipHistogramBucket for reporting tip distribution.
type TipHistogramBucket struct {
	MinGwei float64 `json:"minGwei"`
	MaxGwei float64 `json:"maxGwei"`
	Count   int     `json:"count"`
}

// TxTypeMetrics tracks per-type statistics.
type TxTypeMetrics struct {
	Type       TransactionType `json:"type"`
	Sent       uint64          `json:"sent"`
	Confirmed  uint64          `json:"confirmed"`
	Failed     uint64          `json:"failed"`
	AvgTipGwei float64         `json:"avgTipGwei"`
}

// TxFlowStats contains aggregated TX flow statistics.
// Tracks how transactions journey through stages (sent → pending → preconfirmed → confirmed).
type TxFlowStats struct {
	// Flow patterns
	DirectConfirmed  int `json:"directConfirmed"`  // sent → confirmed (no preconf)
	PendingConfirmed int `json:"pendingConfirmed"` // sent → pending → confirmed
	PreconfConfirmed int `json:"preconfConfirmed"` // sent → pending → preconfirmed → confirmed
	DroppedRequeued  int `json:"droppedRequeued"`  // had dropped/requeued in flow
	RevokedFlow      int `json:"revokedFlow"`      // had revoked in flow
	FailedFlow       int `json:"failedFlow"`       // ended in failed

	// Summary
	TotalTracked  int     `json:"totalTracked"`  // Total TXs tracked
	AvgStageCount float64 `json:"avgStageCount"` // Average stages per TX
}

// TestMetrics holds real-time test metrics.
type TestMetrics struct {
	Status          TestStatus      `json:"status"`
	TxSent          uint64          `json:"txSent"`
	TxConfirmed     uint64          `json:"txConfirmed"`
	TxFailed        uint64          `json:"txFailed"`
	TxDiscarded     uint64          `json:"txDiscarded,omitempty"` // Transactions still pending at test end (discarded)
	CurrentTPS      float64         `json:"currentTps"`
	AverageTPS      float64         `json:"averageTps"`
	ElapsedMs       int64           `json:"elapsedMs"`
	DurationMs      int64           `json:"durationMs"`
	TargetTPS       int             `json:"targetTps"`
	Pattern         LoadPattern     `json:"pattern"`
	TransactionType TransactionType `json:"transactionType"`
	Error           string          `json:"error,omitempty"`
	Warnings        []string        `json:"warnings,omitempty"` // Non-fatal warnings (e.g., insufficient accounts)
	// For max pattern - adaptive rate tracking
	PeakTPS int `json:"peakTps,omitempty"`

	// Initialization progress (shown during StatusInitializing)
	InitPhase         InitPhase `json:"initPhase,omitempty"`         // Current initialization phase
	InitProgress      string    `json:"initProgress,omitempty"`      // Human-readable progress message
	AccountsTotal     int       `json:"accountsTotal,omitempty"`     // Total accounts needed
	AccountsGenerated int       `json:"accountsGenerated,omitempty"` // Accounts generated so far
	FundingTxsSent    int       `json:"fundingTxsSent,omitempty"`    // Funding TXs sent
	FundingTxsTotal   int       `json:"fundingTxsTotal,omitempty"`   // Total funding TXs needed
	ContractsDeployed int       `json:"contractsDeployed,omitempty"` // Contracts deployed
	ContractsTotal    int       `json:"contractsTotal,omitempty"`    // Total contracts to deploy

	// Verification progress (shown during StatusVerifying)
	VerifyPhase      VerifyPhase `json:"verifyPhase,omitempty"`      // Current verification phase
	VerifyProgress   string      `json:"verifyProgress,omitempty"`   // Human-readable progress message
	BlocksToVerify   int         `json:"blocksToVerify,omitempty"`   // Total blocks to verify for tip ordering
	BlocksVerified   int         `json:"blocksVerified,omitempty"`   // Blocks verified so far
	ReceiptsToSample int         `json:"receiptsToSample,omitempty"` // Total receipts to sample
	ReceiptsSampled  int         `json:"receiptsSampled,omitempty"`  // Receipts sampled so far

	// Preconfirmation stage counters (Flashblocks-compliant lifecycle)
	TxPending      uint64 `json:"txPending,omitempty"`      // TX received by sequencer (queued)
	TxPreconfirmed uint64 `json:"txPreconfirmed,omitempty"` // TX selected for block (sequencer commitment)
	TxRevoked      uint64 `json:"txRevoked,omitempty"`      // Preconfirmation broken (execution rejected)
	TxDropped      uint64 `json:"txDropped,omitempty"`      // TX permanently dropped
	TxRequeued     uint64 `json:"txRequeued,omitempty"`     // TX requeued for later block

	// TX flow tracking (journey analysis)
	FlowStats *TxFlowStats `json:"flowStats,omitempty"`

	// Latency statistics
	Latency        *LatencyStats `json:"latency,omitempty"`        // Confirmation latency (send to confirmed)
	PreconfLatency *LatencyStats `json:"preconfLatency,omitempty"` // Preconfirmation latency (send to preconfirmed)
	PendingLatency *LatencyStats `json:"pendingLatency,omitempty"` // Pending latency (send to pending)

	// Realistic test specific metrics
	TipHistogram   []TipHistogramBucket `json:"tipHistogram,omitempty"`
	TxTypeMetrics  []TxTypeMetrics      `json:"txTypeMetrics,omitempty"`
	AccountsActive int                  `json:"accountsActive,omitempty"`
	AccountsFunded int                  `json:"accountsFunded,omitempty"`

	// Block gas metrics (from block builder status)
	LatestBaseFeeGwei  float64 `json:"latestBaseFeeGwei,omitempty"`  // Latest block's baseFeePerGas in gwei
	LatestGasPriceGwei float64 `json:"latestGasPriceGwei,omitempty"` // Latest eth_gasPrice from L2 node in gwei
	LatestGasUsed      uint64  `json:"latestGasUsed,omitempty"`      // Latest block's gasUsed

	// Aggregate block metrics (for live dashboard - matches history metrics)
	TotalGasUsed   uint64  `json:"totalGasUsed,omitempty"`   // Total gas used across all blocks
	BlockCount     int     `json:"blockCount,omitempty"`     // Number of blocks produced
	PeakMgasPerSec float64 `json:"peakMgasPerSec,omitempty"` // Peak MGas/s observed
	AvgMgasPerSec  float64 `json:"avgMgasPerSec,omitempty"`  // Average MGas/s
	AvgFillRate    float64 `json:"avgFillRate,omitempty"`    // Average block fill rate (0-100)

	// Current rolling metrics (for live chart - sampled at 200ms)
	CurrentMgasPerSec float64 `json:"currentMgasPerSec,omitempty"` // Rolling window MGas/s (for chart)
	CurrentFillRate   float64 `json:"currentFillRate,omitempty"`   // Current block fill rate (for chart)

	// HSM / block attestation metadata (optional)
	BlockAttestationEnabled bool   `json:"blockAttestationEnabled"`
	HSMProvider             string `json:"hsmProvider,omitempty"`
	HSMKeyIDActive          string `json:"hsmKeyIdActive,omitempty"`
	HSMFailoverEnabled      bool   `json:"hsmFailoverEnabled"`
}

// TestResult stores the final results of a completed test.
type TestResult struct {
	ID              string           `json:"id"`
	StartedAt       time.Time        `json:"startedAt"`
	CompletedAt     time.Time        `json:"completedAt"`
	Pattern         LoadPattern      `json:"pattern"`
	TransactionType TransactionType  `json:"transactionType"`
	DurationMs      int64            `json:"durationMs"`
	TxSent          uint64           `json:"txSent"`
	TxConfirmed     uint64           `json:"txConfirmed"`
	TxFailed        uint64           `json:"txFailed"`
	TxDiscarded     uint64           `json:"txDiscarded,omitempty"` // Transactions still pending at test end
	AverageTPS      float64          `json:"averageTps"`
	PeakTPS         int              `json:"peakTps,omitempty"`
	Latency         *LatencyStats    `json:"latency,omitempty"`
	PreconfLatency  *LatencyStats    `json:"preconfLatency,omitempty"`
	FlowStats       *TxFlowStats     `json:"flowStats,omitempty"` // TX flow tracking stats
	Config          StartTestRequest `json:"config"`
}

// StartTestRequest is the API request to start a test.
type StartTestRequest struct {
	// Common
	Pattern         LoadPattern     `json:"pattern"`
	DurationSec     int             `json:"durationSec"`
	NumAccounts     int             `json:"numAccounts,omitempty"`
	TransactionType TransactionType `json:"transactionType,omitempty"` // Transaction type (default: eth-transfer)

	// Constant pattern
	ConstantRate int `json:"constantRate,omitempty"`

	// Ramp pattern
	RampStart int `json:"rampStart,omitempty"`
	RampEnd   int `json:"rampEnd,omitempty"`
	RampSteps int `json:"rampSteps,omitempty"`

	// Spike pattern
	BaselineRate  int `json:"baselineRate,omitempty"`
	SpikeRate     int `json:"spikeRate,omitempty"`
	SpikeDuration int `json:"spikeDuration,omitempty"` // seconds
	SpikeInterval int `json:"spikeInterval,omitempty"` // seconds

	// Adaptive pattern
	AdaptiveInitialRate   int `json:"adaptiveInitialRate,omitempty"`
	AdaptiveTargetPending int `json:"adaptiveTargetPending,omitempty"`
	AdaptiveRateStep      int `json:"adaptiveRateStep,omitempty"`

	// Realistic pattern
	RealisticConfig *RealisticTestConfig `json:"realisticConfig,omitempty"`
}

// PreconfEvent is the event received from the preconfirmation WebSocket.
type PreconfEvent struct {
	TxHash      string `json:"txHash"`
	Status      string `json:"status"` // pending, preconfirmed, confirmed, dropped, requeued, revoked
	BlockNumber uint64 `json:"blockNumber,omitempty"`
	Position    int    `json:"position,omitempty"`
	Timestamp   int64  `json:"timestamp"` // Unix milliseconds
}

// PreconfStage constants for preconfirmation status tracking.
const (
	PreconfStagePending      = "pending"      // TX received and queued
	PreconfStagePreconfirmed = "preconfirmed" // TX selected for block (sequencer commitment)
	PreconfStageConfirmed    = "confirmed"    // TX included in finalized block
	PreconfStageDropped      = "dropped"      // TX permanently dropped
	PreconfStageRequeued     = "requeued"     // TX requeued for later
	PreconfStageRevoked      = "revoked"      // Preconfirmation broken - TX rejected by execution layer
)

// PreconfBatchedEvent is a batch of preconfirmation events.
type PreconfBatchedEvent struct {
	Type        string          `json:"type"`                  // preconfirmed, confirmed, dropped, requeued, pending
	BlockNumber uint64          `json:"blockNumber,omitempty"` // Block number for the batch
	Events      []*PreconfEvent `json:"events"`                // Individual events in the batch
	Timestamp   int64           `json:"timestamp"`             // Unix milliseconds
	SeqNum      uint64          `json:"seqNum"`                // Sequence number for gap detection
}

// PreconfMessage is a union type that can hold either a single event or a batch.
// We first try to parse as batch (has "type" field), then fall back to single event.
type PreconfMessage struct {
	// Batch fields
	Type   string          `json:"type,omitempty"`
	Events []*PreconfEvent `json:"events,omitempty"`
	SeqNum uint64          `json:"seqNum,omitempty"` // Sequence number for gap detection
	// Single event fields
	TxHash      string `json:"txHash,omitempty"`
	Status      string `json:"status,omitempty"`
	BlockNumber uint64 `json:"blockNumber,omitempty"`
	Position    int    `json:"position,omitempty"`
	Timestamp   int64  `json:"timestamp,omitempty"`
}

// IsBatch returns true if this message is a batched event.
func (m *PreconfMessage) IsBatch() bool {
	return m.Type != "" && len(m.Events) > 0
}

// GetEvents returns all events from this message (batch or single).
func (m *PreconfMessage) GetEvents() []*PreconfEvent {
	if m.IsBatch() {
		return m.Events
	}
	if m.TxHash != "" {
		return []*PreconfEvent{{
			TxHash:      m.TxHash,
			Status:      m.Status,
			BlockNumber: m.BlockNumber,
			Position:    m.Position,
			Timestamp:   m.Timestamp,
		}}
	}
	return nil
}

// BuilderBlockMetrics is the event received from the builder's block metrics WebSocket.
// Contains per-block timing breakdown, rejection stats, and fill rate.
type BuilderBlockMetrics struct {
	BlockNumber uint64  `json:"blockNumber"`
	BlockHash   string  `json:"blockHash"`
	Timestamp   int64   `json:"timestamp"` // Block timestamp (Unix seconds)
	EmittedAt   int64   `json:"emittedAt"` // When event was emitted (Unix ms)
	GasUsed     uint64  `json:"gasUsed"`
	GasLimit    uint64  `json:"gasLimit"`
	TxCount     int     `json:"txCount"`
	FillRate    float64 `json:"fillRate"` // gasUsed/gasLimit * 100

	// Timing breakdown (all in milliseconds)
	FilterDurationMs     int64 `json:"filterDurationMs"`
	EngineApiDurationMs  int64 `json:"engineApiDurationMs"`
	TotalBuildDurationMs int64 `json:"totalBuildDurationMs"`

	// Rejection stats from this block
	Rejections BuilderBlockRejections `json:"rejections"`
}

// BuilderBlockRejections contains rejection reason counts for a single block.
type BuilderBlockRejections struct {
	NonceTooLow       int `json:"nonceTooLow"`
	NonceTooHigh      int `json:"nonceTooHigh"`
	GasLimitExceeded  int `json:"gasLimitExceeded"`
	InsufficientFunds int `json:"insufficientFunds"`
	Duplicate         int `json:"duplicate"`
	Other             int `json:"other"`
}
