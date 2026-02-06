// Package storage provides persistence for load test history.
package storage

import (
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// TestRun represents a persisted test run with summary statistics.
// JSON tags use camelCase to match TypeScript API expectations.
type TestRun struct {
	ID               string                  `json:"id"`
	StartedAt        time.Time               `json:"startedAt"`
	CompletedAt      *time.Time              `json:"completedAt,omitempty"`
	Pattern          types.LoadPattern       `json:"pattern"`
	TransactionType  types.TransactionType   `json:"transactionType"`
	DurationMs       int64                   `json:"durationMs"`
	TxSent           uint64                  `json:"txSent"`
	TxConfirmed      uint64                  `json:"txConfirmed"`
	TxFailed         uint64                  `json:"txFailed"`
	TxDiscarded      uint64                  `json:"txDiscarded,omitempty"` // Transactions still pending at test end
	AverageTPS       float64                 `json:"averageTps"`
	PeakTPS          int                     `json:"peakTps"`
	LatencyStats     *types.LatencyStats     `json:"latencyStats,omitempty"`
	PreconfLatency   *types.LatencyStats     `json:"preconfLatency,omitempty"`
	Config           *types.StartTestRequest `json:"config,omitempty"`
	Status           string                  `json:"status"` // "running", "completed", "error"
	ErrorMessage     string                  `json:"errorMessage,omitempty"`
	TxLoggingEnabled bool                    `json:"txLoggingEnabled"`
	ExecutionLayer   string                  `json:"executionLayer"` // "reth" or "cdk-erigon"
	// Block metrics (aggregated from time series)
	BlockCount     int     `json:"blockCount,omitempty"`     // Total blocks produced during test
	TotalGasUsed   uint64  `json:"totalGasUsed,omitempty"`   // Total gas used across all blocks
	AvgFillRate    float64 `json:"avgFillRate,omitempty"`    // Average block fill rate (0-100)
	PeakMgasPerSec float64 `json:"peakMgasPerSec,omitempty"` // Peak MGas/s observed
	AvgMgasPerSec  float64 `json:"avgMgasPerSec,omitempty"`  // Average MGas/s
	// User-defined metadata
	CustomName *string `json:"customName,omitempty"` // User-defined test name
	IsFavorite bool    `json:"isFavorite"`           // Whether test is starred/favorited
	// On-chain verification metrics (actual chain state after test)
	OnChainFirstBlock   uint64  `json:"onChainFirstBlock,omitempty"`   // First block number during test
	OnChainLastBlock    uint64  `json:"onChainLastBlock,omitempty"`    // Last block number during test
	OnChainTxCount      uint64  `json:"onChainTxCount,omitempty"`      // Actual TX count in blocks
	OnChainGasUsed      uint64  `json:"onChainGasUsed,omitempty"`      // Total gas used across blocks
	OnChainMgasPerSec   float64 `json:"onChainMgasPerSec,omitempty"`   // Actual MGas/s from chain
	OnChainTps          float64 `json:"onChainTps,omitempty"`          // Actual TPS from chain
	OnChainDurationSecs float64 `json:"onChainDurationSecs,omitempty"` // Duration from first to last block
	// Realistic test specific metrics
	TipHistogram   []types.TipHistogramBucket `json:"tipHistogram,omitempty"`
	TxTypeMetrics  []types.TxTypeMetrics      `json:"txTypeMetrics,omitempty"`
	PendingLatency *types.LatencyStats        `json:"pendingLatency,omitempty"`
	AccountsActive int                        `json:"accountsActive,omitempty"`
	AccountsFunded int                        `json:"accountsFunded,omitempty"`
	// Environment snapshot (config captured at test start)
	Environment *EnvironmentSnapshot `json:"environment,omitempty"`
	// Verification results (analysis after test completion)
	Verification *VerificationResult `json:"verification,omitempty"`
	// Deployed contracts (addresses deployed for this test)
	DeployedContracts []DeployedContract `json:"deployedContracts,omitempty"`
	// Test accounts (generated/funded for this test)
	TestAccounts *TestAccountsInfo `json:"testAccounts,omitempty"`
}

// DeployedContract represents a contract deployed for testing.
type DeployedContract struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// AccountRole describes the role of an account in the test.
type AccountRole string

const (
	AccountRoleDeployer AccountRole = "deployer" // Reserved for contract deployment
	AccountRoleFunder   AccountRole = "funder"   // Used to fund other accounts
	AccountRoleFunded   AccountRole = "funded"   // Dynamically generated and funded
	AccountRoleBuiltIn  AccountRole = "built-in" // Premined test account (Anvil/Hardhat)
)

// AccountInfo represents a single test account with its role.
type AccountInfo struct {
	Address string      `json:"address"`
	Role    AccountRole `json:"role"`
	Index   int         `json:"index"` // Index within its category (e.g., funder 1, funder 2)
}

// TestAccountsInfo contains information about test accounts.
type TestAccountsInfo struct {
	TotalCount    int       `json:"totalCount"`    // Total accounts used (built-in + dynamic)
	DynamicCount  int       `json:"dynamicCount"`  // Dynamically created accounts
	FundedCount   int       `json:"fundedCount"`   // Successfully funded accounts
	FunderAddress string    `json:"funderAddress"` // Address that funded the accounts (deployer)
	Accounts      []string  `json:"accounts,omitempty"` // Legacy: Account addresses (limited to first 100)

	// New: All accounts with roles
	AllAccounts []AccountInfo `json:"allAccounts,omitempty"` // All accounts with their roles
}

// EnvironmentSnapshot captures builder and load-gen config at test start.
// This provides full reproducibility context for each test run.
type EnvironmentSnapshot struct {
	// Block builder config (from /status)
	BuilderBlockTimeMs      int    `json:"builderBlockTimeMs"`
	BuilderGasLimit         uint64 `json:"builderGasLimit"`
	BuilderMaxTxsPerBlock   int    `json:"builderMaxTxsPerBlock"`
	BuilderTxOrdering       string `json:"builderTxOrdering"` // "fifo", "tip_desc", "tip_asc"
	BuilderEnablePreconfs   bool   `json:"builderEnablePreconfs"`
	BuilderSkipEmptyBlocks  bool   `json:"builderSkipEmptyBlocks"`
	BuilderIncludeDepositTx bool   `json:"builderIncludeDepositTx"` // Include L1 deposit TX in each block

	// Load generator config
	LoadGenGasTipCapGwei  float64 `json:"loadGenGasTipCapGwei"`
	LoadGenGasFeeCapGwei  float64 `json:"loadGenGasFeeCapGwei"`
	LoadGenExecutionLayer string  `json:"loadGenExecutionLayer"`

	// Node identification (for test reproducibility and comparison)
	NodeName        string `json:"nodeName"`        // "op-reth", "gravity-reth", "cdk-erigon"
	NodeVersion     string `json:"nodeVersion"`     // e.g., "reth/v1.9.3", "gravity-reth/v0.4.1"
	NodeImage       string `json:"nodeImage"`       // Docker image used (if available)
	ChainID         uint64 `json:"chainId"`         // Chain ID from eth_chainId
	UseBlockBuilder bool   `json:"useBlockBuilder"` // true if external block builder, false for internal sequencer
}

// VerificationResult contains post-test verification analysis.
type VerificationResult struct {
	// On-chain metrics comparison
	MetricsMatch bool  `json:"metricsMatch"`
	TxCountDelta int64 `json:"txCountDelta"` // OnChain - Confirmed (positive = missed confirmations)
	GasUsedDelta int64 `json:"gasUsedDelta"`

	// Tip ordering verification (only if tip_desc or tip_asc)
	TipOrdering *TipOrderingResult `json:"tipOrdering,omitempty"`

	// TX receipt sampling
	TxReceipts *TxReceiptVerification `json:"txReceipts,omitempty"`

	// Summary
	AllChecksPass bool     `json:"allChecksPass"`
	Warnings      []string `json:"warnings,omitempty"`

	// Incremental verification (for long-running tests)
	IncrementalMode bool                              `json:"incrementalMode,omitempty"` // True if incremental verification was used
	Snapshots       []IncrementalVerificationSnapshot `json:"snapshots,omitempty"`       // Periodic samples (limited to last 100)
	SnapshotCount   int                               `json:"snapshotCount,omitempty"`   // Total snapshots taken during test
}

// TipOrderingResult contains tip ordering verification results.
type TipOrderingResult struct {
	Verified           bool                `json:"verified"`
	TotalBlocks        int                 `json:"totalBlocks"`
	BlocksSampled      int                 `json:"blocksSampled"`
	CorrectlyOrdered   int                 `json:"correctlyOrdered"`
	ViolationCount     int                 `json:"violationCount"`               // Total violations (for incremental mode)
	OrderingViolations []OrderingViolation `json:"orderingViolations,omitempty"` // Detailed violations (for non-incremental mode)
	SampleBlocks       []BlockTipAnalysis  `json:"sampleBlocks,omitempty"`
}

// OrderingViolation represents a single tip ordering violation in a block.
type OrderingViolation struct {
	BlockNumber uint64 `json:"blockNumber"`
	TxIndex     int    `json:"txIndex"`
	ExpectedTip uint64 `json:"expectedTip"` // Tip at index-1 (for tip_desc, should be >= actualTip)
	ActualTip   uint64 `json:"actualTip"`
}

// BlockTipAnalysis provides detailed tip analysis for a sampled block.
type BlockTipAnalysis struct {
	BlockNumber uint64   `json:"blockNumber"`
	TxCount     int      `json:"txCount"`
	Tips        []uint64 `json:"tips"` // Tip values in transaction order
	IsOrdered   bool     `json:"isOrdered"`
}

// TxReceiptVerification contains TX receipt sampling results.
type TxReceiptVerification struct {
	SampleSize       int               `json:"sampleSize"`
	SuccessCount     int               `json:"successCount"`
	RevertCount      int               `json:"revertCount"`
	AvgGasUsed       uint64            `json:"avgGasUsed"`
	MinGasUsed       uint64            `json:"minGasUsed"`
	MaxGasUsed       uint64            `json:"maxGasUsed"`
	TotalGasVerified uint64            `json:"totalGasVerified"`
	Samples          []TxReceiptSample `json:"samples,omitempty"`      // First 10 samples for inspection
	RevertedTxs      []TxReceiptSample `json:"revertedTxs,omitempty"`  // All reverted TXs (up to 100)
}

// TxReceiptSample represents a single sampled transaction receipt.
type TxReceiptSample struct {
	TxHash            string `json:"txHash"`
	BlockNumber       uint64 `json:"blockNumber"`
	GasUsed           uint64 `json:"gasUsed"`
	Status            uint64 `json:"status"` // 1=success, 0=revert
	EffectiveGasPrice uint64 `json:"effectiveGasPrice"`
}

// IncrementalVerificationSnapshot represents a periodic verification sample taken during a test.
// These are aggregated at test end for the final VerificationResult.
type IncrementalVerificationSnapshot struct {
	Timestamp        time.Time         `json:"timestamp"`
	FirstBlock       uint64            `json:"firstBlock"`       // First block in this sample window
	LastBlock        uint64            `json:"lastBlock"`        // Last block in this sample window
	BlocksSampled    int               `json:"blocksSampled"`    // Blocks checked for tip ordering
	BlocksOrdered    int               `json:"blocksOrdered"`    // Blocks with correct tip ordering
	ReceiptsSampled  int               `json:"receiptsSampled"`
	ReceiptsSuccess  int               `json:"receiptsSuccess"`
	ReceiptsReverted int               `json:"receiptsReverted"`
	TotalGasUsed     uint64            `json:"totalGasUsed"`     // Gas used in sampled receipts
	MinGasUsed       uint64            `json:"minGasUsed"`       // Min gas used in sampled receipts
	MaxGasUsed       uint64            `json:"maxGasUsed"`       // Max gas used in sampled receipts
	Violations       int               `json:"violations"`       // Tip ordering violations found
	RevertedTxs      []TxReceiptSample `json:"revertedTxs,omitempty"` // Reverted TXs in this snapshot (up to 10)
}

// TestRunMetadataUpdate represents an update to test run metadata (name/favorite).
type TestRunMetadataUpdate struct {
	CustomName *string `json:"customName,omitempty"`
	IsFavorite *bool   `json:"isFavorite,omitempty"`
}

// TimeSeriesPoint represents a single metrics sample during a test.
// JSON tags use camelCase to match TypeScript API expectations.
type TimeSeriesPoint struct {
	TimestampMs  int64   `json:"timestampMs"` // Milliseconds since test start
	TxSent       uint64  `json:"txSent"`
	TxConfirmed  uint64  `json:"txConfirmed"`
	TxFailed     uint64  `json:"txFailed"`
	CurrentTPS   float64 `json:"currentTps"`
	TargetTPS    int     `json:"targetTps"`
	PendingCount int64   `json:"pendingCount"`
	// Block metrics (from L2 newHeads subscription)
	GasUsed     uint64  `json:"gasUsed,omitempty"`     // Gas used in blocks during this period
	GasLimit    uint64  `json:"gasLimit,omitempty"`    // Gas limit of blocks
	BlockCount  int     `json:"blockCount,omitempty"`  // Number of blocks produced
	MgasPerSec  float64 `json:"mgasPerSec,omitempty"`  // Calculated MGas/s
	FillRate    float64 `json:"fillRate,omitempty"`    // Gas usage percentage (0-100)
	// Block timing and gas pricing (for historical mode display)
	AvgBlockTimeMs   float64 `json:"avgBlockTimeMs,omitempty"`   // Average block time in period (ms)
	BaseFeeGwei      float64 `json:"baseFeeGwei,omitempty"`      // Latest base fee per gas (gwei)
	GasPriceGwei     float64 `json:"gasPriceGwei,omitempty"`     // Latest gas price from eth_gasPrice (gwei)
}

// TxLogEntry represents a single transaction record.
// JSON tags use camelCase to match TypeScript API expectations.
type TxLogEntry struct {
	TxHash           string  `json:"txHash"`
	SentAtMs         int64   `json:"sentAtMs"`
	ConfirmedAtMs    int64   `json:"confirmedAtMs,omitempty"`    // 0 if not confirmed
	PreconfAtMs      int64   `json:"preconfAtMs,omitempty"`      // 0 if no preconf
	ConfirmLatencyMs int64   `json:"confirmLatencyMs,omitempty"` // 0 if not confirmed
	PreconfLatencyMs int64   `json:"preconfLatencyMs,omitempty"` // 0 if no preconf
	Status           string  `json:"status"`                     // "pending", "confirmed", "failed"
	ErrorReason      string  `json:"errorReason,omitempty"`
	FromAccount      int     `json:"fromAccount"`
	Nonce            uint64  `json:"nonce"`
	GasTipGwei       float64 `json:"gasTipGwei,omitempty"`
}

// TestRunDetail combines a test run with its time-series data.
// JSON tags use camelCase to match TypeScript API expectations.
type TestRunDetail struct {
	Run        *TestRun          `json:"run"`
	TimeSeries []TimeSeriesPoint `json:"timeSeries"`
}

// PaginatedTestRuns represents a paginated list of test runs.
type PaginatedTestRuns struct {
	Runs   []TestRun `json:"runs"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// PaginatedTxLogs represents a paginated list of transaction logs.
type PaginatedTxLogs struct {
	Transactions []TxLogEntry `json:"transactions"`
	Total        int          `json:"total"`
	Limit        int          `json:"limit"`
	Offset       int          `json:"offset"`
}
