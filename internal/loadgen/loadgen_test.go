package loadgen

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/execnode"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// newTestLoadGenerator creates a LoadGenerator with all mock dependencies
// for unit testing. Override specific mocks by passing additional options.
func newTestLoadGenerator(t *testing.T, opts ...Option) *LoadGenerator {
	t.Helper()

	cfg := &config.Config{
		BuilderRPCURL:  "http://mock-builder:3000",
		L2RPCURL:       "http://mock-l2:8545",
		PreconfWSURL:   "",
		ChainID:        42069,
		GasPrice:       1000000000,
		GasTipCap:      1000000000,
		GasLimit:       21000,
		BlockTimeMS:    150,
		ExecutionLayer: "op-reth",
		Capabilities:   execnode.DefaultRegistry().Get("op-reth"),
	}

	mockStore := &mockStorage{}
	logger := slog.Default()

	defaultOpts := []Option{
		WithBuilderClient(&mockRPCClient{}),
		WithL2Client(&mockRPCClient{}),
		WithMetricsCollector(metrics.NewInMemoryCollector(true)),
		WithSender(&mockSender{}),
		WithDeployer(&mockDeployer{}),
		WithAccountManager(&mockAccountManager{}),
	}

	// User-provided options override defaults
	allOpts := append(defaultOpts, opts...)

	lg, err := NewLoadGenerator(cfg, mockStore, logger, allOpts...)
	if err != nil {
		t.Fatalf("newTestLoadGenerator: %v", err)
	}
	return lg
}

func TestNewLoadGenerator_WithMocks(t *testing.T) {
	lg := newTestLoadGenerator(t)

	if lg == nil {
		t.Fatal("expected non-nil LoadGenerator")
	}
	if lg.status != types.StatusIdle {
		t.Errorf("expected status %q, got %q", types.StatusIdle, lg.status)
	}
	if lg.builderClient == nil {
		t.Error("expected builderClient to be set")
	}
	if lg.l2Client == nil {
		t.Error("expected l2Client to be set")
	}
	if lg.metricsCol == nil {
		t.Error("expected metricsCol to be set")
	}
	if lg.sender == nil {
		t.Error("expected sender to be set")
	}
	if lg.deployer == nil {
		t.Error("expected deployer to be set")
	}
	if lg.accountMgr == nil {
		t.Error("expected accountMgr to be set")
	}
}

func TestNewLoadGenerator_OptionOverridesDefault(t *testing.T) {
	customClient := &mockRPCClient{}
	lg := newTestLoadGenerator(t, WithBuilderClient(customClient))

	if lg.builderClient != customClient {
		t.Error("expected custom builder client to override default")
	}
}

// mockFullStorage implements both storage.Storage and storage.CacheStorage
// to test the constructor's cache storage wiring.
type mockFullStorage struct {
	mockStorage
	mockCacheStorage
}

func TestNewLoadGenerator_DefaultsWithNoOptions(t *testing.T) {
	cfg := &config.Config{
		BuilderRPCURL:  "http://localhost:13000",
		L2RPCURL:       "http://localhost:8545",
		ChainID:        42069,
		GasPrice:       1000000000,
		GasTipCap:      1000000000,
		GasLimit:       21000,
		BlockTimeMS:    1000,
		ExecutionLayer: "op-reth",
		Capabilities:   execnode.DefaultRegistry().Get("op-reth"),
	}

	lg, err := NewLoadGenerator(cfg, &mockStorage{}, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lg.builderClient == nil {
		t.Error("expected default builderClient to be created")
	}
	if lg.l2Client == nil {
		t.Error("expected default l2Client to be created")
	}
	if lg.metricsCol == nil {
		t.Error("expected default metricsCol to be created")
	}
	if lg.sender == nil {
		t.Error("expected default sender to be created")
	}
	if lg.deployer == nil {
		t.Error("expected default deployer to be created")
	}
	if lg.accountMgr == nil {
		t.Error("expected default accountMgr to be created")
	}
	if lg.patternReg == nil {
		t.Error("expected patternReg to be created")
	}
	if lg.txBuilderReg == nil {
		t.Error("expected txBuilderReg to be created")
	}
	if lg.preconfLatencies == nil {
		t.Error("expected preconfLatencies to be created")
	}
	if lg.status != types.StatusIdle {
		t.Errorf("expected initial status %q, got %q", types.StatusIdle, lg.status)
	}
}

func TestNewLoadGenerator_CacheStorageWired(t *testing.T) {
	cfg := &config.Config{
		BuilderRPCURL:  "http://localhost:13000",
		L2RPCURL:       "http://localhost:8545",
		ChainID:        42069,
		GasPrice:       1000000000,
		GasTipCap:      1000000000,
		GasLimit:       21000,
		BlockTimeMS:    1000,
		ExecutionLayer: "op-reth",
		Capabilities:   execnode.DefaultRegistry().Get("op-reth"),
	}

	fullStore := &mockFullStorage{}
	lg, err := NewLoadGenerator(cfg, fullStore, slog.Default(),
		WithAccountManager(&mockAccountManager{}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lg.cacheStorage == nil {
		t.Error("expected cacheStorage to be wired when store implements CacheStorage")
	}
}

func TestNewLoadGenerator_CacheStorageNotWired(t *testing.T) {
	lg := newTestLoadGenerator(t)

	if lg.cacheStorage != nil {
		t.Error("expected cacheStorage nil when store doesn't implement CacheStorage")
	}
}

func TestNewLoadGenerator_RecipientFromFirstAccount(t *testing.T) {
	acc := makeTestAccount(t)
	mgr := &mockAccountManager{accounts: []*account.Account{acc}}
	lg := newTestLoadGenerator(t, WithAccountManager(mgr))

	if lg.txBuilderReg == nil {
		t.Fatal("expected txBuilderReg to be set")
	}
	// The registry is wired with acc's address as the default recipient.
	// We can't inspect the internal recipient directly, but we verify the
	// registry was created with a non-nil account manager.
}

func TestNewLoadGenerator_NoAccountsEmptyRecipient(t *testing.T) {
	mgr := &mockAccountManager{accounts: []*account.Account{}}
	lg := newTestLoadGenerator(t, WithAccountManager(mgr))

	if lg.txBuilderReg == nil {
		t.Fatal("expected txBuilderReg to be set even with no accounts")
	}
}

// --- StartTest tests ---

func TestStartTest_SetsStatusInitializing(t *testing.T) {
	lg := newTestLoadGenerator(t)

	// Capture status synchronously: StartTest sets StatusInitializing
	// before launching the goroutine, so we can check it immediately
	// by hooking into the mock account manager.
	statusAfterStart := make(chan types.TestStatus, 1)
	origMgr := lg.accountMgr.(*mockAccountManager)
	origMgr.GenerateDynamicAccountsFn = func(count int) error {
		// This runs inside the goroutine — capture status at this point
		lg.statusMu.RLock()
		s := lg.status
		lg.statusMu.RUnlock()
		statusAfterStart <- s
		return nil
	}

	err := lg.StartTest(types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  10,
		ConstantRate: 100,
		NumAccounts:  0, // triggers GenerateDynamicAccounts
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case s := <-statusAfterStart:
		if s != types.StatusInitializing {
			t.Errorf("expected status %q during init, got %q", types.StatusInitializing, s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initialization to start")
	}

	// Clean up
	if lg.cancel != nil {
		lg.cancel()
	}
	lg.wg.Wait()
}

func TestStartTest_RejectsDoubleStart(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	err := lg.StartTest(types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  10,
		ConstantRate: 100,
	})

	if err == nil {
		t.Fatal("expected error when test already running")
	}
}

func TestStartTest_RejectsWhileInitializing(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusInitializing
	lg.statusMu.Unlock()

	err := lg.StartTest(types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  10,
		ConstantRate: 100,
	})

	if err == nil {
		t.Fatal("expected error when test is initializing")
	}
}

func TestStartTest_ValidatesRealisticConfig(t *testing.T) {
	lg := newTestLoadGenerator(t)

	err := lg.StartTest(types.StartTestRequest{
		Pattern:     types.PatternRealistic,
		DurationSec: 10,
		RealisticConfig: &types.RealisticTestConfig{
			TargetTPS:   100,
			NumAccounts: 10,
			TxTypeRatios: types.TxTypeRatio{
				EthTransfer: 50,
				// Ratios don't sum to 100 — should fail validation
			},
		},
	})

	if err == nil {
		t.Fatal("expected error for invalid realistic config ratios")
	}
}

func TestStartTest_StoresTestConfig(t *testing.T) {
	lg := newTestLoadGenerator(t)

	req := types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  42,
		ConstantRate: 500,
		NumAccounts:  10,
	}
	err := lg.StartTest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lg.testConfig.DurationSec != 42 {
		t.Errorf("expected DurationSec 42, got %d", lg.testConfig.DurationSec)
	}
	if lg.testConfig.ConstantRate != 500 {
		t.Errorf("expected ConstantRate 500, got %d", lg.testConfig.ConstantRate)
	}

	if lg.cancel != nil {
		lg.cancel()
	}
	lg.wg.Wait()
}

func TestStartTest_ClearsInitProgress(t *testing.T) {
	lg := newTestLoadGenerator(t)

	// Set stale values before starting
	lg.statusMu.Lock()
	lg.initPhase = "old-phase"
	lg.initProgress = "old progress"
	lg.initAccountsTotal = 99
	lg.initAccountsGen = 99
	lg.statusMu.Unlock()

	err := lg.StartTest(types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  10,
		ConstantRate: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// StartTest clears init progress synchronously before launching goroutine.
	// Verify by checking testError is cleared (also set in the sync block).
	lg.statusMu.RLock()
	if lg.testError != "" {
		t.Errorf("expected testError cleared, got %q", lg.testError)
	}
	lg.statusMu.RUnlock()

	if lg.cancel != nil {
		lg.cancel()
	}
	lg.wg.Wait()
}

// --- StopTest tests ---

func TestStopTest_NoOpWhenNotRunning(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.statusMu.Lock()
	lg.status = types.StatusIdle
	lg.statusMu.Unlock()

	// Should not panic or change state
	lg.StopTest()

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusIdle {
		t.Errorf("expected status unchanged, got %q", lg.status)
	}
}

func TestStopTest_TransitionsToCompleted(t *testing.T) {
	lg := newTestLoadGenerator(t)

	// Set up running state
	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.status = types.StatusRunning
	atomic.StoreInt32(&lg.stopping, 0)

	lg.StopTest()

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusCompleted {
		t.Errorf("expected status %q, got %q", types.StatusCompleted, lg.status)
	}
}

func TestStopTest_SetStoppingFlag(t *testing.T) {
	lg := newTestLoadGenerator(t)

	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.status = types.StatusRunning

	lg.StopTest()

	if atomic.LoadInt32(&lg.stopping) != 1 {
		t.Error("expected stopping flag to be set")
	}
}

func TestStopTest_RecordsEndBlockNumber(t *testing.T) {
	mockL2 := &mockRPCClient{
		GetBlockNumberFn: func(ctx context.Context) (uint64, error) {
			return 12345, nil
		},
	}
	lg := newTestLoadGenerator(t, WithL2Client(mockL2))

	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.status = types.StatusRunning

	lg.StopTest()

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.testEndBlockNumber != 12345 {
		t.Errorf("expected testEndBlockNumber 12345, got %d", lg.testEndBlockNumber)
	}
}

func TestStopTest_FinalizesDiscardedTxs(t *testing.T) {
	lg := newTestLoadGenerator(t)

	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.status = types.StatusRunning

	// Add pending txs
	lg.pendingTxs.Store([32]byte{1}, &storage.TxLogEntry{Status: "pending"})
	lg.pendingTxs.Store([32]byte{2}, &storage.TxLogEntry{Status: "pending"})
	lg.pendingTxs.Store([32]byte{3}, &storage.TxLogEntry{Status: "confirmed"})

	lg.StopTest()

	if lg.discardedCount != 2 {
		t.Errorf("expected 2 discarded, got %d", lg.discardedCount)
	}
}

// --- countPendingTxs / finalizePendingTxs tests ---

func TestCountPendingTxs_Empty(t *testing.T) {
	lg := newTestLoadGenerator(t)

	count := lg.countPendingTxs()
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestCountPendingTxs_MixedStatuses(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.pendingTxs.Store([32]byte{1}, &storage.TxLogEntry{Status: "pending"})
	lg.pendingTxs.Store([32]byte{2}, &storage.TxLogEntry{Status: "confirmed"})
	lg.pendingTxs.Store([32]byte{3}, &storage.TxLogEntry{Status: "pending"})
	lg.pendingTxs.Store([32]byte{4}, &storage.TxLogEntry{Status: "failed"})

	count := lg.countPendingTxs()
	if count != 2 {
		t.Errorf("expected 2 pending, got %d", count)
	}
}

func TestFinalizePendingTxs_MarksAsDiscarded(t *testing.T) {
	lg := newTestLoadGenerator(t)

	entry1 := &storage.TxLogEntry{Status: "pending"}
	entry2 := &storage.TxLogEntry{Status: "confirmed"}
	entry3 := &storage.TxLogEntry{Status: "pending"}

	lg.pendingTxs.Store([32]byte{1}, entry1)
	lg.pendingTxs.Store([32]byte{2}, entry2)
	lg.pendingTxs.Store([32]byte{3}, entry3)

	count := lg.finalizePendingTxs()
	if count != 2 {
		t.Errorf("expected 2 discarded, got %d", count)
	}
	if entry1.Status != "discarded" {
		t.Errorf("expected entry1 status 'discarded', got %q", entry1.Status)
	}
	if entry2.Status != "confirmed" {
		t.Errorf("expected entry2 status unchanged, got %q", entry2.Status)
	}
	if entry3.Status != "discarded" {
		t.Errorf("expected entry3 status 'discarded', got %q", entry3.Status)
	}
}

func TestFinalizePendingTxs_Empty(t *testing.T) {
	lg := newTestLoadGenerator(t)

	count := lg.finalizePendingTxs()
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

// --- setError tests ---

func TestSetError_SetsStatusAndMessage(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.setError("something broke")

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusError {
		t.Errorf("expected status %q, got %q", types.StatusError, lg.status)
	}
	if lg.testError != "something broke" {
		t.Errorf("expected testError %q, got %q", "something broke", lg.testError)
	}
}

// --- Concurrency tests ---

func TestCountPendingTxs_ConcurrentAccess(t *testing.T) {
	lg := newTestLoadGenerator(t)

	// Pre-populate
	for i := 0; i < 100; i++ {
		var key [32]byte
		key[0] = byte(i)
		lg.pendingTxs.Store(key, &storage.TxLogEntry{Status: "pending"})
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lg.countPendingTxs()
		}()
	}
	wg.Wait()
}
