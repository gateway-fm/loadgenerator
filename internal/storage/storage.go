package storage

import "context"

// Storage defines the persistence interface for load test data.
type Storage interface {
	// Test run lifecycle
	CreateTestRun(ctx context.Context, run *TestRun) error
	UpdateTestRun(ctx context.Context, run *TestRun) error
	CompleteTestRun(ctx context.Context, id string, run *TestRun) error
	GetTestRun(ctx context.Context, id string) (*TestRun, error)

	// History queries
	ListTestRuns(ctx context.Context, limit, offset int) (*PaginatedTestRuns, error)
	DeleteTestRun(ctx context.Context, id string) error
	UpdateTestRunMetadata(ctx context.Context, id string, update *TestRunMetadataUpdate) error

	// Time-series bulk operations (called after test completes)
	BulkInsertTimeSeries(ctx context.Context, testID string, points []TimeSeriesPoint) error
	GetTimeSeries(ctx context.Context, testID string) ([]TimeSeriesPoint, error)

	// Transaction log bulk operations (called after test completes)
	BulkInsertTxLogs(ctx context.Context, testID string, logs []TxLogEntry) error
	GetTxLogs(ctx context.Context, testID string, limit, offset int) (*PaginatedTxLogs, error)
	GetTxLogByHash(ctx context.Context, txHash string) (*TxLogEntry, error)

	// Lifecycle
	Close() error
}
