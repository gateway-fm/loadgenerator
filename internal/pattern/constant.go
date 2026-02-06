package pattern

import (
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Constant implements a fixed-rate load pattern.
type Constant struct {
	rate int64 // atomic for thread safety
}

// NewConstant creates a constant rate pattern.
func NewConstant(rate int) *Constant {
	return &Constant{
		rate: int64(rate),
	}
}

// Name returns the pattern identifier.
func (c *Constant) Name() types.LoadPattern {
	return types.PatternConstant
}

// GetRate returns the constant rate regardless of elapsed time.
func (c *Constant) GetRate(elapsed time.Duration) int {
	return int(atomic.LoadInt64(&c.rate))
}

// NeedsAdaptiveController returns false - constant rate doesn't need adaptation.
func (c *Constant) NeedsAdaptiveController() bool {
	return false
}

// SetCurrentRate updates the rate (for dynamic control).
func (c *Constant) SetCurrentRate(rate int) {
	atomic.StoreInt64(&c.rate, int64(rate))
}

// GetCurrentRate returns the current rate.
func (c *Constant) GetCurrentRate() int {
	return int(atomic.LoadInt64(&c.rate))
}
