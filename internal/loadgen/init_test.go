package loadgen

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func stopTestLG(lg *LoadGenerator) {
	if lg.cancel != nil {
		lg.cancel()
	}
	lg.wg.Wait()
}

func makeReq(overrides ...func(*types.StartTestRequest)) types.StartTestRequest {
	req := types.StartTestRequest{
		Pattern:      types.PatternConstant,
		DurationSec:  300,
		ConstantRate: 10,
		NumAccounts:  1,
	}
	for _, o := range overrides {
		o(&req)
	}
	return req
}

func initLG(t *testing.T, lg *LoadGenerator, req types.StartTestRequest) {
	t.Helper()
	lg.status = types.StatusInitializing
	lg.testConfig = req
	lg.runInitialization(req)
}

func TestRunInitialization_SetsStatusRunning(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	req := makeReq()
	initLG(t, lg, req)
	defer stopTestLG(lg)

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusRunning {
		t.Errorf("expected status %q, got %q", types.StatusRunning, lg.status)
	}
	if lg.initPhase != types.InitPhaseNone {
		t.Errorf("expected initPhase cleared, got %q", lg.initPhase)
	}
	if lg.initProgress != "" {
		t.Errorf("expected initProgress cleared, got %q", lg.initProgress)
	}
}

func TestRunInitialization_DefaultsTxType(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	req := makeReq()
	req.TransactionType = "" // force default
	initLG(t, lg, req)
	defer stopTestLG(lg)

	if lg.currentTxType != types.TxTypeEthTransfer {
		t.Errorf("expected default tx type %q, got %q", types.TxTypeEthTransfer, lg.currentTxType)
	}
}

func TestRunInitialization_CreatesContextAndCancel(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq())

	if lg.ctx == nil {
		t.Error("expected ctx to be set")
	}
	if lg.cancel == nil {
		t.Error("expected cancel to be set")
	}

	lg.cancel()
	select {
	case <-lg.ctx.Done():
	case <-time.After(time.Second):
		t.Error("context not cancelled after calling cancel()")
	}
	lg.wg.Wait()
}

func TestRunInitialization_AccountGenerationError(t *testing.T) {
	mock := &mockAccountManager{
		accounts: nil,
		GenerateDynamicAccountsFn: func(count int) error {
			return fmt.Errorf("keygen failure")
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq(func(r *types.StartTestRequest) {
		r.NumAccounts = 5
	}))

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusError {
		t.Errorf("expected status %q, got %q", types.StatusError, lg.status)
	}
	if lg.testError == "" {
		t.Error("expected testError to be set")
	}
}

func TestRunInitialization_NonceInitError(t *testing.T) {
	mock := &mockAccountManager{
		accounts: []*account.Account{makeTestAccount(t)},
		InitializeNoncesFn: func(ctx context.Context, client rpc.Client, numAccounts int) error {
			return fmt.Errorf("nonce RPC timeout")
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq())

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusError {
		t.Errorf("expected error status on nonce init failure, got %q", lg.status)
	}
	if lg.testError == "" {
		t.Error("expected testError set for nonce init failure")
	}
}

func TestRunInitialization_InvalidPattern(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq(func(r *types.StartTestRequest) {
		r.Pattern = "nonexistent-pattern"
	}))

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusError {
		t.Errorf("expected error status for invalid pattern, got %q", lg.status)
	}
}

func TestRunInitialization_InitBuffers(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq(func(r *types.StartTestRequest) {
		r.DurationSec = 10
		r.ConstantRate = 500
	}))
	stopTestLG(lg)

	if lg.timeSeriesBuf == nil {
		t.Error("expected timeSeriesBuf to be initialized")
	}
	expectedCap := 10*5 + 10
	if cap(lg.timeSeriesBuf) != expectedCap {
		t.Errorf("expected timeSeriesBuf capacity %d, got %d", expectedCap, cap(lg.timeSeriesBuf))
	}
}

func TestRunInitialization_TxLoggingDisabledForLargeTests(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq(func(r *types.StartTestRequest) {
		r.DurationSec = 3600
		r.ConstantRate = 500000
	}))
	defer stopTestLG(lg)

	if lg.txLoggingEnabled {
		t.Error("expected txLogging disabled for large test")
	}
	if lg.txLogBuf != nil {
		t.Error("expected txLogBuf nil when logging disabled")
	}
}

func TestRunInitialization_GasPricing(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	mockL2 := &mockRPCClient{
		GetGasPriceFn: func(ctx context.Context) (uint64, error) {
			return 1_000_000_000, nil
		},
		GetBaseFeeFn: func(ctx context.Context) (uint64, error) {
			return 500_000_000, nil
		},
		GetBlockNumberFn: func(ctx context.Context) (uint64, error) {
			return 100, nil
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock), WithL2Client(mockL2))
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	if lg.gasTipCap == nil {
		t.Fatal("expected gasTipCap to be set")
	}
	if lg.gasFeeCap == nil {
		t.Fatal("expected gasFeeCap to be set")
	}
	expectedFeeCap := new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(2))
	if lg.gasFeeCap.Cmp(expectedFeeCap) != 0 {
		t.Errorf("expected gasFeeCap %s, got %s", expectedFeeCap, lg.gasFeeCap)
	}
}

func TestRunInitialization_GasPricingFallback(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	mockL2 := &mockRPCClient{
		GetGasPriceFn: func(ctx context.Context) (uint64, error) {
			return 0, fmt.Errorf("RPC error")
		},
		GetBaseFeeFn: func(ctx context.Context) (uint64, error) {
			return 0, fmt.Errorf("RPC error")
		},
		GetBlockNumberFn: func(ctx context.Context) (uint64, error) {
			return 0, nil
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock), WithL2Client(mockL2))
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	expectedFeeCap := new(big.Int).Mul(lg.gasTipCap, big.NewInt(2))
	if lg.gasFeeCap.Cmp(expectedFeeCap) != 0 {
		t.Errorf("expected fallback gasFeeCap %s, got %s", expectedFeeCap, lg.gasFeeCap)
	}
}

func TestRunInitialization_ClearsWarnings(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	lg.warnings = []string{"old warning"}
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	lg.warningsMu.RLock()
	defer lg.warningsMu.RUnlock()
	for _, w := range lg.warnings {
		if w == "old warning" {
			t.Error("expected old warnings cleared, but 'old warning' still present")
		}
	}
}

func TestRunInitialization_ClearsBlockMetrics(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	lg.peakMgasPerSec = 999
	lg.totalBlockCount = 50
	lg.cumulativeGasUsed = 12345
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.peakMgasPerSec != 0 {
		t.Errorf("expected peakMgasPerSec reset, got %f", lg.peakMgasPerSec)
	}
	if lg.totalBlockCount != 0 {
		t.Errorf("expected totalBlockCount reset, got %d", lg.totalBlockCount)
	}
	if lg.cumulativeGasUsed != 0 {
		t.Errorf("expected cumulativeGasUsed reset, got %d", lg.cumulativeGasUsed)
	}
}

func TestRunInitialization_SetsAtomics(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	atomic.StoreInt64(&lg.peakRate, 999)
	atomic.StoreInt32(&lg.stopping, 1)

	initLG(t, lg, makeReq(func(r *types.StartTestRequest) {
		r.ConstantRate = 100
	}))
	stopTestLG(lg)

	if atomic.LoadInt64(&lg.peakRate) != 0 {
		t.Errorf("expected peakRate reset to 0")
	}
	if atomic.LoadInt32(&lg.stopping) != 0 {
		t.Errorf("expected stopping reset to 0")
	}
	if atomic.LoadInt64(&lg.currentRate) <= 0 {
		t.Errorf("expected currentRate > 0, got %d", atomic.LoadInt64(&lg.currentRate))
	}
}

func TestRunInitialization_SetsTestID(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	if lg.currentTestID == "" {
		t.Error("expected currentTestID to be set")
	}
	if len(lg.currentTestID) < 5 {
		t.Errorf("currentTestID too short: %q", lg.currentTestID)
	}
}

func TestRunInitialization_PanicRecovery(t *testing.T) {
	mock := &mockAccountManager{
		accounts: []*account.Account{makeTestAccount(t)},
		InitializeNoncesFn: func(ctx context.Context, client rpc.Client, numAccounts int) error {
			panic("unexpected nil pointer")
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq())

	lg.statusMu.RLock()
	defer lg.statusMu.RUnlock()
	if lg.status != types.StatusError {
		t.Errorf("expected error status after panic, got %q", lg.status)
	}
	if lg.testError == "" {
		t.Error("expected testError set after panic")
	}
}

func TestRunInitialization_AccountCountAutoCalc_Constant(t *testing.T) {
	genCount := 0
	mock := &mockAccountManager{
		accounts: []*account.Account{makeTestAccount(t)},
		GenerateDynamicAccountsFn: func(count int) error {
			genCount = count
			return fmt.Errorf("stop early for test")
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	req := makeReq(func(r *types.StartTestRequest) {
		r.NumAccounts = 0
		r.ConstantRate = 5000
	})
	initLG(t, lg, req)

	if genCount <= 0 {
		t.Error("expected GenerateDynamicAccounts to be called with positive count")
	}
	if lg.initAccountsTotal <= 1 {
		t.Errorf("expected auto-calculated accounts > 1, got %d", lg.initAccountsTotal)
	}
	// Error state expected since we stopped early
	if lg.status != types.StatusError {
		t.Errorf("expected error status, got %q", lg.status)
	}
}

func TestRunInitialization_AccountCountFromRealisticConfig(t *testing.T) {
	mock := &mockAccountManager{
		accounts: []*account.Account{makeTestAccount(t)},
		GenerateDynamicAccountsFn: func(count int) error {
			return fmt.Errorf("stop early for test")
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	req := makeReq(func(r *types.StartTestRequest) {
		r.Pattern = types.PatternRealistic
		r.NumAccounts = 0
		r.ConstantRate = 0
		r.RealisticConfig = &types.RealisticTestConfig{
			NumAccounts: 42,
			TargetTPS:   100,
			TxTypeRatios: types.TxTypeRatio{
				EthTransfer: 100,
			},
		}
	})
	initLG(t, lg, req)

	if lg.initAccountsTotal != 42 {
		t.Errorf("expected 42 accounts from realistic config, got %d", lg.initAccountsTotal)
	}
}

func TestRunInitialization_AccountCountFallback(t *testing.T) {
	mock := &mockAccountManager{
		accounts: []*account.Account{makeTestAccount(t)},
		GenerateDynamicAccountsFn: func(count int) error {
			return fmt.Errorf("stop early for test")
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	req := makeReq(func(r *types.StartTestRequest) {
		r.NumAccounts = 0
		r.ConstantRate = 0
	})
	initLG(t, lg, req)

	if lg.initAccountsTotal != 10 {
		t.Errorf("expected fallback 10 accounts, got %d", lg.initAccountsTotal)
	}
}

func TestInitBuffers_TxLoggingEnabled(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = true

	lg.initBuffers(10*time.Second, 100)

	if lg.timeSeriesBuf == nil {
		t.Error("expected timeSeriesBuf allocated")
	}
	if lg.txLogBuf == nil {
		t.Error("expected txLogBuf allocated when logging enabled")
	}
	expectedTxCap := 10*100 + 1000
	if cap(lg.txLogBuf) != expectedTxCap {
		t.Errorf("expected txLogBuf capacity %d, got %d", expectedTxCap, cap(lg.txLogBuf))
	}
}

func TestInitBuffers_TxLoggingDisabled(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.txLoggingEnabled = false

	lg.initBuffers(10*time.Second, 100)

	if lg.timeSeriesBuf == nil {
		t.Error("expected timeSeriesBuf allocated")
	}
	if lg.txLogBuf != nil {
		t.Error("expected txLogBuf nil when logging disabled")
	}
}

func TestShouldLogTransactions_Init(t *testing.T) {
	lg := newTestLoadGenerator(t)

	tests := []struct {
		name     string
		duration time.Duration
		tps      int
		want     bool
	}{
		{"small test", 60 * time.Second, 100, true},
		{"medium test", 300 * time.Second, 1000, true},
		{"large test exceeding limit", 3600 * time.Second, 500000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lg.shouldLogTransactions(tt.duration, tt.tps)
			if got != tt.want {
				t.Errorf("shouldLogTransactions(%v, %d) = %v, want %v", tt.duration, tt.tps, got, tt.want)
			}
		})
	}
}

func TestRunInitialization_RecordsStartBlock(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	mockL2 := &mockRPCClient{
		GetBlockNumberFn: func(ctx context.Context) (uint64, error) {
			return 42, nil
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock), WithL2Client(mockL2))
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.testStartBlockNumber != 42 {
		t.Errorf("expected testStartBlockNumber=42, got %d", lg.testStartBlockNumber)
	}
}

func TestRunInitialization_ExplicitGasFeeCap(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	lg.cfg.GasFeeCap = 5_000_000_000
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	expected := big.NewInt(5_000_000_000)
	if lg.gasFeeCap.Cmp(expected) != 0 {
		t.Errorf("expected explicit gasFeeCap %s, got %s", expected, lg.gasFeeCap)
	}
}

func TestRunInitialization_BaseFeeBumpsFeeCap(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	mockL2 := &mockRPCClient{
		GetGasPriceFn: func(ctx context.Context) (uint64, error) {
			return 1_000_000_000, nil
		},
		GetBaseFeeFn: func(ctx context.Context) (uint64, error) {
			return 5_000_000_000, nil
		},
		GetBlockNumberFn: func(ctx context.Context) (uint64, error) {
			return 1, nil
		},
	}
	lg := newTestLoadGenerator(t, WithAccountManager(mock), WithL2Client(mockL2))
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	minExpected := new(big.Int).Mul(big.NewInt(5_000_000_000), big.NewInt(2))
	if lg.gasFeeCap.Cmp(minExpected) < 0 {
		t.Errorf("expected gasFeeCap >= %s (2x baseFee), got %s", minExpected, lg.gasFeeCap)
	}
}

func TestRunInitialization_SetsDuration(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	initLG(t, lg, makeReq(func(r *types.StartTestRequest) {
		r.DurationSec = 60
	}))
	defer stopTestLG(lg)

	if lg.currentDuration != 60*time.Second {
		t.Errorf("expected duration 60s, got %v", lg.currentDuration)
	}
	if lg.startTime.IsZero() {
		t.Error("expected startTime to be set")
	}
}

func TestRunInitialization_MetricsReset(t *testing.T) {
	mock := &mockAccountManager{accounts: []*account.Account{makeTestAccount(t)}}
	lg := newTestLoadGenerator(t, WithAccountManager(mock))
	atomic.StoreUint64(&lg.lastPreconfSeqNum, 99)
	atomic.StoreUint64(&lg.preconfGaps, 5)
	initLG(t, lg, makeReq())
	defer stopTestLG(lg)

	if atomic.LoadUint64(&lg.lastPreconfSeqNum) != 0 {
		t.Error("expected lastPreconfSeqNum reset")
	}
	if atomic.LoadUint64(&lg.preconfGaps) != 0 {
		t.Error("expected preconfGaps reset")
	}
}
