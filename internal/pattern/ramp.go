package pattern

import (
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Ramp implements a linearly increasing rate pattern.
type Ramp struct {
	startRate   int
	endRate     int
	duration    time.Duration
	currentRate int64 // atomic
}

// NewRamp creates a ramp pattern that increases from startRate to endRate over duration.
func NewRamp(startRate, endRate int, duration time.Duration) *Ramp {
	return &Ramp{
		startRate:   startRate,
		endRate:     endRate,
		duration:    duration,
		currentRate: int64(startRate),
	}
}

// Name returns the pattern identifier.
func (r *Ramp) Name() types.LoadPattern {
	return types.PatternRamp
}

// GetRate returns the rate based on linear interpolation of elapsed time.
func (r *Ramp) GetRate(elapsed time.Duration) int {
	if elapsed <= 0 {
		return r.startRate
	}
	if elapsed >= r.duration {
		return r.endRate
	}

	// Linear interpolation
	progress := float64(elapsed) / float64(r.duration)
	rate := float64(r.startRate) + progress*float64(r.endRate-r.startRate)
	currentRate := int(rate)

	atomic.StoreInt64(&r.currentRate, int64(currentRate))
	return currentRate
}

// NeedsAdaptiveController returns false - ramp pattern doesn't need external control.
func (r *Ramp) NeedsAdaptiveController() bool {
	return false
}

// SetCurrentRate updates the rate (for dynamic control).
func (r *Ramp) SetCurrentRate(rate int) {
	atomic.StoreInt64(&r.currentRate, int64(rate))
}

// GetCurrentRate returns the current rate.
func (r *Ramp) GetCurrentRate() int {
	return int(atomic.LoadInt64(&r.currentRate))
}
