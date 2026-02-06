package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestJoinStrings(t *testing.T) {
	tests := []struct {
		name string
		strs []string
		sep  string
		want string
	}{
		{
			name: "empty slice",
			strs: []string{},
			sep:  ", ",
			want: "",
		},
		{
			name: "single element",
			strs: []string{"hello"},
			sep:  ", ",
			want: "hello",
		},
		{
			name: "two elements",
			strs: []string{"hello", "world"},
			sep:  ", ",
			want: "hello, world",
		},
		{
			name: "multiple elements with different separator",
			strs: []string{"a", "b", "c", "d"},
			sep:  " AND ",
			want: "a AND b AND c AND d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinStrings(tt.strs, tt.sep)
			if got != tt.want {
				t.Errorf("joinStrings(%v, %q) = %q, want %q", tt.strs, tt.sep, got, tt.want)
			}
		})
	}
}

func TestNullInt64(t *testing.T) {
	tests := []struct {
		name      string
		input     int64
		wantValid bool
		wantValue int64
	}{
		{
			name:      "zero returns invalid",
			input:     0,
			wantValid: false,
			wantValue: 0,
		},
		{
			name:      "positive value returns valid",
			input:     123,
			wantValid: true,
			wantValue: 123,
		},
		{
			name:      "negative value returns valid",
			input:     -456,
			wantValid: true,
			wantValue: -456,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nullInt64(tt.input)
			if got.Valid != tt.wantValid {
				t.Errorf("nullInt64(%d).Valid = %v, want %v", tt.input, got.Valid, tt.wantValid)
			}
			if got.Valid && got.Int64 != tt.wantValue {
				t.Errorf("nullInt64(%d).Int64 = %d, want %d", tt.input, got.Int64, tt.wantValue)
			}
		})
	}
}

func TestNullString(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantValid bool
		wantValue string
	}{
		{
			name:      "empty string returns invalid",
			input:     "",
			wantValid: false,
			wantValue: "",
		},
		{
			name:      "non-empty string returns valid",
			input:     "hello",
			wantValid: true,
			wantValue: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nullString(tt.input)
			if got.Valid != tt.wantValid {
				t.Errorf("nullString(%q).Valid = %v, want %v", tt.input, got.Valid, tt.wantValid)
			}
			if got.Valid && got.String != tt.wantValue {
				t.Errorf("nullString(%q).String = %q, want %q", tt.input, got.String, tt.wantValue)
			}
		})
	}
}

// createTestStorage creates a new SQLite storage with a temporary database.
func createTestStorage(t *testing.T) (*SQLiteStorage, func()) {
	t.Helper()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "storage_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	storage, err := NewSQLiteStorage(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	cleanup := func() {
		storage.Close()
		os.RemoveAll(tmpDir)
	}

	return storage, cleanup
}

func TestNewSQLiteStorage(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	if storage == nil {
		t.Fatal("expected storage to be non-nil")
	}
	if storage.db == nil {
		t.Fatal("expected db to be non-nil")
	}
}

func TestNewSQLiteStorage_InvalidPath(t *testing.T) {
	// Use a path that should be impossible to create
	_, err := NewSQLiteStorage("/nonexistent/directory/that/should/not/exist/test.db")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestCreateAndGetTestRun(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	startTime := time.Now()

	run := &TestRun{
		ID:              "test-123",
		StartedAt:       startTime,
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config: &types.StartTestRequest{
			Pattern:      types.PatternConstant,
			ConstantRate: 100,
			DurationSec:  30,
			NumAccounts:  50,
		},
		Status:           "running",
		TxLoggingEnabled: true,
		ExecutionLayer:   "reth",
	}

	// Create test run
	err := storage.CreateTestRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Get test run
	got, err := storage.GetTestRun(ctx, "test-123")
	if err != nil {
		t.Fatalf("GetTestRun failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected test run, got nil")
	}

	// Verify fields
	if got.ID != run.ID {
		t.Errorf("ID = %q, want %q", got.ID, run.ID)
	}
	if got.Pattern != run.Pattern {
		t.Errorf("Pattern = %q, want %q", got.Pattern, run.Pattern)
	}
	if got.TransactionType != run.TransactionType {
		t.Errorf("TransactionType = %q, want %q", got.TransactionType, run.TransactionType)
	}
	if got.DurationMs != run.DurationMs {
		t.Errorf("DurationMs = %d, want %d", got.DurationMs, run.DurationMs)
	}
	if got.Status != run.Status {
		t.Errorf("Status = %q, want %q", got.Status, run.Status)
	}
	if got.ExecutionLayer != run.ExecutionLayer {
		t.Errorf("ExecutionLayer = %q, want %q", got.ExecutionLayer, run.ExecutionLayer)
	}
}

func TestGetTestRun_NotFound(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	got, err := storage.GetTestRun(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("GetTestRun failed: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent test run, got %+v", got)
	}
}

func TestUpdateTestRun(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial test run
	run := &TestRun{
		ID:              "test-update",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "running",
		ExecutionLayer:  "reth",
	}
	err := storage.CreateTestRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Update the test run
	run.TxSent = 1000
	run.TxConfirmed = 950
	run.TxFailed = 50
	run.AverageTPS = 95.5
	run.PeakTPS = 120
	run.Status = "completed"
	run.LatencyStats = &types.LatencyStats{
		Count: 950,
		Min:   10,
		Max:   500,
		Avg:   150,
		P50:   120,
		P90:   300,
		P99:   450,
	}

	err = storage.UpdateTestRun(ctx, run)
	if err != nil {
		t.Fatalf("UpdateTestRun failed: %v", err)
	}

	// Verify update
	got, err := storage.GetTestRun(ctx, "test-update")
	if err != nil {
		t.Fatalf("GetTestRun failed: %v", err)
	}

	if got.TxSent != 1000 {
		t.Errorf("TxSent = %d, want 1000", got.TxSent)
	}
	if got.TxConfirmed != 950 {
		t.Errorf("TxConfirmed = %d, want 950", got.TxConfirmed)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want 'completed'", got.Status)
	}
	if got.LatencyStats == nil {
		t.Error("expected LatencyStats to be non-nil")
	} else if got.LatencyStats.P50 != 120 {
		t.Errorf("LatencyStats.P50 = %f, want 120", got.LatencyStats.P50)
	}
}

func TestCompleteTestRun(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial test run
	run := &TestRun{
		ID:              "test-complete",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "running",
		ExecutionLayer:  "reth",
	}
	err := storage.CreateTestRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Complete the test run
	run.TxSent = 3000
	run.TxConfirmed = 2900
	run.TxFailed = 80
	run.TxDiscarded = 20
	run.Status = "completed"
	run.BlockCount = 120
	run.TotalGasUsed = 2520000000
	run.AvgFillRate = 84.5
	run.PeakMgasPerSec = 15.2
	run.AvgMgasPerSec = 12.1
	run.OnChainFirstBlock = 100
	run.OnChainLastBlock = 220
	run.OnChainTxCount = 2900
	run.OnChainGasUsed = 2520000000
	run.Environment = &EnvironmentSnapshot{
		BuilderBlockTimeMs:    250,
		BuilderGasLimit:       30000000,
		BuilderMaxTxsPerBlock: 25000,
		BuilderTxOrdering:     "fifo",
	}

	err = storage.CompleteTestRun(ctx, "test-complete", run)
	if err != nil {
		t.Fatalf("CompleteTestRun failed: %v", err)
	}

	// Verify completion
	got, err := storage.GetTestRun(ctx, "test-complete")
	if err != nil {
		t.Fatalf("GetTestRun failed: %v", err)
	}

	if got.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
	if got.TxDiscarded != 20 {
		t.Errorf("TxDiscarded = %d, want 20", got.TxDiscarded)
	}
	if got.BlockCount != 120 {
		t.Errorf("BlockCount = %d, want 120", got.BlockCount)
	}
	if got.PeakMgasPerSec != 15.2 {
		t.Errorf("PeakMgasPerSec = %f, want 15.2", got.PeakMgasPerSec)
	}
	if got.Environment == nil {
		t.Error("expected Environment to be non-nil")
	} else if got.Environment.BuilderBlockTimeMs != 250 {
		t.Errorf("Environment.BuilderBlockTimeMs = %d, want 250", got.Environment.BuilderBlockTimeMs)
	}
}

func TestListTestRuns(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple test runs
	for i := 0; i < 5; i++ {
		run := &TestRun{
			ID:              "test-list-" + string(rune('A'+i)),
			StartedAt:       time.Now().Add(time.Duration(i) * time.Minute),
			Pattern:         types.PatternConstant,
			TransactionType: types.TxTypeEthTransfer,
			DurationMs:      30000,
			Config:          &types.StartTestRequest{},
			Status:          "completed",
			ExecutionLayer:  "reth",
		}
		if err := storage.CreateTestRun(ctx, run); err != nil {
			t.Fatalf("CreateTestRun failed: %v", err)
		}
	}

	// List all
	result, err := storage.ListTestRuns(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListTestRuns failed: %v", err)
	}
	if result.Total != 5 {
		t.Errorf("Total = %d, want 5", result.Total)
	}
	if len(result.Runs) != 5 {
		t.Errorf("len(Runs) = %d, want 5", len(result.Runs))
	}

	// List with pagination
	result, err = storage.ListTestRuns(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListTestRuns failed: %v", err)
	}
	if result.Total != 5 {
		t.Errorf("Total = %d, want 5", result.Total)
	}
	if len(result.Runs) != 2 {
		t.Errorf("len(Runs) = %d, want 2", len(result.Runs))
	}

	// List with offset
	result, err = storage.ListTestRuns(ctx, 2, 3)
	if err != nil {
		t.Fatalf("ListTestRuns failed: %v", err)
	}
	if len(result.Runs) != 2 {
		t.Errorf("len(Runs) = %d, want 2", len(result.Runs))
	}
}

func TestDeleteTestRun(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test run
	run := &TestRun{
		ID:              "test-delete",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "completed",
		ExecutionLayer:  "reth",
	}
	err := storage.CreateTestRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Delete test run
	err = storage.DeleteTestRun(ctx, "test-delete")
	if err != nil {
		t.Fatalf("DeleteTestRun failed: %v", err)
	}

	// Verify deletion
	got, err := storage.GetTestRun(ctx, "test-delete")
	if err != nil {
		t.Fatalf("GetTestRun failed: %v", err)
	}
	if got != nil {
		t.Error("expected test run to be deleted")
	}
}

func TestUpdateTestRunMetadata(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test run
	run := &TestRun{
		ID:              "test-metadata",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "completed",
		ExecutionLayer:  "reth",
	}
	err := storage.CreateTestRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Update custom name
	customName := "My Test Run"
	err = storage.UpdateTestRunMetadata(ctx, "test-metadata", &TestRunMetadataUpdate{
		CustomName: &customName,
	})
	if err != nil {
		t.Fatalf("UpdateTestRunMetadata failed: %v", err)
	}

	// Verify
	got, _ := storage.GetTestRun(ctx, "test-metadata")
	if got.CustomName == nil || *got.CustomName != customName {
		t.Errorf("CustomName = %v, want %q", got.CustomName, customName)
	}

	// Update favorite
	isFavorite := true
	err = storage.UpdateTestRunMetadata(ctx, "test-metadata", &TestRunMetadataUpdate{
		IsFavorite: &isFavorite,
	})
	if err != nil {
		t.Fatalf("UpdateTestRunMetadata failed: %v", err)
	}

	// Verify
	got, _ = storage.GetTestRun(ctx, "test-metadata")
	if !got.IsFavorite {
		t.Error("expected IsFavorite to be true")
	}
}

func TestUpdateTestRunMetadata_NotFound(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	customName := "test"
	err := storage.UpdateTestRunMetadata(ctx, "nonexistent", &TestRunMetadataUpdate{
		CustomName: &customName,
	})
	if err == nil {
		t.Error("expected error for nonexistent test run")
	}
}

func TestUpdateTestRunMetadata_NoUpdate(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	// No fields to update should succeed silently
	err := storage.UpdateTestRunMetadata(ctx, "any-id", &TestRunMetadataUpdate{})
	if err != nil {
		t.Errorf("expected no error for empty update, got: %v", err)
	}
}

func TestBulkInsertAndGetTimeSeries(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test run first
	run := &TestRun{
		ID:              "test-ts",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "running",
		ExecutionLayer:  "reth",
	}
	if err := storage.CreateTestRun(ctx, run); err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Insert time series points
	points := []TimeSeriesPoint{
		{TimestampMs: 0, TxSent: 0, TxConfirmed: 0, CurrentTPS: 0, TargetTPS: 100},
		{TimestampMs: 1000, TxSent: 100, TxConfirmed: 95, CurrentTPS: 100, TargetTPS: 100, GasUsed: 2100000, MgasPerSec: 2.1},
		{TimestampMs: 2000, TxSent: 200, TxConfirmed: 190, CurrentTPS: 100, TargetTPS: 100, GasUsed: 2100000, MgasPerSec: 2.1},
	}

	err := storage.BulkInsertTimeSeries(ctx, "test-ts", points)
	if err != nil {
		t.Fatalf("BulkInsertTimeSeries failed: %v", err)
	}

	// Get time series
	got, err := storage.GetTimeSeries(ctx, "test-ts")
	if err != nil {
		t.Fatalf("GetTimeSeries failed: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("len(TimeSeries) = %d, want 3", len(got))
	}
	if got[1].TxSent != 100 {
		t.Errorf("TimeSeries[1].TxSent = %d, want 100", got[1].TxSent)
	}
	if got[2].MgasPerSec != 2.1 {
		t.Errorf("TimeSeries[2].MgasPerSec = %f, want 2.1", got[2].MgasPerSec)
	}
}

func TestBulkInsertTimeSeries_Empty(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.BulkInsertTimeSeries(ctx, "any-id", []TimeSeriesPoint{})
	if err != nil {
		t.Errorf("expected no error for empty points, got: %v", err)
	}
}

func TestBulkInsertAndGetTxLogs(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test run first
	run := &TestRun{
		ID:              "test-txlogs",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "running",
		ExecutionLayer:  "reth",
	}
	if err := storage.CreateTestRun(ctx, run); err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Insert tx logs
	logs := []TxLogEntry{
		{TxHash: "0xabc1", SentAtMs: 1000, Status: "confirmed", ConfirmedAtMs: 1100, ConfirmLatencyMs: 100, FromAccount: 0, Nonce: 0},
		{TxHash: "0xabc2", SentAtMs: 1001, Status: "pending", FromAccount: 1, Nonce: 0},
		{TxHash: "0xabc3", SentAtMs: 1002, Status: "failed", ErrorReason: "nonce too low", FromAccount: 2, Nonce: 0},
	}

	err := storage.BulkInsertTxLogs(ctx, "test-txlogs", logs)
	if err != nil {
		t.Fatalf("BulkInsertTxLogs failed: %v", err)
	}

	// Get tx logs with pagination
	result, err := storage.GetTxLogs(ctx, "test-txlogs", 10, 0)
	if err != nil {
		t.Fatalf("GetTxLogs failed: %v", err)
	}

	if result.Total != 3 {
		t.Errorf("Total = %d, want 3", result.Total)
	}
	if len(result.Transactions) != 3 {
		t.Fatalf("len(Transactions) = %d, want 3", len(result.Transactions))
	}
	if result.Transactions[0].TxHash != "0xabc1" {
		t.Errorf("Transactions[0].TxHash = %q, want '0xabc1'", result.Transactions[0].TxHash)
	}
	if result.Transactions[2].ErrorReason != "nonce too low" {
		t.Errorf("Transactions[2].ErrorReason = %q, want 'nonce too low'", result.Transactions[2].ErrorReason)
	}
}

func TestBulkInsertTxLogs_Empty(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	err := storage.BulkInsertTxLogs(ctx, "any-id", []TxLogEntry{})
	if err != nil {
		t.Errorf("expected no error for empty logs, got: %v", err)
	}
}

func TestGetTxLogByHash(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test run and insert log
	run := &TestRun{
		ID:              "test-hash",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "running",
		ExecutionLayer:  "reth",
	}
	if err := storage.CreateTestRun(ctx, run); err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	logs := []TxLogEntry{
		{TxHash: "0xfind-me", SentAtMs: 1000, Status: "confirmed", FromAccount: 0, Nonce: 42, GasTipGwei: 1.5},
	}
	if err := storage.BulkInsertTxLogs(ctx, "test-hash", logs); err != nil {
		t.Fatalf("BulkInsertTxLogs failed: %v", err)
	}

	// Find by hash
	got, err := storage.GetTxLogByHash(ctx, "0xfind-me")
	if err != nil {
		t.Fatalf("GetTxLogByHash failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected to find tx log")
	}
	if got.Nonce != 42 {
		t.Errorf("Nonce = %d, want 42", got.Nonce)
	}
	if got.GasTipGwei != 1.5 {
		t.Errorf("GasTipGwei = %f, want 1.5", got.GasTipGwei)
	}
}

func TestGetTxLogByHash_NotFound(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	got, err := storage.GetTxLogByHash(ctx, "0xnonexistent")
	if err != nil {
		t.Fatalf("GetTxLogByHash failed: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent hash, got %+v", got)
	}
}

func TestColumnExists(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	// Check existing column
	if !storage.columnExists("test_runs", "id") {
		t.Error("expected 'id' column to exist in test_runs")
	}

	// Check non-existing column
	if storage.columnExists("test_runs", "nonexistent_column") {
		t.Error("expected 'nonexistent_column' to not exist")
	}

	// Check non-existing table
	if storage.columnExists("nonexistent_table", "id") {
		t.Error("expected query to return false for nonexistent table")
	}
}

func TestCascadeDelete(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test run with related data
	run := &TestRun{
		ID:              "test-cascade",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "running",
		ExecutionLayer:  "reth",
	}
	if err := storage.CreateTestRun(ctx, run); err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Insert related time series
	points := []TimeSeriesPoint{{TimestampMs: 0, TxSent: 0}}
	if err := storage.BulkInsertTimeSeries(ctx, "test-cascade", points); err != nil {
		t.Fatalf("BulkInsertTimeSeries failed: %v", err)
	}

	// Insert related tx logs
	logs := []TxLogEntry{{TxHash: "0xcascade", SentAtMs: 1000, Status: "pending", FromAccount: 0, Nonce: 0}}
	if err := storage.BulkInsertTxLogs(ctx, "test-cascade", logs); err != nil {
		t.Fatalf("BulkInsertTxLogs failed: %v", err)
	}

	// Verify data exists
	ts, _ := storage.GetTimeSeries(ctx, "test-cascade")
	if len(ts) == 0 {
		t.Fatal("expected time series to exist before delete")
	}

	// Delete test run (should cascade)
	if err := storage.DeleteTestRun(ctx, "test-cascade"); err != nil {
		t.Fatalf("DeleteTestRun failed: %v", err)
	}

	// Verify cascade - time series should be deleted
	ts, _ = storage.GetTimeSeries(ctx, "test-cascade")
	if len(ts) != 0 {
		t.Errorf("expected time series to be deleted, got %d points", len(ts))
	}

	// Verify cascade - tx logs should be deleted
	txResult, _ := storage.GetTxLogs(ctx, "test-cascade", 10, 0)
	if txResult.Total != 0 {
		t.Errorf("expected tx logs to be deleted, got %d logs", txResult.Total)
	}
}

func TestFavoritesSortFirst(t *testing.T) {
	storage, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()

	// Create test runs - one older but favorited
	older := &TestRun{
		ID:              "older",
		StartedAt:       time.Now().Add(-time.Hour),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "completed",
		ExecutionLayer:  "reth",
	}
	newer := &TestRun{
		ID:              "newer",
		StartedAt:       time.Now(),
		Pattern:         types.PatternConstant,
		TransactionType: types.TxTypeEthTransfer,
		DurationMs:      30000,
		Config:          &types.StartTestRequest{},
		Status:          "completed",
		ExecutionLayer:  "reth",
	}

	if err := storage.CreateTestRun(ctx, older); err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}
	if err := storage.CreateTestRun(ctx, newer); err != nil {
		t.Fatalf("CreateTestRun failed: %v", err)
	}

	// Mark older as favorite
	isFavorite := true
	if err := storage.UpdateTestRunMetadata(ctx, "older", &TestRunMetadataUpdate{IsFavorite: &isFavorite}); err != nil {
		t.Fatalf("UpdateTestRunMetadata failed: %v", err)
	}

	// List - favorite (older) should come first despite being older
	result, err := storage.ListTestRuns(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListTestRuns failed: %v", err)
	}

	if len(result.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(result.Runs))
	}
	if result.Runs[0].ID != "older" {
		t.Errorf("expected favorited 'older' to come first, got %q", result.Runs[0].ID)
	}
}
