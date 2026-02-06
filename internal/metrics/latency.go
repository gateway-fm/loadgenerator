// Package metrics provides metrics collection and calculation.
package metrics

import (
	"math"
	"sort"
	"sync"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// StreamingLatencyStats provides efficient streaming percentile calculation.
// Uses reservoir sampling for percentile estimation without storing all samples.
type StreamingLatencyStats struct {
	mu sync.RWMutex

	// Running statistics (O(1) memory)
	count int64
	sum   float64
	min   float64
	max   float64

	// Reservoir for percentile estimation
	// Uses Algorithm R (Vitter) - O(reservoirSize) memory
	reservoir     []float64
	reservoirSize int
	seen          int64

	// Histogram buckets (confirmation latency: 0-250ms, 250-500ms, 500-1s, 1-2s, 2s+)
	buckets      []int64
	bucketBounds []float64

	// Per-instance random state for reservoir sampling (xorshift64*)
	// Avoids data races from global state
	randState uint64
}

const (
	// DefaultReservoirSize is the number of samples to keep for percentile estimation.
	// Larger = more accurate, but more memory. 10000 gives <1% error at p99.
	DefaultReservoirSize = 10000

	// Confirmation latency bucket bounds in milliseconds
	bucket0 = 250.0
	bucket1 = 500.0
	bucket2 = 1000.0
	bucket3 = 2000.0
)

// NewStreamingLatencyStats creates a new streaming latency calculator.
func NewStreamingLatencyStats() *StreamingLatencyStats {
	return &StreamingLatencyStats{
		min:           math.MaxFloat64,
		max:           0,
		reservoir:     make([]float64, 0, DefaultReservoirSize),
		reservoirSize: DefaultReservoirSize,
		buckets:       make([]int64, 5), // 5 buckets
		bucketBounds:  []float64{bucket0, bucket1, bucket2, bucket3},
		randState:     1, // Initialize per-instance random state
	}
}

// Add records a latency sample in milliseconds.
// This is O(1) amortized and safe for concurrent use.
func (s *StreamingLatencyStats) Add(latencyMs float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.count++
	s.sum += latencyMs
	s.seen++

	if latencyMs < s.min {
		s.min = latencyMs
	}
	if latencyMs > s.max {
		s.max = latencyMs
	}

	// Update histogram bucket
	bucket := s.getBucketIndex(latencyMs)
	s.buckets[bucket]++

	// Reservoir sampling (Algorithm R)
	if len(s.reservoir) < s.reservoirSize {
		s.reservoir = append(s.reservoir, latencyMs)
	} else {
		// Replace with probability reservoirSize/seen
		j := s.fastRand() % uint64(s.seen)
		if j < uint64(s.reservoirSize) {
			s.reservoir[j] = latencyMs
		}
	}
}

// getBucketIndex returns the bucket index for a latency value.
func (s *StreamingLatencyStats) getBucketIndex(latencyMs float64) int {
	for i, bound := range s.bucketBounds {
		if latencyMs < bound {
			return i
		}
	}
	return len(s.bucketBounds) // Last bucket (2s+)
}

// fastRand returns a pseudo-random uint64 using xorshift.
// Not cryptographically secure, but fast and good enough for reservoir sampling.
// Uses per-instance state to avoid data races between multiple instances.
func (s *StreamingLatencyStats) fastRand() uint64 {
	// xorshift64*
	s.randState ^= s.randState >> 12
	s.randState ^= s.randState << 25
	s.randState ^= s.randState >> 27
	return s.randState * 0x2545F4914F6CDD1D
}

// GetStats returns the current latency statistics.
// This is O(reservoirSize * log(reservoirSize)) for percentile calculation.
func (s *StreamingLatencyStats) GetStats() *types.LatencyStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.count == 0 {
		return nil
	}

	// Copy reservoir for sorting (don't modify original)
	sorted := make([]float64, len(s.reservoir))
	copy(sorted, s.reservoir)
	sort.Float64s(sorted)

	stats := &types.LatencyStats{
		Count: int(s.count),
		Min:   s.min,
		Max:   s.max,
		Avg:   s.sum / float64(s.count),
		P50:   s.percentile(sorted, 0.50),
		P75:   s.percentile(sorted, 0.75),
		P90:   s.percentile(sorted, 0.90),
		P95:   s.percentile(sorted, 0.95),
		P99:   s.percentile(sorted, 0.99),
		Buckets: []types.LatencyBucket{
			{Label: "0-250ms", Count: int(s.buckets[0])},
			{Label: "250-500ms", Count: int(s.buckets[1])},
			{Label: "500-1s", Count: int(s.buckets[2])},
			{Label: "1-2s", Count: int(s.buckets[3])},
			{Label: "2s+", Count: int(s.buckets[4])},
		},
	}

	return stats
}

// percentile calculates the p-th percentile from a sorted slice.
func (s *StreamingLatencyStats) percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}

	// Linear interpolation
	idx := p * float64(len(sorted)-1)
	lower := int(idx)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}

	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

// Reset clears all statistics.
func (s *StreamingLatencyStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.count = 0
	s.sum = 0
	s.min = math.MaxFloat64
	s.max = 0
	s.reservoir = s.reservoir[:0]
	s.seen = 0
	for i := range s.buckets {
		s.buckets[i] = 0
	}
}

// Count returns the number of samples recorded.
func (s *StreamingLatencyStats) Count() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.count
}
