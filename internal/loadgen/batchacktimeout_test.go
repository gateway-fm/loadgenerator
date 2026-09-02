package loadgen

import (
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/config"
)

// Every throughput figure published from this generator so far was measured at the
// 30s default, so an unset config MUST still resolve to exactly 30s -- otherwise
// the new flag silently reinterprets the existing baseline.
func TestBatchAckTimeoutFor(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{name: "unset keeps the historical default", cfg: &config.Config{}, want: defaultBatchAckTimeout},
		{name: "nil config keeps the default", cfg: nil, want: defaultBatchAckTimeout},
		// Config rejects non-positive values, but this must not panic or return a
		// zero timer if one ever reaches it by another path -- a zero timer fires
		// immediately and would stall every account.
		{name: "zero keeps the default", cfg: &config.Config{L2BatchAckTimeout: 0}, want: defaultBatchAckTimeout},
		{name: "negative keeps the default", cfg: &config.Config{L2BatchAckTimeout: -time.Second}, want: defaultBatchAckTimeout},
		{name: "configured value wins", cfg: &config.Config{L2BatchAckTimeout: 2 * time.Second}, want: 2 * time.Second},
		{name: "sub-second allowed", cfg: &config.Config{L2BatchAckTimeout: 250 * time.Millisecond}, want: 250 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := batchAckTimeoutFor(tt.cfg); got != tt.want {
				t.Errorf("batchAckTimeoutFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Guards the documented default against a careless edit: the whole point of the
// change is that the timeout is now tunable *without* moving the baseline.
func TestDefaultBatchAckTimeoutUnchanged(t *testing.T) {
	if defaultBatchAckTimeout != 30*time.Second {
		t.Errorf("defaultBatchAckTimeout = %v, want 30s", defaultBatchAckTimeout)
	}
}
