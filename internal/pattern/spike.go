package pattern

import (
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Spike implements a pattern with periodic traffic spikes.
type Spike struct {
	baselineRate  int
	spikeRate     int
	spikeDuration time.Duration
	spikeInterval time.Duration
	currentRate   int64 // atomic
}

// NewSpike creates a spike pattern.
// Runs at baselineRate normally, and spikeRate during spikes.
// Spikes occur every spikeInterval and last spikeDuration.
func NewSpike(baselineRate, spikeRate int, spikeDuration, spikeInterval time.Duration) *Spike {
	return &Spike{
		baselineRate:  baselineRate,
		spikeRate:     spikeRate,
		spikeDuration: spikeDuration,
		spikeInterval: spikeInterval,
		currentRate:   int64(baselineRate),
	}
}

// Name returns the pattern identifier.
func (s *Spike) Name() types.LoadPattern {
	return types.PatternSpike
}

// GetRate returns the rate based on whether we're in a spike window.
func (s *Spike) GetRate(elapsed time.Duration) int {
	// Determine position within the current interval
	positionInInterval := elapsed % s.spikeInterval

	// We're in a spike if we're in the last spikeDuration of the interval
	spikeStart := s.spikeInterval - s.spikeDuration

	var rate int
	if positionInInterval >= spikeStart {
		rate = s.spikeRate
	} else {
		rate = s.baselineRate
	}

	atomic.StoreInt64(&s.currentRate, int64(rate))
	return rate
}

// NeedsAdaptiveController returns false - spike pattern doesn't need external control.
func (s *Spike) NeedsAdaptiveController() bool {
	return false
}

// SetCurrentRate updates the rate (for dynamic control).
func (s *Spike) SetCurrentRate(rate int) {
	atomic.StoreInt64(&s.currentRate, int64(rate))
}

// GetCurrentRate returns the current rate.
func (s *Spike) GetCurrentRate() int {
	return int(atomic.LoadInt64(&s.currentRate))
}
