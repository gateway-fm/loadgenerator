package metrics

import (
	"math"
	"sync"
	"testing"
)

func TestStreamingLatencyStats_Basic(t *testing.T) {
	s := NewStreamingLatencyStats()

	// Add some samples
	for i := 0; i < 100; i++ {
		s.Add(float64(i))
	}

	stats := s.GetStats()
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}

	if stats.Count != 100 {
		t.Errorf("expected count 100, got %d", stats.Count)
	}

	if stats.Min != 0 {
		t.Errorf("expected min 0, got %f", stats.Min)
	}

	if stats.Max != 99 {
		t.Errorf("expected max 99, got %f", stats.Max)
	}

	// Average should be ~49.5
	if math.Abs(stats.Avg-49.5) > 0.1 {
		t.Errorf("expected avg ~49.5, got %f", stats.Avg)
	}

	// P50 should be ~50
	if math.Abs(stats.P50-49.5) > 2 {
		t.Errorf("expected p50 ~49.5, got %f", stats.P50)
	}
}

func TestStreamingLatencyStats_Empty(t *testing.T) {
	s := NewStreamingLatencyStats()

	stats := s.GetStats()
	if stats != nil {
		t.Error("expected nil stats for empty collector")
	}
}

func TestStreamingLatencyStats_Buckets(t *testing.T) {
	s := NewStreamingLatencyStats()

	// Add samples in different bucket ranges
	// 0-250ms: 10 samples
	for i := 0; i < 10; i++ {
		s.Add(100)
	}
	// 250-500ms: 5 samples
	for i := 0; i < 5; i++ {
		s.Add(300)
	}
	// 500-1000ms: 3 samples
	for i := 0; i < 3; i++ {
		s.Add(750)
	}

	stats := s.GetStats()
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}

	if len(stats.Buckets) != 5 {
		t.Errorf("expected 5 buckets, got %d", len(stats.Buckets))
	}

	// Check bucket counts
	if stats.Buckets[0].Count != 10 {
		t.Errorf("expected bucket 0 count 10, got %d", stats.Buckets[0].Count)
	}
	if stats.Buckets[1].Count != 5 {
		t.Errorf("expected bucket 1 count 5, got %d", stats.Buckets[1].Count)
	}
	if stats.Buckets[2].Count != 3 {
		t.Errorf("expected bucket 2 count 3, got %d", stats.Buckets[2].Count)
	}
}

func TestStreamingLatencyStats_Concurrent(t *testing.T) {
	s := NewStreamingLatencyStats()

	var wg sync.WaitGroup
	numGoroutines := 10
	samplesPerGoroutine := 1000

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < samplesPerGoroutine; j++ {
				s.Add(float64(id*100 + j%100))
			}
		}(i)
	}

	wg.Wait()

	stats := s.GetStats()
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}

	expectedCount := numGoroutines * samplesPerGoroutine
	if stats.Count != expectedCount {
		t.Errorf("expected count %d, got %d", expectedCount, stats.Count)
	}
}

func TestStreamingLatencyStats_Reset(t *testing.T) {
	s := NewStreamingLatencyStats()

	for i := 0; i < 100; i++ {
		s.Add(float64(i))
	}

	s.Reset()

	stats := s.GetStats()
	if stats != nil {
		t.Error("expected nil stats after reset")
	}

	if s.Count() != 0 {
		t.Errorf("expected count 0 after reset, got %d", s.Count())
	}
}

func BenchmarkStreamingLatencyStats_Add(b *testing.B) {
	s := NewStreamingLatencyStats()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		s.Add(float64(i % 1000))
	}
}

func BenchmarkStreamingLatencyStats_GetStats(b *testing.B) {
	s := NewStreamingLatencyStats()

	// Pre-populate with 10000 samples
	for i := 0; i < 10000; i++ {
		s.Add(float64(i % 1000))
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		s.GetStats()
	}
}
