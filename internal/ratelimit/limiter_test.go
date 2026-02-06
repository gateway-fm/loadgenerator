package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterNew(t *testing.T) {
	l := New(100)
	if l.Rate() != 100 {
		t.Errorf("expected rate 100, got %v", l.Rate())
	}
}

func TestLimiterNewMinimum(t *testing.T) {
	// Zero or negative rate should default to minimum
	l := New(0)
	if l.Rate() != 1 {
		t.Errorf("expected rate 1 (minimum), got %v", l.Rate())
	}

	l = New(-5)
	if l.Rate() != 1 {
		t.Errorf("expected rate 1 (minimum), got %v", l.Rate())
	}
}

func TestLimiterSetRate(t *testing.T) {
	l := New(100)
	l.SetRate(500)
	if l.Rate() != 500 {
		t.Errorf("expected rate 500, got %v", l.Rate())
	}
}

func TestLimiterSetRateMinimum(t *testing.T) {
	l := New(100)
	l.SetRate(0)
	if l.Rate() != 1 {
		t.Errorf("expected rate 1 (minimum), got %v", l.Rate())
	}
}

func TestLimiterWaitImmediate(t *testing.T) {
	// First call should be immediate (sequence 0, time = startTime)
	l := New(10000)
	ctx := context.Background()

	start := time.Now()
	err := l.Wait(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("expected near-instant first wait, got %v", elapsed)
	}
}

func TestLimiterWaitCancellation(t *testing.T) {
	// Low rate to ensure Wait blocks
	l := New(1) // 1 per second

	ctx, cancel := context.WithCancel(context.Background())

	// First wait should be immediate
	_ = l.Wait(ctx)

	// Cancel before second wait completes
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// Second wait should be cancelled
	err := l.Wait(ctx)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestLimiterSmoothness(t *testing.T) {
	// Test that permits are issued at correct intervals
	rate := 100.0 // 100 per second = 10ms per permit
	l := New(rate)
	ctx := context.Background()

	// Measure time for 10 permits
	n := 10
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	elapsed := time.Since(start)

	// Expected time: (n-1 permits) * interval = 90ms
	// (first permit is immediate, subsequent ones spaced by interval)
	expected := time.Duration(float64(time.Second) * float64(n-1) / rate)
	// Allow 20% tolerance for timer precision
	minExpected := time.Duration(float64(expected) * 0.8)
	maxExpected := time.Duration(float64(expected) * 1.3)

	if elapsed < minExpected || elapsed > maxExpected {
		t.Errorf("expected elapsed time ~%v (range %v-%v), got %v",
			expected, minExpected, maxExpected, elapsed)
	}
}

func TestLimiterHighThroughput(t *testing.T) {
	// Test high throughput with multiple goroutines
	rate := 10000.0 // 10k per second
	l := New(rate)
	ctx := context.Background()

	numWorkers := 100
	permitsPerWorker := 100
	totalPermits := numWorkers * permitsPerWorker

	var wg sync.WaitGroup
	var count atomic.Int64

	start := time.Now()

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < permitsPerWorker; j++ {
				if err := l.Wait(ctx); err != nil {
					return
				}
				count.Add(1)
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	if count.Load() != int64(totalPermits) {
		t.Errorf("expected %d permits, got %d", totalPermits, count.Load())
	}

	// Expected time: (totalPermits - 1) / rate
	expected := time.Duration(float64(time.Second) * float64(totalPermits-1) / rate)
	// Allow 30% tolerance for high-concurrency timing
	minExpected := time.Duration(float64(expected) * 0.7)
	maxExpected := time.Duration(float64(expected) * 1.4)

	if elapsed < minExpected || elapsed > maxExpected {
		t.Errorf("expected elapsed time ~%v (range %v-%v), got %v",
			expected, minExpected, maxExpected, elapsed)
	}

	// Check actual rate
	actualRate := float64(totalPermits) / elapsed.Seconds()
	if actualRate < rate*0.7 || actualRate > rate*1.4 {
		t.Errorf("expected rate ~%v, got %v", rate, actualRate)
	}
}

func TestLimiterRateChange(t *testing.T) {
	l := New(100)
	ctx := context.Background()

	// Consume a few permits at initial rate
	for i := 0; i < 5; i++ {
		l.Wait(ctx)
	}

	// Change rate
	l.SetRate(1000)
	if l.Rate() != 1000 {
		t.Errorf("expected rate 1000, got %v", l.Rate())
	}

	// New permits should use new rate (faster)
	start := time.Now()
	for i := 0; i < 10; i++ {
		l.Wait(ctx)
	}
	elapsed := time.Since(start)

	// At 1000/s, 10 permits should take ~9ms (first is immediate)
	if elapsed > 50*time.Millisecond {
		t.Errorf("rate change didn't take effect, elapsed %v", elapsed)
	}
}

func TestLimiterAccuracy(t *testing.T) {
	// Test that the limiter is accurate within 2%
	rate := 5000.0 // 5k per second
	l := New(rate)
	ctx := context.Background()

	duration := 2 * time.Second
	deadline := time.Now().Add(duration)

	var count int64
	for time.Now().Before(deadline) {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
	}

	// Expected permits in 2 seconds at 5k/s = 10000
	expected := int64(rate * duration.Seconds())
	tolerance := int64(float64(expected) * 0.02) // 2% tolerance

	diff := count - expected
	if diff < 0 {
		diff = -diff
	}

	if diff > tolerance {
		actualRate := float64(count) / duration.Seconds()
		t.Errorf("rate accuracy out of tolerance: expected %d permits (±%d), got %d (actual rate: %.1f/s, target: %.1f/s, error: %.2f%%)",
			expected, tolerance, count, actualRate, rate, 100*float64(count-expected)/float64(expected))
	}
}
