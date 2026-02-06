package pattern

import (
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestConstantPattern(t *testing.T) {
	p := NewConstant(500)

	if p.Name() != types.PatternConstant {
		t.Errorf("expected name %s, got %s", types.PatternConstant, p.Name())
	}

	// Rate should be constant regardless of elapsed time
	testCases := []time.Duration{
		0,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
	}

	for _, elapsed := range testCases {
		rate := p.GetRate(elapsed)
		if rate != 500 {
			t.Errorf("at %v: expected rate 500, got %d", elapsed, rate)
		}
	}

	if p.NeedsAdaptiveController() {
		t.Error("constant pattern should not need adaptive controller")
	}
}

func TestRampPattern(t *testing.T) {
	duration := 60 * time.Second
	p := NewRamp(100, 1000, duration)

	if p.Name() != types.PatternRamp {
		t.Errorf("expected name %s, got %s", types.PatternRamp, p.Name())
	}

	testCases := []struct {
		elapsed      time.Duration
		expectedRate int
	}{
		{0, 100},
		{30 * time.Second, 550},  // Midpoint
		{60 * time.Second, 1000}, // End
		{90 * time.Second, 1000}, // Past end, should stay at max
	}

	for _, tc := range testCases {
		rate := p.GetRate(tc.elapsed)
		// Allow some tolerance due to float math
		diff := rate - tc.expectedRate
		if diff < -10 || diff > 10 {
			t.Errorf("at %v: expected rate ~%d, got %d", tc.elapsed, tc.expectedRate, rate)
		}
	}

	if p.NeedsAdaptiveController() {
		t.Error("ramp pattern should not need adaptive controller")
	}
}

func TestSpikePattern(t *testing.T) {
	baseline := 100
	spike := 1000
	spikeDuration := 5 * time.Second
	spikeInterval := 15 * time.Second

	p := NewSpike(baseline, spike, spikeDuration, spikeInterval)

	if p.Name() != types.PatternSpike {
		t.Errorf("expected name %s, got %s", types.PatternSpike, p.Name())
	}

	testCases := []struct {
		elapsed      time.Duration
		expectedRate int
	}{
		{0, baseline},                   // Start of interval, baseline
		{5 * time.Second, baseline},     // Still baseline
		{9 * time.Second, baseline},     // Just before spike
		{10 * time.Second, spike},       // During spike (10-15s)
		{12 * time.Second, spike},       // Still in spike
		{14 * time.Second, spike},       // End of spike
		{15 * time.Second, baseline},    // New interval, back to baseline
		{25 * time.Second, spike},       // Second spike (25-30s)
	}

	for _, tc := range testCases {
		rate := p.GetRate(tc.elapsed)
		if rate != tc.expectedRate {
			t.Errorf("at %v: expected rate %d, got %d", tc.elapsed, tc.expectedRate, rate)
		}
	}

	if p.NeedsAdaptiveController() {
		t.Error("spike pattern should not need adaptive controller")
	}
}

func TestAdaptivePattern(t *testing.T) {
	initialRate := 100
	p := NewAdaptive(initialRate)

	if p.Name() != types.PatternAdaptive {
		t.Errorf("expected name %s, got %s", types.PatternAdaptive, p.Name())
	}

	// Initial rate
	if rate := p.GetRate(0); rate != initialRate {
		t.Errorf("expected initial rate %d, got %d", initialRate, rate)
	}

	if !p.NeedsAdaptiveController() {
		t.Error("adaptive pattern should need adaptive controller")
	}

	// Test setting rate externally (adaptive controller simulation)
	p.SetCurrentRate(500)
	if rate := p.GetRate(10 * time.Second); rate != 500 {
		t.Errorf("expected rate 500 after SetCurrentRate, got %d", rate)
	}

	if p.GetCurrentRate() != 500 {
		t.Errorf("expected GetCurrentRate 500, got %d", p.GetCurrentRate())
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	cfg := Config{
		Duration:     60 * time.Second,
		ConstantRate: 500,
		RampStart:    100,
		RampEnd:      1000,
		BaselineRate: 100,
		SpikeRate:    1000,
		SpikeDuration: 5 * time.Second,
		SpikeInterval: 15 * time.Second,
		AdaptiveInitialRate: 100,
	}

	// Test all patterns are registered
	patterns := []types.LoadPattern{
		types.PatternConstant,
		types.PatternRamp,
		types.PatternSpike,
		types.PatternAdaptive,
		types.PatternRealistic,
	}

	for _, name := range patterns {
		p, err := r.Get(name, cfg)
		if err != nil {
			t.Errorf("failed to get pattern %s: %v", name, err)
			continue
		}
		if p == nil {
			t.Errorf("pattern %s is nil", name)
		}
	}

	// Test unknown pattern
	_, err := r.Get("unknown", cfg)
	if err == nil {
		t.Error("expected error for unknown pattern")
	}
}
