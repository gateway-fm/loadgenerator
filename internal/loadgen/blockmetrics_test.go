package loadgen

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/storage"
)

func TestCalculateRollingMgasPerSec_EmptyWindow(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	result := lg.calculateRollingMgasPerSec()
	if result != 0 {
		t.Errorf("expected 0 for empty window, got %f", result)
	}
}

func TestCalculateRollingMgasPerSec_NilContext(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx = nil

	result := lg.calculateRollingMgasPerSec()
	if result != 0 {
		t.Errorf("expected 0 for nil context, got %f", result)
	}
}

func TestCalculateRollingMgasPerSec(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		window    []rollingGasPoint
		wantZero  bool
		wantAbove float64
		wantBelow float64
	}{
		{
			name: "single point under 1 second returns 0 (minimum window check)",
			window: []rollingGasPoint{
				{timestamp: now.Add(-100 * time.Millisecond), gasUsed: 50_000_000},
			},
			wantZero: true,
		},
		{
			name: "two points under 1 second returns 0 (minimum window check)",
			window: []rollingGasPoint{
				{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 50_000_000},
				{timestamp: now.Add(-300 * time.Millisecond), gasUsed: 50_000_000},
			},
			wantZero: true,
		},
		{
			name: "three points over 1 second calculates correctly",
			window: []rollingGasPoint{
				{timestamp: now.Add(-2 * time.Second), gasUsed: 100_000_000},
				{timestamp: now.Add(-1 * time.Second), gasUsed: 100_000_000},
				{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 100_000_000},
			},
			// 300M gas over 2s = 150 MGas/s
			wantAbove: 100,
			wantBelow: 200,
		},
		{
			name: "points outside window are pruned",
			window: []rollingGasPoint{
				{timestamp: now.Add(-10 * time.Second), gasUsed: 999_000_000},
				{timestamp: now.Add(-2 * time.Second), gasUsed: 50_000_000},
				{timestamp: now.Add(-1 * time.Second), gasUsed: 50_000_000},
				{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 50_000_000},
			},
			// Only 150M gas in window (old 999M pruned), over ~2s
			wantAbove: 50,
			wantBelow: 200,
		},
		{
			name: "all points expired returns 0",
			window: []rollingGasPoint{
				{timestamp: now.Add(-10 * time.Second), gasUsed: 100_000_000},
				{timestamp: now.Add(-8 * time.Second), gasUsed: 100_000_000},
			},
			wantZero: true,
		},
		{
			name: "high gas single second window with 3+ blocks",
			window: []rollingGasPoint{
				{timestamp: now.Add(-1500 * time.Millisecond), gasUsed: 500_000_000},
				{timestamp: now.Add(-1000 * time.Millisecond), gasUsed: 500_000_000},
				{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 500_000_000},
			},
			// 1500M gas over 1.5s = 1000 MGas/s
			wantAbove: 800,
			wantBelow: 1200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lg := newTestLoadGenerator(t)
			lg.ctx, lg.cancel = context.WithCancel(context.Background())
			defer lg.cancel()

			lg.blockMetricsMu.Lock()
			lg.rollingGasWindow = make([]rollingGasPoint, len(tt.window))
			copy(lg.rollingGasWindow, tt.window)
			lg.blockMetricsMu.Unlock()

			result := lg.calculateRollingMgasPerSec()

			if tt.wantZero {
				if result != 0 {
					t.Errorf("expected 0, got %f", result)
				}
				return
			}
			if result <= tt.wantAbove {
				t.Errorf("expected > %f, got %f", tt.wantAbove, result)
			}
			if result >= tt.wantBelow {
				t.Errorf("expected < %f, got %f", tt.wantBelow, result)
			}
		})
	}
}

func TestCalculateRollingMgasPerSec_CachesLastValue(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	now := time.Now()
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-2 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 100_000_000},
	}
	lg.blockMetricsMu.Unlock()

	result := lg.calculateRollingMgasPerSec()
	if result == 0 {
		t.Fatal("expected non-zero result")
	}

	lg.blockMetricsMu.Lock()
	cached := lg.lastMgasPerSec
	lg.blockMetricsMu.Unlock()

	if cached != result {
		t.Errorf("lastMgasPerSec not cached: expected %f, got %f", result, cached)
	}
}

func TestCalculateRollingMgasPerSec_FreezeOnStop(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())

	now := time.Now()
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-2 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 100_000_000},
	}
	lg.blockMetricsMu.Unlock()

	liveResult := lg.calculateRollingMgasPerSec()
	if liveResult == 0 {
		t.Fatal("expected non-zero live result")
	}

	lg.cancel()

	frozenResult := lg.calculateRollingMgasPerSec()
	if frozenResult != liveResult {
		t.Errorf("expected frozen value %f after cancel, got %f", liveResult, frozenResult)
	}
}

func TestCalculateRollingTxPerSec_EmptyWindow(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	result := lg.calculateRollingTxPerSec()
	if result != 0 {
		t.Errorf("expected 0, got %f", result)
	}
}

func TestCalculateRollingTxPerSec_NilContext(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx = nil

	result := lg.calculateRollingTxPerSec()
	if result != 0 {
		t.Errorf("expected 0 for nil context, got %f", result)
	}
}

func TestCalculateRollingTxPerSec(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		window    []rollingTxPoint
		wantZero  bool
		wantAbove float64
		wantBelow float64
	}{
		{
			name: "single point under 1s returns 0 (minimum window)",
			window: []rollingTxPoint{
				{timestamp: now.Add(-100 * time.Millisecond), txCount: 500},
			},
			wantZero: true,
		},
		{
			name: "three points over 2 seconds",
			window: []rollingTxPoint{
				{timestamp: now.Add(-2 * time.Second), txCount: 1000},
				{timestamp: now.Add(-1 * time.Second), txCount: 1000},
				{timestamp: now.Add(-500 * time.Millisecond), txCount: 1000},
			},
			// 3000 tx over 2s = 1500 TPS
			wantAbove: 1000,
			wantBelow: 2000,
		},
		{
			name: "expired points pruned",
			window: []rollingTxPoint{
				{timestamp: now.Add(-10 * time.Second), txCount: 9999},
				{timestamp: now.Add(-2 * time.Second), txCount: 100},
				{timestamp: now.Add(-1 * time.Second), txCount: 100},
				{timestamp: now.Add(-500 * time.Millisecond), txCount: 100},
			},
			// 300 tx over 2s = 150 TPS (old 9999 pruned)
			wantAbove: 100,
			wantBelow: 250,
		},
		{
			name: "all expired returns 0",
			window: []rollingTxPoint{
				{timestamp: now.Add(-10 * time.Second), txCount: 500},
			},
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lg := newTestLoadGenerator(t)
			lg.ctx, lg.cancel = context.WithCancel(context.Background())
			defer lg.cancel()

			lg.blockMetricsMu.Lock()
			lg.rollingTxWindow = make([]rollingTxPoint, len(tt.window))
			copy(lg.rollingTxWindow, tt.window)
			lg.blockMetricsMu.Unlock()

			result := lg.calculateRollingTxPerSec()

			if tt.wantZero {
				if result != 0 {
					t.Errorf("expected 0, got %f", result)
				}
				return
			}
			if result <= tt.wantAbove {
				t.Errorf("expected > %f, got %f", tt.wantAbove, result)
			}
			if result >= tt.wantBelow {
				t.Errorf("expected < %f, got %f", tt.wantBelow, result)
			}
		})
	}
}

func TestCalculateRollingTxPerSec_FreezeOnStop(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())

	now := time.Now()
	lg.blockMetricsMu.Lock()
	lg.rollingTxWindow = []rollingTxPoint{
		{timestamp: now.Add(-2 * time.Second), txCount: 500},
		{timestamp: now.Add(-1 * time.Second), txCount: 500},
		{timestamp: now.Add(-500 * time.Millisecond), txCount: 500},
	}
	lg.blockMetricsMu.Unlock()

	liveResult := lg.calculateRollingTxPerSec()
	if liveResult == 0 {
		t.Fatal("expected non-zero live result")
	}

	lg.cancel()

	frozenResult := lg.calculateRollingTxPerSec()
	if frozenResult != liveResult {
		t.Errorf("expected frozen value %f, got %f", liveResult, frozenResult)
	}
}

func TestCalculateRollingTxPerSec_UpdatesPeakRate(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	atomic.StoreInt64(&lg.peakRate, 0)

	now := time.Now()
	lg.blockMetricsMu.Lock()
	lg.rollingTxWindow = []rollingTxPoint{
		{timestamp: now.Add(-2 * time.Second), txCount: 5000},
		{timestamp: now.Add(-1 * time.Second), txCount: 5000},
		{timestamp: now.Add(-500 * time.Millisecond), txCount: 5000},
	}
	lg.blockMetricsMu.Unlock()

	result := lg.calculateRollingTxPerSec()
	if result == 0 {
		t.Fatal("expected non-zero result")
	}

	peak := atomic.LoadInt64(&lg.peakRate)
	if peak == 0 {
		t.Error("expected peakRate to be updated")
	}
}

func TestGetBlockMetricsForPeriod_WithWSMetrics(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.blockMetricsMu.Lock()
	lg.blockMetrics = []blockMetricsPoint{
		{gasUsed: 21_000_000, gasLimit: 30_000_000, blockTime: 1.0, blockTimeMs: 1000},
		{gasUsed: 25_000_000, gasLimit: 30_000_000, blockTime: 1.0, blockTimeMs: 1000},
		{gasUsed: 30_000_000, gasLimit: 30_000_000, blockTime: 1.0, blockTimeMs: 1000},
	}
	lg.blockMetricsMu.Unlock()

	gasUsed, gasLimit, blockCount, mgasPerSec, fillRate := lg.getBlockMetricsForPeriod()

	if gasUsed != 76_000_000 {
		t.Errorf("gasUsed: expected 76000000, got %d", gasUsed)
	}
	if gasLimit != 90_000_000 {
		t.Errorf("gasLimit: expected 90000000, got %d", gasLimit)
	}
	if blockCount != 3 {
		t.Errorf("blockCount: expected 3, got %d", blockCount)
	}
	// mgasPerSec = 76M / 1M / 3s = ~25.33
	if mgasPerSec < 25 || mgasPerSec > 26 {
		t.Errorf("mgasPerSec: expected ~25.33, got %f", mgasPerSec)
	}
	// fillRate = 76M / 90M * 100 = ~84.4%
	if fillRate < 84 || fillRate > 85 {
		t.Errorf("fillRate: expected ~84.4, got %f", fillRate)
	}

	// Verify blockMetrics cleared after call
	lg.blockMetricsMu.Lock()
	remaining := len(lg.blockMetrics)
	lg.blockMetricsMu.Unlock()
	if remaining != 0 {
		t.Errorf("expected blockMetrics cleared, got %d entries", remaining)
	}
}

func TestGetBlockMetricsForPeriod_Empty(t *testing.T) {
	lg := newTestLoadGenerator(t)
	// l2Client is set but will return 0 block number, so RPC fallback returns zeros
	gasUsed, _, blockCount, _, _ := lg.getBlockMetricsForPeriod()
	if gasUsed != 0 {
		t.Errorf("expected 0 gasUsed for empty metrics, got %d", gasUsed)
	}
	if blockCount != 0 {
		t.Errorf("expected 0 blockCount for empty metrics, got %d", blockCount)
	}
}

func TestPeakMgasPerSec_Tracking(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()
	lg.startTime = time.Now().Add(-2 * time.Second) // past warmup

	now := time.Now()

	// First measurement: moderate
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-2 * time.Second), gasUsed: 50_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 50_000_000},
		{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 50_000_000},
	}
	lg.blockMetricsMu.Unlock()

	mgas1 := lg.calculateRollingMgasPerSec()

	// Record peak via the same logic as recordTimeSeriesPoint
	lg.blockMetricsMu.Lock()
	if mgas1 > lg.peakMgasPerSec && time.Since(lg.startTime) > 1*time.Second {
		lg.peakMgasPerSec = mgas1
	}
	lg.blockMetricsMu.Unlock()

	// Second measurement: higher
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-2 * time.Second), gasUsed: 200_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 200_000_000},
		{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 200_000_000},
	}
	lg.blockMetricsMu.Unlock()

	mgas2 := lg.calculateRollingMgasPerSec()

	lg.blockMetricsMu.Lock()
	if mgas2 > lg.peakMgasPerSec && time.Since(lg.startTime) > 1*time.Second {
		lg.peakMgasPerSec = mgas2
	}
	peak := lg.peakMgasPerSec
	lg.blockMetricsMu.Unlock()

	if mgas2 <= mgas1 {
		t.Errorf("expected second measurement > first: %f <= %f", mgas2, mgas1)
	}
	if peak != mgas2 {
		t.Errorf("peak should equal highest measurement: expected %f, got %f", mgas2, peak)
	}
}

func TestPeakMgasPerSec_SkippedDuringWarmup(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()
	lg.startTime = time.Now() // just started, within warmup

	now := time.Now()
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-2 * time.Second), gasUsed: 999_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 999_000_000},
		{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 999_000_000},
	}
	lg.peakMgasPerSec = 0
	lg.blockMetricsMu.Unlock()

	mgas := lg.calculateRollingMgasPerSec()
	if mgas == 0 {
		t.Fatal("expected non-zero mgas")
	}

	// Simulate warmup check from recordTimeSeriesPoint
	testDuration := time.Since(lg.startTime)
	lg.blockMetricsMu.Lock()
	if mgas > lg.peakMgasPerSec && testDuration > 1*time.Second {
		lg.peakMgasPerSec = mgas
	}
	peak := lg.peakMgasPerSec
	lg.blockMetricsMu.Unlock()

	if peak != 0 {
		t.Errorf("peak should remain 0 during warmup, got %f", peak)
	}
}

func TestCalculateAvgBlockTimeMs(t *testing.T) {
	tests := []struct {
		name    string
		metrics []blockMetricsPoint
		want    float64
	}{
		{
			name:    "empty",
			metrics: nil,
			want:    0,
		},
		{
			name: "single block",
			metrics: []blockMetricsPoint{
				{blockTimeMs: 1000},
			},
			want: 1000,
		},
		{
			name: "multiple blocks averaged",
			metrics: []blockMetricsPoint{
				{blockTimeMs: 800},
				{blockTimeMs: 1000},
				{blockTimeMs: 1200},
			},
			want: 1000,
		},
		{
			name: "skips zero block times",
			metrics: []blockMetricsPoint{
				{blockTimeMs: 0},
				{blockTimeMs: 1000},
				{blockTimeMs: 2000},
			},
			want: 1500,
		},
		{
			name: "uses last 10 blocks only",
			metrics: func() []blockMetricsPoint {
				pts := make([]blockMetricsPoint, 15)
				for i := range pts {
					pts[i].blockTimeMs = 100 // old blocks
				}
				// Last 10 blocks have blockTimeMs=500
				for i := 5; i < 15; i++ {
					pts[i].blockTimeMs = 500
				}
				return pts
			}(),
			want: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lg := newTestLoadGenerator(t)
			lg.blockMetricsMu.Lock()
			lg.blockMetrics = tt.metrics
			lg.blockMetricsMu.Unlock()

			got := lg.calculateAvgBlockTimeMs()
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("expected %f, got %f", tt.want, got)
			}
		})
	}
}

func TestRecordTimeSeriesPoint_NoStartTime(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	lg.recordTimeSeriesPoint()
	if len(lg.timeSeriesBuf) != 0 {
		t.Errorf("expected no point recorded when startTime is zero, got %d", len(lg.timeSeriesBuf))
	}
}

func TestRecordTimeSeriesPoint_AppendsPoint(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()
	lg.startTime = time.Now().Add(-5 * time.Second)
	lg.timeSeriesBuf = make([]storage.TimeSeriesPoint, 0, 10)
	atomic.StoreInt64(&lg.currentRate, 1000)

	lg.recordTimeSeriesPoint()

	if len(lg.timeSeriesBuf) != 1 {
		t.Fatalf("expected 1 point, got %d", len(lg.timeSeriesBuf))
	}
	p := lg.timeSeriesBuf[0]
	if p.TimestampMs <= 0 {
		t.Errorf("expected positive timestamp, got %d", p.TimestampMs)
	}
	if p.TargetTPS != 1000 {
		t.Errorf("expected targetTPS=1000, got %d", p.TargetTPS)
	}
}

func TestRecordTimeSeriesPoint_IncludesGasPricing(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()
	lg.startTime = time.Now().Add(-2 * time.Second)
	lg.timeSeriesBuf = make([]storage.TimeSeriesPoint, 0, 10)

	lg.builderPressureMu.Lock()
	lg.latestBaseFeeGwei = 1.5
	lg.latestGasPriceGwei = 2.5
	lg.builderPressureMu.Unlock()

	lg.recordTimeSeriesPoint()

	if len(lg.timeSeriesBuf) != 1 {
		t.Fatalf("expected 1 point, got %d", len(lg.timeSeriesBuf))
	}
	p := lg.timeSeriesBuf[0]
	if p.BaseFeeGwei != 1.5 {
		t.Errorf("expected baseFeeGwei=1.5, got %f", p.BaseFeeGwei)
	}
	if p.GasPriceGwei != 2.5 {
		t.Errorf("expected gasPriceGwei=2.5, got %f", p.GasPriceGwei)
	}
}

func TestRollingWindowPrune(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	now := time.Now()
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-10 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-8 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-6 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-2 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-500 * time.Millisecond), gasUsed: 100_000_000},
	}
	lg.blockMetricsMu.Unlock()

	lg.calculateRollingMgasPerSec()

	lg.blockMetricsMu.Lock()
	remaining := len(lg.rollingGasWindow)
	lg.blockMetricsMu.Unlock()

	if remaining != 3 {
		t.Errorf("expected 3 entries after pruning (within 5s window), got %d", remaining)
	}
}

func TestCalculateRollingMgasPerSec_WindowBoundary(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.ctx, lg.cancel = context.WithCancel(context.Background())
	defer lg.cancel()

	now := time.Now()
	// Entry exactly at the 5-second boundary (cutoff edge)
	lg.blockMetricsMu.Lock()
	lg.rollingGasWindow = []rollingGasPoint{
		{timestamp: now.Add(-5*time.Second - 1*time.Millisecond), gasUsed: 999_000_000}, // just outside
		{timestamp: now.Add(-4*time.Second + 999*time.Millisecond), gasUsed: 100_000_000}, // just inside
		{timestamp: now.Add(-3 * time.Second), gasUsed: 100_000_000},
		{timestamp: now.Add(-1 * time.Second), gasUsed: 100_000_000},
	}
	lg.blockMetricsMu.Unlock()

	result := lg.calculateRollingMgasPerSec()

	// Should include 300M gas (3 points), not 999M from expired point
	// 300M / 1M = 300 Mgas over ~4-5 seconds = ~60-75 MGas/s
	if result > 100 {
		t.Errorf("boundary entry likely included expired point, got %f MGas/s (expected < 100)", result)
	}

	lg.blockMetricsMu.Lock()
	remaining := len(lg.rollingGasWindow)
	lg.blockMetricsMu.Unlock()
	if remaining != 3 {
		t.Errorf("expected 3 entries after boundary prune, got %d", remaining)
	}
}
