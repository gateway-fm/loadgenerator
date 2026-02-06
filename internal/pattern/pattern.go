// Package pattern provides load pattern implementations.
package pattern

import (
	"fmt"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Pattern calculates target TPS based on elapsed time.
type Pattern interface {
	// Name returns the pattern identifier.
	Name() types.LoadPattern

	// GetRate returns target TPS for the given elapsed time.
	GetRate(elapsed time.Duration) int

	// NeedsAdaptiveController returns true for patterns like "max" that need external rate control.
	NeedsAdaptiveController() bool

	// SetCurrentRate is called by adaptive controller to override rate (for max pattern).
	SetCurrentRate(rate int)

	// GetCurrentRate returns the current rate (may differ from GetRate for adaptive patterns).
	GetCurrentRate() int
}

// Config holds pattern-specific configuration.
type Config struct {
	Duration time.Duration

	// Constant pattern
	ConstantRate int

	// Ramp pattern
	RampStart int
	RampEnd   int
	RampSteps int

	// Spike pattern
	BaselineRate  int
	SpikeRate     int
	SpikeDuration time.Duration
	SpikeInterval time.Duration

	// Adaptive pattern
	AdaptiveInitialRate   int
	AdaptiveTargetPending int
	AdaptiveRateStep      int

	// Realistic pattern
	RealisticRate int
}

// Registry manages pattern lookup by name.
type Registry struct {
	patterns map[types.LoadPattern]func(Config) Pattern
}

// NewRegistry creates a new pattern registry with all built-in patterns.
func NewRegistry() *Registry {
	r := &Registry{
		patterns: make(map[types.LoadPattern]func(Config) Pattern),
	}

	r.Register(types.PatternConstant, func(cfg Config) Pattern {
		return NewConstant(cfg.ConstantRate)
	})
	r.Register(types.PatternRamp, func(cfg Config) Pattern {
		return NewRamp(cfg.RampStart, cfg.RampEnd, cfg.Duration)
	})
	r.Register(types.PatternSpike, func(cfg Config) Pattern {
		return NewSpike(cfg.BaselineRate, cfg.SpikeRate, cfg.SpikeDuration, cfg.SpikeInterval)
	})
	r.Register(types.PatternAdaptive, func(cfg Config) Pattern {
		return NewAdaptive(cfg.AdaptiveInitialRate)
	})
	r.Register(types.PatternRealistic, func(cfg Config) Pattern {
		return NewConstant(cfg.RealisticRate) // Realistic uses constant rate with mixed tx types
	})
	r.Register(types.PatternAdaptiveRealistic, func(cfg Config) Pattern {
		return NewAdaptive(cfg.AdaptiveInitialRate) // Adaptive-realistic uses adaptive rate with mixed tx types
	})

	return r
}

// Register adds a pattern factory to the registry.
func (r *Registry) Register(name types.LoadPattern, factory func(Config) Pattern) {
	r.patterns[name] = factory
}

// Get returns a pattern instance for the given name and config.
func (r *Registry) Get(name types.LoadPattern, cfg Config) (Pattern, error) {
	factory, ok := r.patterns[name]
	if !ok {
		return nil, fmt.Errorf("unknown pattern: %s", name)
	}
	return factory(cfg), nil
}
