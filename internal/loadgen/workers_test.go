package loadgen

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/pattern"
	"github.com/gateway-fm/loadgenerator/internal/ratelimit"
)

func TestShouldStop_ContextCancelled(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour

	if lg.shouldStop() {
		t.Fatal("should not stop before cancellation")
	}

	cancel()
	if !lg.shouldStop() {
		t.Fatal("should stop after context cancellation")
	}
}

func TestShouldStop_StoppingFlag(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour

	atomic.StoreInt32(&lg.stopping, 1)
	if !lg.shouldStop() {
		t.Fatal("should stop when stopping flag is set")
	}
}

func TestShouldStop_DurationExpired(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.startTime = time.Now().Add(-2 * time.Second)
	lg.currentDuration = 1 * time.Second

	if !lg.shouldStop() {
		t.Fatal("should stop when duration expired")
	}
}

func TestCircuitBreaker_AtomicCounters(t *testing.T) {
	lg := newTestLoadGenerator(t)

	atomic.StoreInt64(&lg.recentSends, 0)
	atomic.StoreInt64(&lg.recentFails, 0)
	atomic.StoreInt64(&lg.recentRevocations, 0)

	atomic.AddInt64(&lg.recentSends, 100)
	atomic.AddInt64(&lg.recentFails, 5)
	atomic.AddInt64(&lg.recentRevocations, 3)

	sends := atomic.LoadInt64(&lg.recentSends)
	fails := atomic.LoadInt64(&lg.recentFails)
	revocations := atomic.LoadInt64(&lg.recentRevocations)

	if sends != 100 {
		t.Errorf("expected sends=100, got %d", sends)
	}
	if fails != 5 {
		t.Errorf("expected fails=5, got %d", fails)
	}
	if revocations != 3 {
		t.Errorf("expected revocations=3, got %d", revocations)
	}

	// Swap should reset to 0
	swapped := atomic.SwapInt64(&lg.recentSends, 0)
	if swapped != 100 {
		t.Errorf("expected swap to return 100, got %d", swapped)
	}
	if atomic.LoadInt64(&lg.recentSends) != 0 {
		t.Error("expected recentSends=0 after swap")
	}
}

func TestCircuitBreaker_OpenClose(t *testing.T) {
	lg := newTestLoadGenerator(t)

	if atomic.LoadInt32(&lg.circuitOpen) != 0 {
		t.Fatal("circuit should start closed")
	}

	atomic.StoreInt32(&lg.circuitOpen, 1)
	if atomic.LoadInt32(&lg.circuitOpen) != 1 {
		t.Fatal("circuit should be open after set")
	}

	atomic.StoreInt32(&lg.circuitOpen, 0)
	if atomic.LoadInt32(&lg.circuitOpen) != 0 {
		t.Fatal("circuit should be closed after reset")
	}
}

func TestPendingCount_Tracking(t *testing.T) {
	lg := newTestLoadGenerator(t)

	atomic.StoreInt64(&lg.pendingCount, 0)
	atomic.AddInt64(&lg.pendingCount, 10)
	atomic.AddInt64(&lg.pendingCount, 5)

	if got := atomic.LoadInt64(&lg.pendingCount); got != 15 {
		t.Errorf("expected pendingCount=15, got %d", got)
	}

	atomic.AddInt64(&lg.pendingCount, -3)
	if got := atomic.LoadInt64(&lg.pendingCount); got != 12 {
		t.Errorf("expected pendingCount=12, got %d", got)
	}
}

func TestCircuitBreaker_FailureRateThreshold(t *testing.T) {
	// Thresholds from adaptiveController:
	//   failureRateThreshold = 0.30 (>30% send failures)
	//   revocationRateThreshold = 0.10 (>10% revocations)
	//   recoveryRateThreshold = 0.05 (<5% combined)
	tests := []struct {
		name       string
		sends      int64
		fails      int64
		revocations int64
		shouldTrip bool
	}{
		{"healthy - low failure", 200, 10, 0, false},             // 5% failure
		{"high failure rate", 200, 70, 0, true},                  // 35% > 30%
		{"borderline failure", 200, 60, 0, false},                // 30% exactly, not >30%
		{"high revocation rate", 200, 0, 22, true},               // 11% > 10%
		{"borderline revocation", 200, 0, 20, false},             // 10% exactly, not >10%
		{"combined but below individual", 200, 40, 10, false},    // 20% fail, 5% revoke - neither over
		{"both over threshold", 200, 70, 30, true},               // 35% fail, 15% revoke
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failureRate := float64(tt.fails) / float64(tt.sends)
			revocationRate := float64(tt.revocations) / float64(tt.sends)
			tripped := failureRate > 0.30 || revocationRate > 0.10
			if tripped != tt.shouldTrip {
				t.Errorf("sends=%d fails=%d revocations=%d: got tripped=%v, want %v (failRate=%.2f, revocRate=%.2f)",
					tt.sends, tt.fails, tt.revocations, tripped, tt.shouldTrip, failureRate, revocationRate)
			}
		})
	}
}

func TestCircuitBreaker_RecoveryThreshold(t *testing.T) {
	// recoveryRateThreshold = 0.05 (close circuit if combined < 5%)
	tests := []struct {
		name        string
		sends       int64
		fails       int64
		revocations int64
		shouldClose bool
	}{
		{"fully recovered", 100, 1, 1, true},     // 2% combined < 5%
		{"borderline", 100, 3, 1, true},           // 4% combined < 5%
		{"not recovered", 100, 3, 3, false},        // 6% combined >= 5%
		{"zero failures", 100, 0, 0, true},         // 0% < 5%
		{"exactly 5%", 100, 3, 2, false},           // 5% is not < 5%
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			combinedRate := float64(tt.fails+tt.revocations) / float64(tt.sends)
			closed := combinedRate < 0.05
			if closed != tt.shouldClose {
				t.Errorf("sends=%d fails=%d revocations=%d: got close=%v, want %v (combined=%.2f)",
					tt.sends, tt.fails, tt.revocations, closed, tt.shouldClose, combinedRate)
			}
		})
	}
}

func TestAdaptiveController_CircuitOpensAndReducesRate(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(1000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(1000)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	// Set high failure rate: 50 fails out of 120 sends = 41% > 30% threshold
	atomic.StoreInt64(&lg.currentRate, 1000)
	atomic.StoreInt64(&lg.recentSends, 120)
	atomic.StoreInt64(&lg.recentFails, 50)
	atomic.StoreInt64(&lg.recentRevocations, 0)
	atomic.StoreInt32(&lg.circuitOpen, 0)

	// Mock account manager to not block on resyncAllNonces
	mockAccMgr := &mockAccountManager{accounts: []*account.Account{}}
	lg.accountMgr = mockAccMgr

	lg.wg.Add(1)
	go lg.adaptiveController()

	// Wait for one tick (500ms) + buffer
	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	if atomic.LoadInt32(&lg.circuitOpen) != 1 {
		t.Fatal("circuit should be open after high failure rate")
	}

	newRate := atomic.LoadInt64(&lg.currentRate)
	if newRate >= 1000 {
		t.Errorf("rate should have been reduced from 1000, got %d", newRate)
	}
	if newRate < 50 {
		t.Errorf("rate should not go below floor of 50, got %d", newRate)
	}
}

func TestAdaptiveController_AIMDRecovery(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(1000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(200)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	// Circuit is open, rate was halved to 200, low failure (recovering)
	atomic.StoreInt64(&lg.currentRate, 200)
	atomic.StoreInt32(&lg.circuitOpen, 1)
	atomic.StoreInt64(&lg.preCircuitRate, 1000)
	// Provide insufficient samples so circuit breaker evaluation is skipped,
	// but circuit stays open -> AIMD recovery kicks in
	atomic.StoreInt64(&lg.recentSends, 10)
	atomic.StoreInt64(&lg.recentFails, 0)
	atomic.StoreInt64(&lg.recentRevocations, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	// AIMD: newRate = 200 + 200/2 = 300
	newRate := atomic.LoadInt64(&lg.currentRate)
	if newRate <= 200 {
		t.Errorf("rate should have increased via AIMD from 200, got %d", newRate)
	}
	if newRate > 1000 {
		t.Errorf("rate should be capped at preCircuitRate=1000, got %d", newRate)
	}
}

func TestAdaptiveController_RateIncrease_BelowTarget(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(500)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(500)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	atomic.StoreInt64(&lg.currentRate, 500)
	atomic.StoreInt64(&lg.pendingCount, 100) // Well below target
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.recentSends, 5) // Not enough for circuit eval
	atomic.StoreInt64(&lg.recentFails, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	newRate := atomic.LoadInt64(&lg.currentRate)
	if newRate <= 500 {
		t.Errorf("rate should increase when pending < target, got %d", newRate)
	}
}

func TestAdaptiveController_CriticalBackoff(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(2000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(2000)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	atomic.StoreInt64(&lg.currentRate, 2000)
	atomic.StoreInt64(&lg.pendingCount, 5000) // > 4x target = critical
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.recentSends, 5) // Not enough for circuit eval
	atomic.StoreInt64(&lg.recentFails, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	newRate := atomic.LoadInt64(&lg.currentRate)
	// Critical backoff halves: 2000/2 = 1000
	if newRate >= 2000 {
		t.Errorf("rate should be halved on critical backoff, got %d", newRate)
	}
	if newRate < 10 {
		t.Errorf("rate should not go below 10, got %d", newRate)
	}
}

func TestAdaptiveController_ExitsOnContextCancel(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(100)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(100)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour

	done := make(chan struct{})
	lg.wg.Add(1)
	go func() {
		lg.adaptiveController()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("adaptiveController did not exit on context cancellation")
	}
}

func TestResyncAllNonces_DelegatesToAccountManager(t *testing.T) {
	var getNonceCalls int
	mockBuilder := &mockRPCClient{
		GetNonceFn: func(ctx context.Context, address string) (uint64, error) {
			getNonceCalls++
			return 42, nil
		},
	}

	acc1, err := account.NewAccountFromHex("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err != nil {
		t.Fatal(err)
	}
	acc2, err := account.NewAccountFromHex("59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d")
	if err != nil {
		t.Fatal(err)
	}

	mockAccMgr := &mockAccountManager{
		accounts: []*account.Account{acc1, acc2},
	}

	lg := newTestLoadGenerator(t,
		WithBuilderClient(mockBuilder),
		WithAccountManager(mockAccMgr),
	)

	lg.resyncAllNonces()

	if getNonceCalls != 2 {
		t.Errorf("expected 2 GetNonce calls (one per account), got %d", getNonceCalls)
	}
}

func TestResyncAllNonces_FallsBackToL2(t *testing.T) {
	var confirmedCalls int
	mockBuilder := &mockRPCClient{
		GetNonceFn: func(ctx context.Context, address string) (uint64, error) {
			return 0, errors.New("connection failed")
		},
	}
	mockL2 := &mockRPCClient{
		GetConfirmedNonceFn: func(ctx context.Context, address string) (uint64, error) {
			confirmedCalls++
			return 10, nil
		},
	}

	acc, err := account.NewAccountFromHex("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err != nil {
		t.Fatal(err)
	}

	mockAccMgr := &mockAccountManager{
		accounts: []*account.Account{acc},
	}

	lg := newTestLoadGenerator(t,
		WithBuilderClient(mockBuilder),
		WithL2Client(mockL2),
		WithAccountManager(mockAccMgr),
	)

	lg.resyncAllNonces()

	if confirmedCalls != 1 {
		t.Errorf("expected 1 GetConfirmedNonce fallback call, got %d", confirmedCalls)
	}
}

func TestTpsCalculator_ExitsOnContextCancel(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.rateLimiter = ratelimit.New(100)

	done := make(chan struct{})
	lg.wg.Add(1)
	go func() {
		lg.tpsCalculator()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tpsCalculator did not exit on context cancellation")
	}
}

func TestCompletionWatcher_ExitsOnContextCancel(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour

	done := make(chan struct{})
	go func() {
		lg.completionWatcher()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("completionWatcher did not exit on context cancellation")
	}
}

func TestAdaptiveController_BackpressureThrottle(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(2000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(2000)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	atomic.StoreInt64(&lg.currentRate, 2000)
	atomic.StoreInt64(&lg.pendingCount, 100)
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.recentSends, 5)
	atomic.StoreInt64(&lg.recentFails, 0)

	// Set high builder pressure (> 0.8 threshold)
	lg.builderPressureMu.Lock()
	lg.builderPressure = 0.95
	lg.builderPressureMu.Unlock()

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	newRate := atomic.LoadInt64(&lg.currentRate)
	if newRate >= 2000 {
		t.Errorf("rate should be reduced by backpressure, got %d", newRate)
	}
}

func TestAdaptiveController_CircuitOpenTriggersNonceResync(t *testing.T) {
	resyncCalled := make(chan struct{}, 1)
	mockBuilder := &mockRPCClient{
		GetNonceFn: func(ctx context.Context, address string) (uint64, error) {
			select {
			case resyncCalled <- struct{}{}:
			default:
			}
			return 0, nil
		},
	}

	acc, err := account.NewAccountFromHex("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err != nil {
		t.Fatal(err)
	}
	mockAccMgr := &mockAccountManager{accounts: []*account.Account{acc}}

	lg := newTestLoadGenerator(t,
		WithBuilderClient(mockBuilder),
		WithAccountManager(mockAccMgr),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(1000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(1000)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	atomic.StoreInt64(&lg.currentRate, 1000)
	atomic.StoreInt64(&lg.recentSends, 200)
	atomic.StoreInt64(&lg.recentFails, 80) // 40% > 30%
	atomic.StoreInt64(&lg.recentRevocations, 0)
	atomic.StoreInt32(&lg.circuitOpen, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	select {
	case <-resyncCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("resyncAllNonces was not called when circuit opened")
	}

	if atomic.LoadInt32(&lg.circuitOpen) != 1 {
		t.Fatal("circuit should be open")
	}

	cancel()
	lg.wg.Wait()
}

func TestAdaptiveController_PreCircuitRateSaved(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(1500)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(1500)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	mockAccMgr := &mockAccountManager{accounts: []*account.Account{}}
	lg.accountMgr = mockAccMgr

	atomic.StoreInt64(&lg.currentRate, 1500)
	atomic.StoreInt64(&lg.recentSends, 200)
	atomic.StoreInt64(&lg.recentFails, 70) // 35% > 30%
	atomic.StoreInt64(&lg.recentRevocations, 0)
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.preCircuitRate, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	saved := atomic.LoadInt64(&lg.preCircuitRate)
	if saved != 1500 {
		t.Errorf("preCircuitRate should be saved as 1500, got %d", saved)
	}
}

func TestSenderWorker_ExitsOnContextCancel(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	lg.ctx = ctx
	lg.cancel = cancel
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.rateLimiter = ratelimit.New(100)
	lg.testConfig.Pattern = "constant"
	lg.currentTxType = "eth-transfer"

	acc, err := account.NewAccountFromHex("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err != nil {
		t.Fatal(err)
	}

	// Cancel immediately so senderWorker exits on first ctx check
	cancel()

	done := make(chan struct{})
	lg.wg.Add(1)
	go func() {
		lg.senderWorker(0, []*account.Account{acc})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("senderWorker did not exit on context cancellation")
	}
}

func TestSenderWorker_ExitsWithEmptyAccounts(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.rateLimiter = ratelimit.New(100)

	done := make(chan struct{})
	lg.wg.Add(1)
	go func() {
		lg.senderWorker(0, []*account.Account{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("senderWorker should exit immediately with empty accounts")
	}
}

func TestAdaptiveController_ModerateOverload(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(1000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(1000)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	atomic.StoreInt64(&lg.currentRate, 1000)
	atomic.StoreInt64(&lg.pendingCount, 1500) // > target but < 2x target
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.recentSends, 5)
	atomic.StoreInt64(&lg.recentFails, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	newRate := atomic.LoadInt64(&lg.currentRate)
	// Moderate: rate - step = 1000 - 100 = 900
	if newRate >= 1000 {
		t.Errorf("rate should decrease on moderate overload, got %d", newRate)
	}
}

func TestAdaptiveController_HighOverload(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(1000)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(1000)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	atomic.StoreInt64(&lg.currentRate, 1000)
	atomic.StoreInt64(&lg.pendingCount, 2500) // > 2x target but < 4x
	atomic.StoreInt32(&lg.circuitOpen, 0)
	atomic.StoreInt64(&lg.recentSends, 5)
	atomic.StoreInt64(&lg.recentFails, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	newRate := atomic.LoadInt64(&lg.currentRate)
	// Aggressive: rate - 2*step = 1000 - 200 = 800
	if newRate >= 1000 {
		t.Errorf("rate should decrease aggressively on high overload, got %d", newRate)
	}
}

func TestAdaptiveController_AIMDRecoveryCappedAtPreCircuitRate(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	maxPat := pattern.NewAdaptive(500)
	lg.currentPattern = maxPat
	lg.rateLimiter = ratelimit.New(800)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour
	lg.testConfig.AdaptiveTargetPending = 1000
	lg.testConfig.AdaptiveRateStep = 100

	// Circuit open, rate already close to ceiling
	atomic.StoreInt64(&lg.currentRate, 800)
	atomic.StoreInt32(&lg.circuitOpen, 1)
	atomic.StoreInt64(&lg.preCircuitRate, 900)
	// Low samples so circuit eval is skipped, stays open -> AIMD
	atomic.StoreInt64(&lg.recentSends, 10)
	atomic.StoreInt64(&lg.recentFails, 0)
	atomic.StoreInt64(&lg.recentRevocations, 0)

	lg.wg.Add(1)
	go lg.adaptiveController()

	time.Sleep(700 * time.Millisecond)
	cancel()
	lg.wg.Wait()

	newRate := atomic.LoadInt64(&lg.currentRate)
	// AIMD: 800 + 400 = 1200, but capped at 900
	if newRate > 900 {
		t.Errorf("AIMD rate should be capped at preCircuitRate=900, got %d", newRate)
	}
}

func TestAdaptiveController_SkipsNonAdaptivePattern(t *testing.T) {
	lg := newTestLoadGenerator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lg.ctx = ctx
	lg.cancel = cancel

	// Use a non-adaptive pattern (nil is fine, it will just `continue`)
	lg.currentPattern = nil
	lg.rateLimiter = ratelimit.New(100)
	lg.startTime = time.Now()
	lg.currentDuration = 1 * time.Hour

	initialRate := int64(500)
	atomic.StoreInt64(&lg.currentRate, initialRate)

	done := make(chan struct{})
	lg.wg.Add(1)
	go func() {
		lg.adaptiveController()
		close(done)
	}()

	time.Sleep(700 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("adaptiveController did not exit")
	}

	// Rate should not have changed since pattern is nil
	if got := atomic.LoadInt64(&lg.currentRate); got != initialRate {
		t.Errorf("rate should not change with nil pattern, got %d", got)
	}
}

func TestCircuitBreaker_RateFloor(t *testing.T) {
	// When circuit opens and rate/2 < 50, floor at 50
	rate := int64(80)
	newRate := rate / 2
	if newRate < 50 {
		newRate = 50
	}
	if newRate != 50 {
		t.Errorf("expected rate floor at 50, got %d", newRate)
	}

	// When rate/2 >= 50, no floor
	rate = int64(200)
	newRate = rate / 2
	if newRate < 50 {
		newRate = 50
	}
	if newRate != 100 {
		t.Errorf("expected rate=100, got %d", newRate)
	}
}
