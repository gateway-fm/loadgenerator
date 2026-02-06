package pattern

import (
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Adaptive implements an adaptive rate pattern that tries to maximize throughput.
// It requires an external adaptive controller to adjust the rate based on
// pending transaction count.
type Adaptive struct {
	initialRate int
	currentRate int64 // atomic - controlled externally
}

// NewAdaptive creates an adaptive pattern starting at initialRate.
// The rate will be adjusted by an external adaptive controller.
func NewAdaptive(initialRate int) *Adaptive {
	return &Adaptive{
		initialRate: initialRate,
		currentRate: int64(initialRate),
	}
}

// Name returns the pattern identifier.
func (a *Adaptive) Name() types.LoadPattern {
	return types.PatternAdaptive
}

// GetRate returns the current rate (set by adaptive controller).
func (a *Adaptive) GetRate(elapsed time.Duration) int {
	return int(atomic.LoadInt64(&a.currentRate))
}

// NeedsAdaptiveController returns true - adaptive pattern needs external rate control.
func (a *Adaptive) NeedsAdaptiveController() bool {
	return true
}

// SetCurrentRate updates the rate (called by adaptive controller).
func (a *Adaptive) SetCurrentRate(rate int) {
	atomic.StoreInt64(&a.currentRate, int64(rate))
}

// GetCurrentRate returns the current rate.
func (a *Adaptive) GetCurrentRate() int {
	return int(atomic.LoadInt64(&a.currentRate))
}

// GetInitialRate returns the initial rate.
func (a *Adaptive) GetInitialRate() int {
	return a.initialRate
}
