package metrics

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicSubSaturating(t *testing.T) {
	testCases := []struct {
		name     string
		initial  int64
		delta    int64
		expected int64
	}{
		{"normal subtraction", 100, 50, 50},
		{"exact to zero", 100, 100, 0},
		{"saturating at zero", 100, 150, 0},
		{"zero minus value", 0, 50, 0},
		{"large values", 1000000, 500000, 500000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var value int64 = tc.initial
			result := AtomicSubSaturating(&value, tc.delta)

			if result != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, result)
			}
			if value != tc.expected {
				t.Errorf("value expected %d, got %d", tc.expected, value)
			}
		})
	}
}

func TestAtomicSubSaturating_Concurrent(t *testing.T) {
	var value int64 = 1000000

	var wg sync.WaitGroup
	numGoroutines := 100
	subtractPerGoroutine := int64(10000)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			AtomicSubSaturating(&value, subtractPerGoroutine)
		}()
	}

	wg.Wait()

	// 1000000 - (100 * 10000) = 0
	if value != 0 {
		t.Errorf("expected 0, got %d", value)
	}
}

func TestAtomicSubSaturating_Oversubtract(t *testing.T) {
	var value int64 = 100

	var wg sync.WaitGroup
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			AtomicSubSaturating(&value, 50)
		}()
	}

	wg.Wait()

	// Should saturate at 0, never go negative
	if value != 0 {
		t.Errorf("expected 0, got %d", value)
	}
}

func TestAtomicMax(t *testing.T) {
	testCases := []struct {
		name     string
		initial  int64
		newVal   int64
		expected int64
	}{
		{"new is larger", 50, 100, 100},
		{"new is smaller", 100, 50, 100},
		{"new is equal", 100, 100, 100},
		{"zero to positive", 0, 100, 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var value int64 = tc.initial
			result := AtomicMax(&value, tc.newVal)

			if result != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, result)
			}
			if value != tc.expected {
				t.Errorf("value expected %d, got %d", tc.expected, value)
			}
		})
	}
}

func TestAtomicMax_Concurrent(t *testing.T) {
	var value int64 = 0

	var wg sync.WaitGroup
	maxValue := int64(10000)

	for i := int64(0); i < maxValue; i++ {
		wg.Add(1)
		go func(v int64) {
			defer wg.Done()
			AtomicMax(&value, v)
		}(i)
	}

	wg.Wait()

	if value != maxValue-1 {
		t.Errorf("expected %d, got %d", maxValue-1, value)
	}
}

func TestCounter(t *testing.T) {
	c := &Counter{}

	// Test Inc
	if v := c.Inc(); v != 1 {
		t.Errorf("expected 1, got %d", v)
	}

	// Test Add
	if v := c.Add(10); v != 11 {
		t.Errorf("expected 11, got %d", v)
	}

	// Test Load
	if v := c.Load(); v != 11 {
		t.Errorf("expected 11, got %d", v)
	}

	// Test Store
	c.Store(50)
	if v := c.Load(); v != 50 {
		t.Errorf("expected 50, got %d", v)
	}

	// Test Reset
	c.Reset()
	if v := c.Load(); v != 0 {
		t.Errorf("expected 0, got %d", v)
	}

	// Test SubSaturating
	c.Store(100)
	if v := c.SubSaturating(30); v != 70 {
		t.Errorf("expected 70, got %d", v)
	}
	if v := c.SubSaturating(100); v != 0 {
		t.Errorf("expected 0 (saturated), got %d", v)
	}
}

func BenchmarkAtomicSubSaturating(b *testing.B) {
	var value int64 = 1000000000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AtomicSubSaturating(&value, 1)
		atomic.AddInt64(&value, 1) // Add it back to prevent exhaustion
	}
}

func BenchmarkAtomicSubSaturating_Contended(b *testing.B) {
	var value int64 = 1000000000

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			AtomicSubSaturating(&value, 1)
			atomic.AddInt64(&value, 1)
		}
	})
}
