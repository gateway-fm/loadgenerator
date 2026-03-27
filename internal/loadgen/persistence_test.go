package loadgen

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestShouldLogTransactions(t *testing.T) {
	lg := newTestLoadGenerator(t)

	tests := []struct {
		name     string
		duration time.Duration
		tps      int
		want     bool
	}{
		{"short low tps", 10 * time.Second, 100, true},
		{"medium duration", 60 * time.Second, 5000, true},
		{"long high tps exceeds threshold", 600 * time.Second, 10000, false},
		{"exactly at threshold", time.Duration(maxTxLogMemory/txLogEntrySize) * time.Second, 1, true},
		{"zero duration", 0, 10000, true},
		{"zero tps", 60 * time.Second, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lg.shouldLogTransactions(tt.duration, tt.tps)
			if got != tt.want {
				estimatedMem := int(tt.duration.Seconds()) * tt.tps * txLogEntrySize
				t.Errorf("shouldLogTransactions(%v, %d) = %v, want %v (estimatedMem=%d, max=%d)",
					tt.duration, tt.tps, got, tt.want, estimatedMem, maxTxLogMemory)
			}
		})
	}
}

func TestInitBuffers_TimeSeriesCapacity(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = false

	lg.initBuffers(30*time.Second, 1000)

	expectedCap := 30*5 + 10
	if cap(lg.timeSeriesBuf) != expectedCap {
		t.Errorf("timeSeriesBuf cap = %d, want %d", cap(lg.timeSeriesBuf), expectedCap)
	}
	if len(lg.timeSeriesBuf) != 0 {
		t.Errorf("timeSeriesBuf len = %d, want 0", len(lg.timeSeriesBuf))
	}
	if lg.txLogBuf != nil {
		t.Error("txLogBuf should be nil when logging disabled")
	}
}

func TestInitBuffers_TxLogCapacity(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true

	lg.initBuffers(10*time.Second, 500)

	expectedTxCap := 10*500 + 1000
	if cap(lg.txLogBuf) != expectedTxCap {
		t.Errorf("txLogBuf cap = %d, want %d", cap(lg.txLogBuf), expectedTxCap)
	}
	if len(lg.txLogBuf) != 0 {
		t.Errorf("txLogBuf len = %d, want 0", len(lg.txLogBuf))
	}
}

func TestInitBuffers_ClearsPendingTxs(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = false

	lg.pendingTxs.Store(common.Hash{1}, &storage.TxLogEntry{TxHash: "0x01"})
	lg.pendingTxs.Store(common.Hash{2}, &storage.TxLogEntry{TxHash: "0x02"})

	lg.initBuffers(5*time.Second, 100)

	count := 0
	lg.pendingTxs.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("pendingTxs should be cleared after initBuffers, got %d entries", count)
	}
}

func TestRecordTxSent(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true
	lg.txLogBuf = make([]storage.TxLogEntry, 0, 100)
	lg.pendingTxs = sync.Map{}

	txHash := common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	sentAt := time.Now()

	lg.recordTxSent(txHash, sentAt, 3, 42, 1.5)

	if len(lg.txLogBuf) != 1 {
		t.Fatalf("txLogBuf len = %d, want 1", len(lg.txLogBuf))
	}
	entry := lg.txLogBuf[0]
	if entry.Status != "pending" {
		t.Errorf("status = %q, want pending", entry.Status)
	}
	if entry.FromAccount != 3 {
		t.Errorf("fromAccount = %d, want 3", entry.FromAccount)
	}
	if entry.Nonce != 42 {
		t.Errorf("nonce = %d, want 42", entry.Nonce)
	}
	if entry.GasTipGwei != 1.5 {
		t.Errorf("gasTipGwei = %f, want 1.5", entry.GasTipGwei)
	}

	val, ok := lg.pendingTxs.Load(txHash)
	if !ok {
		t.Fatal("expected txHash in pendingTxs")
	}
	pe := val.(*storage.TxLogEntry)
	if pe.TxHash != txHash.Hex() {
		t.Errorf("pendingTxs entry hash = %q, want %q", pe.TxHash, txHash.Hex())
	}
}

func TestRecordTxSent_DisabledLogging(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = false

	lg.recordTxSent(common.Hash{1}, time.Now(), 0, 0, 0)

	count := 0
	lg.pendingTxs.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Error("should not record when logging disabled")
	}
}

func TestRecordTxConfirmed(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true

	txHash := common.Hash{0xAA}
	sentAt := time.Now().Add(-100 * time.Millisecond)
	entry := &storage.TxLogEntry{
		TxHash:   txHash.Hex(),
		SentAtMs: sentAt.UnixMilli(),
		Status:   "pending",
	}
	lg.pendingTxs.Store(txHash, entry)

	confirmedAt := time.Now()
	lg.recordTxConfirmed(txHash, confirmedAt)

	if entry.Status != "confirmed" {
		t.Errorf("status = %q, want confirmed", entry.Status)
	}
	if entry.ConfirmedAtMs != confirmedAt.UnixMilli() {
		t.Errorf("confirmedAtMs = %d, want %d", entry.ConfirmedAtMs, confirmedAt.UnixMilli())
	}
	expectedLatency := confirmedAt.UnixMilli() - sentAt.UnixMilli()
	if entry.ConfirmLatencyMs != expectedLatency {
		t.Errorf("confirmLatencyMs = %d, want %d", entry.ConfirmLatencyMs, expectedLatency)
	}
}

func TestRecordTxConfirmed_MissingHash(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true

	lg.recordTxConfirmed(common.Hash{0xFF}, time.Now())
}

func TestRecordTxPreconfirmed(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true

	txHash := common.Hash{0xBB}
	sentAt := time.Now().Add(-50 * time.Millisecond)
	entry := &storage.TxLogEntry{
		TxHash:   txHash.Hex(),
		SentAtMs: sentAt.UnixMilli(),
		Status:   "pending",
	}
	lg.pendingTxs.Store(txHash, entry)

	preconfAt := time.Now()
	lg.recordTxPreconfirmed(txHash, preconfAt)

	if entry.PreconfAtMs != preconfAt.UnixMilli() {
		t.Errorf("preconfAtMs = %d, want %d", entry.PreconfAtMs, preconfAt.UnixMilli())
	}
	expectedLatency := preconfAt.UnixMilli() - sentAt.UnixMilli()
	if entry.PreconfLatencyMs != expectedLatency {
		t.Errorf("preconfLatencyMs = %d, want %d", entry.PreconfLatencyMs, expectedLatency)
	}
}

func TestBuildTestRunAccounts(t *testing.T) {
	builtIn := make([]*account.Account, 3)
	dynamic := make([]*account.Account, 5)
	for i := range builtIn {
		builtIn[i] = makeTestAccount(t)
	}
	for i := range dynamic {
		dynamic[i] = makeTestAccount(t)
	}

	mockAccMgr := &mockAccountManager{
		accounts:        builtIn,
		dynamicAccounts: dynamic,
		funded:          4,
	}

	lg := newTestLoadGenerator(t, WithAccountManager(mockAccMgr))
	lg.currentTestID = "test-123"
	lg.startTime = time.Now().Add(-10 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.timeSeriesBuf = nil
	lg.txLoggingEnabled = false

	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, id string, run *storage.TestRun) error {
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if capturedRun == nil {
		t.Fatal("CompleteTestRun was not called")
	}
	accts := capturedRun.TestAccounts
	if accts == nil {
		t.Fatal("TestAccounts is nil")
	}
	if accts.TotalCount != 8 {
		t.Errorf("TotalCount = %d, want 8", accts.TotalCount)
	}
	if accts.DynamicCount != 5 {
		t.Errorf("DynamicCount = %d, want 5", accts.DynamicCount)
	}
	if accts.FundedCount != 4 {
		t.Errorf("FundedCount = %d, want 4", accts.FundedCount)
	}
	if accts.FunderAddress != builtIn[0].Address.Hex() {
		t.Errorf("FunderAddress = %q, want %q", accts.FunderAddress, builtIn[0].Address.Hex())
	}
	if len(accts.AllAccounts) != 8 {
		t.Fatalf("AllAccounts len = %d, want 8", len(accts.AllAccounts))
	}
	if accts.AllAccounts[0].Role != storage.AccountRoleDeployer {
		t.Errorf("account 0 role = %q, want deployer", accts.AllAccounts[0].Role)
	}
	if accts.AllAccounts[1].Role != storage.AccountRoleFunder {
		t.Errorf("account 1 role = %q, want funder", accts.AllAccounts[1].Role)
	}
	if accts.AllAccounts[3].Role != storage.AccountRoleFunded {
		t.Errorf("account 3 role = %q, want funded", accts.AllAccounts[3].Role)
	}
	if len(accts.Accounts) != 8 {
		t.Errorf("legacy Accounts len = %d, want 8", len(accts.Accounts))
	}
}

func TestBuildTestRunAccounts_LegacyCap100(t *testing.T) {
	builtIn := make([]*account.Account, 10)
	dynamic := make([]*account.Account, 95)
	for i := range builtIn {
		builtIn[i] = makeTestAccount(t)
	}
	for i := range dynamic {
		dynamic[i] = makeTestAccount(t)
	}

	mockAccMgr := &mockAccountManager{
		accounts:        builtIn,
		dynamicAccounts: dynamic,
		funded:          90,
	}

	lg := newTestLoadGenerator(t, WithAccountManager(mockAccMgr))
	lg.currentTestID = "test-cap"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false

	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, run *storage.TestRun) error {
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if capturedRun == nil {
		t.Fatal("CompleteTestRun was not called")
	}
	if len(capturedRun.TestAccounts.Accounts) != 100 {
		t.Errorf("legacy Accounts len = %d, want 100 (capped)", len(capturedRun.TestAccounts.Accounts))
	}
	if len(capturedRun.TestAccounts.AllAccounts) != 105 {
		t.Errorf("AllAccounts len = %d, want 105 (uncapped)", len(capturedRun.TestAccounts.AllAccounts))
	}
}

func TestSaveTestResult_CallsCompleteTestRun(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-save-1"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  5,
		ConstantRate: 100,
	}
	lg.currentTxType = types.TxTypeEthTransfer
	lg.txLoggingEnabled = false
	atomic.StoreInt64(&lg.peakRate, 150)

	col := metrics.NewInMemoryCollector(true)
	lg.metricsCol = col

	var completeCalled bool
	var capturedID string
	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, id string, run *storage.TestRun) error {
			completeCalled = true
			capturedID = id
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if !completeCalled {
		t.Fatal("CompleteTestRun was not called")
	}
	if capturedID != "test-save-1" {
		t.Errorf("test ID = %q, want test-save-1", capturedID)
	}
	if capturedRun.Status != "completed" {
		t.Errorf("status = %q, want completed", capturedRun.Status)
	}
	if capturedRun.PeakTPS != 150 {
		t.Errorf("peakTPS = %d, want 150", capturedRun.PeakTPS)
	}
}

func TestSaveTestResult_AddsToTestHistory(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-history"
	lg.startTime = time.Now().Add(-1 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false
	lg.storage = nil

	initialLen := len(lg.testHistory)
	lg.saveTestResult()

	if len(lg.testHistory) != initialLen+1 {
		t.Errorf("testHistory len = %d, want %d", len(lg.testHistory), initialLen+1)
	}
	last := lg.testHistory[len(lg.testHistory)-1]
	if last.ID != "test-history" {
		t.Errorf("last result ID = %q, want test-history", last.ID)
	}
}

func TestSaveTestResult_TestHistoryCap100(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.startTime = time.Now().Add(-1 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false
	lg.storage = nil

	lg.testHistory = make([]types.TestResult, 100)
	for i := range lg.testHistory {
		lg.testHistory[i].ID = "old"
	}

	lg.currentTestID = "new-test"
	lg.saveTestResult()

	if len(lg.testHistory) != 100 {
		t.Errorf("testHistory len = %d, want 100 (capped)", len(lg.testHistory))
	}
	if lg.testHistory[99].ID != "new-test" {
		t.Errorf("last entry ID = %q, want new-test", lg.testHistory[99].ID)
	}
}

func TestPersistTestData_TimeSeriesBulkInsert(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-ts"
	lg.startTime = time.Now().Add(-10 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false

	lg.timeSeriesBuf = []storage.TimeSeriesPoint{
		{TimestampMs: 1000, TxSent: 100, GasUsed: 5_000_000, GasLimit: 30_000_000, BlockCount: 1, MgasPerSec: 5.0, FillRate: 16.6},
		{TimestampMs: 2000, TxSent: 200, GasUsed: 10_000_000, GasLimit: 30_000_000, BlockCount: 2, MgasPerSec: 10.0, FillRate: 33.3},
		{TimestampMs: 3000, TxSent: 300, GasUsed: 8_000_000, GasLimit: 30_000_000, BlockCount: 1, MgasPerSec: 8.0, FillRate: 26.6},
	}

	var tsCalled bool
	var tsTestID string
	var tsPoints []storage.TimeSeriesPoint
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, _ *storage.TestRun) error { return nil },
		BulkInsertTimeSeriesFn: func(_ context.Context, testID string, points []storage.TimeSeriesPoint) error {
			tsCalled = true
			tsTestID = testID
			tsPoints = points
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if !tsCalled {
		t.Fatal("BulkInsertTimeSeries was not called")
	}
	if tsTestID != "test-ts" {
		t.Errorf("testID = %q, want test-ts", tsTestID)
	}
	if len(tsPoints) != 3 {
		t.Errorf("points len = %d, want 3", len(tsPoints))
	}
}

func TestPersistTestData_EmptyTimeSeries(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-empty-ts"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false
	lg.timeSeriesBuf = nil

	tsCalled := false
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, _ *storage.TestRun) error { return nil },
		BulkInsertTimeSeriesFn: func(_ context.Context, _ string, _ []storage.TimeSeriesPoint) error {
			tsCalled = true
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if tsCalled {
		t.Error("BulkInsertTimeSeries should not be called for empty buffer")
	}
}

func TestPersistTestData_TxLogBulkInsert(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-txlog"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = true

	txHash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	entry := &storage.TxLogEntry{
		TxHash:          txHash.Hex(),
		SentAtMs:        time.Now().Add(-3 * time.Second).UnixMilli(),
		Status:          "confirmed",
		ConfirmedAtMs:   time.Now().Add(-2 * time.Second).UnixMilli(),
		ConfirmLatencyMs: 1000,
	}
	lg.pendingTxs.Store(txHash, entry)

	lg.txLogBuf = []storage.TxLogEntry{
		{TxHash: txHash.Hex(), SentAtMs: entry.SentAtMs, Status: "pending"},
	}

	txLogCalled := make(chan struct{}, 1)
	var txLogTestID string
	var txLogEntries []storage.TxLogEntry
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, _ *storage.TestRun) error { return nil },
		BulkInsertTxLogsFn: func(_ context.Context, testID string, logs []storage.TxLogEntry) error {
			txLogTestID = testID
			txLogEntries = logs
			txLogCalled <- struct{}{}
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	select {
	case <-txLogCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("BulkInsertTxLogs was not called within timeout")
	}

	if txLogTestID != "test-txlog" {
		t.Errorf("testID = %q, want test-txlog", txLogTestID)
	}
	if len(txLogEntries) != 1 {
		t.Fatalf("txLogEntries len = %d, want 1", len(txLogEntries))
	}
	if txLogEntries[0].Status != "confirmed" {
		t.Errorf("tx status = %q, want confirmed (should be updated from pendingTxs)", txLogEntries[0].Status)
	}
	if txLogEntries[0].ConfirmLatencyMs != 1000 {
		t.Errorf("confirmLatencyMs = %d, want 1000", txLogEntries[0].ConfirmLatencyMs)
	}
}

func TestPersistTestData_TxLogDisabled(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-nolog"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false
	lg.txLogBuf = nil

	txLogCalled := false
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, _ *storage.TestRun) error { return nil },
		BulkInsertTxLogsFn: func(_ context.Context, _ string, _ []storage.TxLogEntry) error {
			txLogCalled = true
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	time.Sleep(100 * time.Millisecond)
	if txLogCalled {
		t.Error("BulkInsertTxLogs should not be called when logging disabled")
	}
}

func TestPersistTestData_BlockMetricsAggregation(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-agg"
	lg.startTime = time.Now().Add(-10 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false

	lg.timeSeriesBuf = []storage.TimeSeriesPoint{
		{GasUsed: 5_000_000, GasLimit: 30_000_000, BlockCount: 1, MgasPerSec: 5.0, FillRate: 16.6},
		{GasUsed: 15_000_000, GasLimit: 30_000_000, BlockCount: 2, MgasPerSec: 15.0, FillRate: 50.0},
		{GasUsed: 0, GasLimit: 0, BlockCount: 0, MgasPerSec: 0, FillRate: 0},
	}

	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, run *storage.TestRun) error {
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if capturedRun == nil {
		t.Fatal("CompleteTestRun not called")
	}
	if capturedRun.BlockCount != 3 {
		t.Errorf("BlockCount = %d, want 3", capturedRun.BlockCount)
	}
	if capturedRun.TotalGasUsed != 20_000_000 {
		t.Errorf("TotalGasUsed = %d, want 20000000", capturedRun.TotalGasUsed)
	}
	if capturedRun.PeakMgasPerSec != 15.0 {
		t.Errorf("PeakMgasPerSec = %f, want 15.0", capturedRun.PeakMgasPerSec)
	}
	expectedAvgFillRate := (16.6 + 50.0) / 2.0
	if capturedRun.AvgFillRate < expectedAvgFillRate-0.1 || capturedRun.AvgFillRate > expectedAvgFillRate+0.1 {
		t.Errorf("AvgFillRate = %f, want ~%f", capturedRun.AvgFillRate, expectedAvgFillRate)
	}
}

func TestPersistTestData_NilStorage(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-nil-storage"
	lg.startTime = time.Now().Add(-1 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false
	lg.storage = nil

	lg.saveTestResult()
}

func TestSaveTestResult_FlowStats(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-flow"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false

	col := metrics.NewInMemoryCollector(true)
	lg.metricsCol = col

	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, run *storage.TestRun) error {
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if capturedRun == nil {
		t.Fatal("CompleteTestRun not called")
	}
	if capturedRun.ID != "test-flow" {
		t.Errorf("ID = %q, want test-flow", capturedRun.ID)
	}
}

func TestRecordTxSent_ConcurrentAccess(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true
	lg.txLogBuf = make([]storage.TxLogEntry, 0, 1000)
	lg.pendingTxs = sync.Map{}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var hash common.Hash
			hash[0] = byte(idx)
			hash[1] = byte(idx >> 8)
			hash[31] = byte(idx)
			lg.recordTxSent(hash, time.Now(), idx%10, uint64(idx), float64(idx))
		}(i)
	}
	wg.Wait()

	lg.txLogBufMu.Lock()
	bufLen := len(lg.txLogBuf)
	lg.txLogBufMu.Unlock()

	if bufLen != 100 {
		t.Errorf("txLogBuf len = %d, want 100", bufLen)
	}

	mapCount := 0
	lg.pendingTxs.Range(func(_, _ any) bool { mapCount++; return true })
	if mapCount != 100 {
		t.Errorf("pendingTxs count = %d, want 100", mapCount)
	}
}

func TestSaveTestResult_DeployedContracts(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-contracts"
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false
	lg.contractsDeployed = true
	lg.erc20Contract = common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	lg.gasConsumerContract = common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12")

	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, run *storage.TestRun) error {
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if capturedRun == nil {
		t.Fatal("CompleteTestRun not called")
	}
	if len(capturedRun.DeployedContracts) < 2 {
		t.Fatalf("DeployedContracts len = %d, want >= 2", len(capturedRun.DeployedContracts))
	}
	foundERC20 := false
	foundGasConsumer := false
	for _, c := range capturedRun.DeployedContracts {
		if c.Name == "ERC20" {
			foundERC20 = true
		}
		if c.Name == "GasConsumer" {
			foundGasConsumer = true
		}
	}
	if !foundERC20 {
		t.Error("missing ERC20 in DeployedContracts")
	}
	if !foundGasConsumer {
		t.Error("missing GasConsumer in DeployedContracts")
	}
}

func TestSaveTestResult_AverageTPS(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.currentTestID = "test-tps"
	lg.startTime = time.Now().Add(-10 * time.Second)
	lg.testConfig = types.StartTestRequest{Pattern: types.PatternConstant}
	lg.txLoggingEnabled = false

	col := metrics.NewInMemoryCollector(true)
	for i := 0; i < 1000; i++ {
		hash := common.BigToHash(common.Big1)
		hash[0] = byte(i)
		hash[1] = byte(i >> 8)
		col.RecordTxSent(hash, time.Now())
		col.IncTxSent()
	}
	lg.metricsCol = col

	var capturedRun *storage.TestRun
	mockStore := &mockStorage{
		CompleteTestRunFn: func(_ context.Context, _ string, run *storage.TestRun) error {
			capturedRun = run
			return nil
		},
	}
	lg.storage = mockStore

	lg.saveTestResult()

	if capturedRun == nil {
		t.Fatal("CompleteTestRun not called")
	}
	if capturedRun.TxSent != 1000 {
		t.Errorf("TxSent = %d, want 1000", capturedRun.TxSent)
	}
	if capturedRun.AverageTPS <= 0 {
		t.Error("AverageTPS should be > 0")
	}
}
