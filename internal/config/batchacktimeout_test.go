package config

import (
	"testing"
	"time"
)

// A bad value must fail at startup, not degrade into a zero timer. A zero timeout
// fires immediately, so every batch would report "ack timed out" and the run would
// look like a total chain stall caused by a typo in an env var.
func TestParseBatchAckTimeout(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{name: "seconds", in: "2s", want: 2 * time.Second},
		{name: "milliseconds", in: "1500ms", want: 1500 * time.Millisecond},
		{name: "the historical default", in: "30s", want: 30 * time.Second},
		{name: "zero rejected", in: "0s", wantErr: true},
		{name: "negative rejected", in: "-1s", wantErr: true},
		// A bare integer is the most likely operator mistake ("2" meaning 2s);
		// time.ParseDuration rejects it, and so must we rather than coercing.
		{name: "unitless rejected", in: "2", wantErr: true},
		{name: "garbage rejected", in: "soon", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBatchAckTimeout(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseBatchAckTimeout(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBatchAckTimeout(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseBatchAckTimeout(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
