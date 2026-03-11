package loadgen

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestGetMetrics_Idle(t *testing.T) {
	lg := newTestLoadGenerator(t)

	m := lg.GetMetrics()
	if m.Status != types.StatusIdle {
		t.Errorf("expected status %q, got %q", types.StatusIdle, m.Status)
	}
	if m.ElapsedMs != 0 {
		t.Errorf("expected elapsedMs 0, got %d", m.ElapsedMs)
	}
	if m.DurationMs != 0 {
		t.Errorf("expected durationMs 0, got %d", m.DurationMs)
	}
	if m.Error != "" {
		t.Errorf("expected no error, got %q", m.Error)
	}
}

func TestGetMetrics_Running(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	atomic.StoreInt64(&lg.currentRate, 5000)
	atomic.StoreInt64(&lg.peakRate, 8000)

	lg.builderPressureMu.Lock()
	lg.latestBaseFeeGwei = 1.5
	lg.latestGasPriceGwei = 2.0
	lg.latestGasUsed = 21000000
	lg.blockAttestationEnabled = true
	lg.hsmProvider = "aws-kms"
	lg.hsmKeyIDActive = "key-123"
	lg.hsmFailoverEnabled = true
	lg.builderPressureMu.Unlock()

	lg.blockMetricsMu.Lock()
	lg.cumulativeGasUsed = 100_000_000
	lg.cumulativeGasLimit = 200_000_000
	lg.peakMgasPerSec = 500.0
	lg.totalBlockCount = 10
	lg.blockMetricsMu.Unlock()

	m := lg.GetMetrics()
	if m.Status != types.StatusRunning {
		t.Errorf("expected running, got %q", m.Status)
	}
	if m.TargetTPS != 5000 {
		t.Errorf("expected targetTPS 5000, got %d", m.TargetTPS)
	}
	if m.PeakTPS != 8000 {
		t.Errorf("expected peakTPS 8000, got %d", m.PeakTPS)
	}
	if m.LatestBaseFeeGwei != 1.5 {
		t.Errorf("expected baseFee 1.5, got %f", m.LatestBaseFeeGwei)
	}
	if m.LatestGasPriceGwei != 2.0 {
		t.Errorf("expected gasPrice 2.0, got %f", m.LatestGasPriceGwei)
	}
	if m.LatestGasUsed != 21000000 {
		t.Errorf("expected gasUsed 21000000, got %d", m.LatestGasUsed)
	}
	if m.TotalGasUsed != 100_000_000 {
		t.Errorf("expected totalGasUsed 100000000, got %d", m.TotalGasUsed)
	}
	if m.BlockCount != 10 {
		t.Errorf("expected blockCount 10, got %d", m.BlockCount)
	}
	if m.PeakMgasPerSec != 500.0 {
		t.Errorf("expected peakMgas 500, got %f", m.PeakMgasPerSec)
	}
	if !m.BlockAttestationEnabled {
		t.Error("expected blockAttestationEnabled true")
	}
	if m.HSMProvider != "aws-kms" {
		t.Errorf("expected hsmProvider aws-kms, got %q", m.HSMProvider)
	}
	if m.HSMKeyIDActive != "key-123" {
		t.Errorf("expected hsmKeyIDActive key-123, got %q", m.HSMKeyIDActive)
	}
	if !m.HSMFailoverEnabled {
		t.Error("expected hsmFailoverEnabled true")
	}
}

func TestGetMetrics_WithError(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.statusMu.Lock()
	lg.status = types.StatusError
	lg.testError = "something broke"
	lg.statusMu.Unlock()

	m := lg.GetMetrics()
	if m.Status != types.StatusError {
		t.Errorf("expected error status, got %q", m.Status)
	}
	if m.Error != "something broke" {
		t.Errorf("expected error msg, got %q", m.Error)
	}
}

func TestGetMetrics_Warnings(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.warningsMu.Lock()
	lg.warnings = []string{"low balance", "high latency"}
	lg.warningsMu.Unlock()

	m := lg.GetMetrics()
	if len(m.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(m.Warnings))
	}
	if m.Warnings[0] != "low balance" || m.Warnings[1] != "high latency" {
		t.Errorf("unexpected warnings: %v", m.Warnings)
	}
}

func TestGetMetrics_InitializingPhase(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.statusMu.Lock()
	lg.status = types.StatusInitializing
	lg.statusMu.Unlock()
	lg.initPhase = types.InitPhaseFundingAccts
	lg.initProgress = "Funding 50/100"
	lg.initAccountsTotal = 100
	lg.initAccountsGen = 100
	lg.initFundingSent = 50
	lg.initFundingTotal = 100

	m := lg.GetMetrics()
	if m.InitPhase != types.InitPhaseFundingAccts {
		t.Errorf("expected init phase funding, got %q", m.InitPhase)
	}
	if m.AccountsTotal != 100 {
		t.Errorf("expected accountsTotal 100, got %d", m.AccountsTotal)
	}
	if m.FundingTxsSent != 50 {
		t.Errorf("expected fundingSent 50, got %d", m.FundingTxsSent)
	}
}

func TestGetMetrics_VerifyingPhase(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.statusMu.Lock()
	lg.status = types.StatusVerifying
	lg.verifyPhase = types.VerifyPhaseTxCount
	lg.verifyProgress = "Counting TXs"
	lg.blocksToVerify = 50
	lg.blocksVerified = 25
	lg.receiptsToSample = 100
	lg.receiptsSampled = 40
	lg.statusMu.Unlock()

	m := lg.GetMetrics()
	if m.VerifyPhase != types.VerifyPhaseTxCount {
		t.Errorf("expected verify phase tx_count, got %q", m.VerifyPhase)
	}
	if m.BlocksToVerify != 50 {
		t.Errorf("expected blocksToVerify 50, got %d", m.BlocksToVerify)
	}
	if m.BlocksVerified != 25 {
		t.Errorf("expected blocksVerified 25, got %d", m.BlocksVerified)
	}
}

func TestStartTest_AlreadyRunning(t *testing.T) {
	tests := []struct {
		name   string
		status types.TestStatus
	}{
		{"running", types.StatusRunning},
		{"initializing", types.StatusInitializing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lg := newTestLoadGenerator(t)
			lg.statusMu.Lock()
			lg.status = tt.status
			lg.statusMu.Unlock()

			err := lg.StartTest(types.StartTestRequest{
				Pattern:     types.PatternConstant,
				DurationSec: 10,
			})
			if err == nil {
				t.Fatal("expected error for already running/initializing")
			}
		})
	}
}

func TestStartTest_InvalidRealisticConfig(t *testing.T) {
	lg := newTestLoadGenerator(t)

	err := lg.StartTest(types.StartTestRequest{
		Pattern:     types.PatternRealistic,
		DurationSec: 10,
		RealisticConfig: &types.RealisticTestConfig{
			NumAccounts: 10,
			TargetTPS:   100,
			TxTypeRatios: types.TxTypeRatio{
				EthTransfer: 50,
				// Sum != 100
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid realistic config")
	}
}

func TestStartTest_ValidRealisticConfig(t *testing.T) {
	lg := newTestLoadGenerator(t)

	err := lg.StartTest(types.StartTestRequest{
		Pattern:     types.PatternRealistic,
		DurationSec: 10,
		RealisticConfig: &types.RealisticTestConfig{
			NumAccounts: 10,
			TargetTPS:   100,
			TxTypeRatios: types.TxTypeRatio{
				EthTransfer: 100,
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Status should be initializing (background init started)
	lg.statusMu.RLock()
	status := lg.status
	lg.statusMu.RUnlock()
	if status != types.StatusInitializing {
		t.Errorf("expected initializing, got %q", status)
	}
}

func TestGetHistoryPaginated(t *testing.T) {
	t.Run("nil storage returns empty", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.storage = nil

		result, err := lg.GetHistoryPaginated(10, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Total != 0 {
			t.Errorf("expected total 0, got %d", result.Total)
		}
		if len(result.Runs) != 0 {
			t.Errorf("expected empty runs, got %d", len(result.Runs))
		}
		if result.Limit != 10 {
			t.Errorf("expected limit 10, got %d", result.Limit)
		}
	})

	t.Run("delegates to storage", func(t *testing.T) {
		ms := &mockStorage{
			ListTestRunsFn: func(_ context.Context, limit, offset int) (*storage.PaginatedTestRuns, error) {
				return &storage.PaginatedTestRuns{
					Runs:   []storage.TestRun{{ID: "run-1"}},
					Total:  1,
					Limit:  limit,
					Offset: offset,
				}, nil
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		result, err := lg.GetHistoryPaginated(20, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Total != 1 {
			t.Errorf("expected total 1, got %d", result.Total)
		}
		if result.Runs[0].ID != "run-1" {
			t.Errorf("expected run-1, got %q", result.Runs[0].ID)
		}
		if result.Limit != 20 || result.Offset != 5 {
			t.Errorf("expected limit=20 offset=5, got %d/%d", result.Limit, result.Offset)
		}
	})

	t.Run("propagates storage error", func(t *testing.T) {
		ms := &mockStorage{
			ListTestRunsFn: func(_ context.Context, _, _ int) (*storage.PaginatedTestRuns, error) {
				return nil, fmt.Errorf("db error")
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		_, err := lg.GetHistoryPaginated(10, 0)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestGetTestRunDetail(t *testing.T) {
	t.Run("nil storage returns nil", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.storage = nil

		detail, err := lg.GetTestRunDetail("abc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if detail != nil {
			t.Error("expected nil detail")
		}
	})

	t.Run("not found returns nil", func(t *testing.T) {
		ms := &mockStorage{
			GetTestRunFn: func(_ context.Context, _ string) (*storage.TestRun, error) {
				return nil, nil
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		detail, err := lg.GetTestRunDetail("nonexistent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if detail != nil {
			t.Error("expected nil detail for not found")
		}
	})

	t.Run("assembles run and timeSeries", func(t *testing.T) {
		run := &storage.TestRun{ID: "run-42", TxSent: 1000}
		ts := []storage.TimeSeriesPoint{
			{TimestampMs: 100, TxSent: 50},
			{TimestampMs: 200, TxSent: 100},
		}
		ms := &mockStorage{
			GetTestRunFn: func(_ context.Context, id string) (*storage.TestRun, error) {
				if id == "run-42" {
					return run, nil
				}
				return nil, nil
			},
			GetTimeSeriesFn: func(_ context.Context, id string) ([]storage.TimeSeriesPoint, error) {
				if id == "run-42" {
					return ts, nil
				}
				return nil, nil
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		detail, err := lg.GetTestRunDetail("run-42")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if detail == nil {
			t.Fatal("expected non-nil detail")
		}
		if detail.Run.ID != "run-42" {
			t.Errorf("expected run ID run-42, got %q", detail.Run.ID)
		}
		if len(detail.TimeSeries) != 2 {
			t.Errorf("expected 2 time series points, got %d", len(detail.TimeSeries))
		}
	})

	t.Run("GetTestRun error propagates", func(t *testing.T) {
		ms := &mockStorage{
			GetTestRunFn: func(_ context.Context, _ string) (*storage.TestRun, error) {
				return nil, fmt.Errorf("db read error")
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		_, err := lg.GetTestRunDetail("x")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("GetTimeSeries error propagates", func(t *testing.T) {
		ms := &mockStorage{
			GetTestRunFn: func(_ context.Context, _ string) (*storage.TestRun, error) {
				return &storage.TestRun{ID: "x"}, nil
			},
			GetTimeSeriesFn: func(_ context.Context, _ string) ([]storage.TimeSeriesPoint, error) {
				return nil, fmt.Errorf("ts error")
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		_, err := lg.GetTestRunDetail("x")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestGetTestRunTransactions(t *testing.T) {
	t.Run("nil storage returns empty", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.storage = nil

		result, err := lg.GetTestRunTransactions("abc", 50, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Transactions) != 0 {
			t.Errorf("expected empty transactions, got %d", len(result.Transactions))
		}
		if result.Limit != 50 {
			t.Errorf("expected limit 50, got %d", result.Limit)
		}
	})

	t.Run("delegates to storage", func(t *testing.T) {
		ms := &mockStorage{
			GetTxLogsFn: func(_ context.Context, id string, limit, offset int) (*storage.PaginatedTxLogs, error) {
				return &storage.PaginatedTxLogs{
					Transactions: []storage.TxLogEntry{{TxHash: "0xabc"}},
					Total:        1,
					Limit:        limit,
					Offset:       offset,
				}, nil
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		result, err := lg.GetTestRunTransactions("run-1", 25, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Transactions[0].TxHash != "0xabc" {
			t.Errorf("expected tx hash 0xabc, got %q", result.Transactions[0].TxHash)
		}
	})
}

func TestDeleteTestRun(t *testing.T) {
	t.Run("nil storage returns nil", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.storage = nil

		if err := lg.DeleteTestRun("abc"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("delegates to storage", func(t *testing.T) {
		var deletedID string
		ms := &mockStorage{
			DeleteTestRunFn: func(_ context.Context, id string) error {
				deletedID = id
				return nil
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		if err := lg.DeleteTestRun("run-99"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deletedID != "run-99" {
			t.Errorf("expected deleted ID run-99, got %q", deletedID)
		}
	})

	t.Run("propagates error", func(t *testing.T) {
		ms := &mockStorage{
			DeleteTestRunFn: func(_ context.Context, _ string) error {
				return fmt.Errorf("delete failed")
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		if err := lg.DeleteTestRun("x"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestUpdateTestRunMetadata(t *testing.T) {
	t.Run("nil storage returns nil", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.storage = nil

		err := lg.UpdateTestRunMetadata("abc", &storage.TestRunMetadataUpdate{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("delegates to storage", func(t *testing.T) {
		var capturedID string
		var capturedUpdate *storage.TestRunMetadataUpdate
		name := "my test"
		ms := &mockStorage{
			UpdateTestRunMetadataFn: func(_ context.Context, id string, update *storage.TestRunMetadataUpdate) error {
				capturedID = id
				capturedUpdate = update
				return nil
			},
		}
		lg := newTestLoadGenerator(t)
		lg.storage = ms

		update := &storage.TestRunMetadataUpdate{CustomName: &name}
		if err := lg.UpdateTestRunMetadata("run-5", update); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if capturedID != "run-5" {
			t.Errorf("expected ID run-5, got %q", capturedID)
		}
		if capturedUpdate.CustomName == nil || *capturedUpdate.CustomName != "my test" {
			t.Error("expected custom name to be passed through")
		}
	})
}

func TestRecycleFunds(t *testing.T) {
	t.Run("error when running", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.statusMu.Lock()
		lg.status = types.StatusRunning
		lg.statusMu.Unlock()

		_, err := lg.RecycleFunds()
		if err == nil {
			t.Fatal("expected error when running")
		}
	})

	t.Run("delegates to account manager", func(t *testing.T) {
		am := &mockAccountManager{
			RecycleFundsFn: func(_ context.Context, _ rpc.Client) (int, error) {
				return 42, nil
			},
		}
		lg := newTestLoadGenerator(t, WithAccountManager(am))

		count, err := lg.RecycleFunds()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 42 {
			t.Errorf("expected 42 recycled, got %d", count)
		}
	})

	t.Run("propagates error from account manager", func(t *testing.T) {
		am := &mockAccountManager{
			RecycleFundsFn: func(_ context.Context, _ rpc.Client) (int, error) {
				return 0, fmt.Errorf("recycle failed")
			},
		}
		lg := newTestLoadGenerator(t, WithAccountManager(am))

		_, err := lg.RecycleFunds()
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestReset(t *testing.T) {
	t.Run("resets to idle", func(t *testing.T) {
		lg := newTestLoadGenerator(t)

		lg.statusMu.Lock()
		lg.status = types.StatusCompleted
		lg.testError = "old error"
		lg.statusMu.Unlock()

		atomic.StoreInt64(&lg.currentRate, 5000)
		atomic.StoreInt64(&lg.peakRate, 9000)

		lg.warningsMu.Lock()
		lg.warnings = []string{"old warning"}
		lg.warningsMu.Unlock()

		lg.Reset()

		lg.statusMu.RLock()
		status := lg.status
		testError := lg.testError
		lg.statusMu.RUnlock()

		if status != types.StatusIdle {
			t.Errorf("expected idle, got %q", status)
		}
		if testError != "" {
			t.Errorf("expected empty error, got %q", testError)
		}
		if atomic.LoadInt64(&lg.currentRate) != 0 {
			t.Error("expected currentRate reset to 0")
		}
		if atomic.LoadInt64(&lg.peakRate) != 0 {
			t.Error("expected peakRate reset to 0")
		}
		lg.warningsMu.RLock()
		if len(lg.warnings) != 0 {
			t.Errorf("expected warnings cleared, got %d", len(lg.warnings))
		}
		lg.warningsMu.RUnlock()
	})

	t.Run("no-op when running", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.statusMu.Lock()
		lg.status = types.StatusRunning
		lg.statusMu.Unlock()

		lg.Reset()

		lg.statusMu.RLock()
		status := lg.status
		lg.statusMu.RUnlock()
		if status != types.StatusRunning {
			t.Errorf("expected still running, got %q", status)
		}
	})

	t.Run("resets block metrics", func(t *testing.T) {
		lg := newTestLoadGenerator(t)

		lg.statusMu.Lock()
		lg.status = types.StatusCompleted
		lg.statusMu.Unlock()

		lg.blockMetricsMu.Lock()
		lg.cumulativeGasUsed = 999
		lg.cumulativeGasLimit = 888
		lg.peakMgasPerSec = 777.0
		lg.totalBlockCount = 50
		lg.blockMetricsMu.Unlock()

		lg.builderPressureMu.Lock()
		lg.latestBaseFeeGwei = 5.0
		lg.latestGasPriceGwei = 10.0
		lg.latestGasUsed = 42000
		lg.builderPressureMu.Unlock()

		lg.Reset()

		lg.blockMetricsMu.Lock()
		if lg.cumulativeGasUsed != 0 {
			t.Errorf("expected cumulativeGasUsed 0, got %d", lg.cumulativeGasUsed)
		}
		if lg.peakMgasPerSec != 0 {
			t.Errorf("expected peakMgas 0, got %f", lg.peakMgasPerSec)
		}
		if lg.totalBlockCount != 0 {
			t.Errorf("expected totalBlockCount 0, got %d", lg.totalBlockCount)
		}
		lg.blockMetricsMu.Unlock()

		lg.builderPressureMu.RLock()
		if lg.latestBaseFeeGwei != 0 {
			t.Errorf("expected baseFee 0, got %f", lg.latestBaseFeeGwei)
		}
		lg.builderPressureMu.RUnlock()
	})
}

func TestGetHistory(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.testHistoryMu.Lock()
	lg.testHistory = []types.TestResult{
		{ID: "a", TxSent: 100},
		{ID: "b", TxSent: 200},
	}
	lg.testHistoryMu.Unlock()

	h := lg.GetHistory()
	if len(h) != 2 {
		t.Fatalf("expected 2 results, got %d", len(h))
	}
	if h[0].ID != "a" || h[1].ID != "b" {
		t.Errorf("unexpected history IDs: %q, %q", h[0].ID, h[1].ID)
	}

	// Verify it returns a copy (modifying result doesn't affect internal state)
	h[0].ID = "modified"
	lg.testHistoryMu.RLock()
	if lg.testHistory[0].ID != "a" {
		t.Error("GetHistory should return a copy")
	}
	lg.testHistoryMu.RUnlock()
}

func TestCheckL2RPC(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := &mockRPCClient{
			GetBlockNumberFn: func(_ context.Context) (uint64, error) {
				return 100, nil
			},
		}
		lg := newTestLoadGenerator(t, WithL2Client(client))
		if err := lg.CheckL2RPC(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		client := &mockRPCClient{
			GetBlockNumberFn: func(_ context.Context) (uint64, error) {
				return 0, fmt.Errorf("connection refused")
			},
		}
		lg := newTestLoadGenerator(t, WithL2Client(client))
		if err := lg.CheckL2RPC(); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCheckBuilderRPC(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := &mockRPCClient{
			GetBlockNumberFn: func(_ context.Context) (uint64, error) {
				return 100, nil
			},
		}
		lg := newTestLoadGenerator(t, WithBuilderClient(client))
		if err := lg.CheckBuilderRPC(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		client := &mockRPCClient{
			GetBlockNumberFn: func(_ context.Context) (uint64, error) {
				return 0, fmt.Errorf("timeout")
			},
		}
		lg := newTestLoadGenerator(t, WithBuilderClient(client))
		if err := lg.CheckBuilderRPC(); err == nil {
			t.Fatal("expected error")
		}
	})
}
