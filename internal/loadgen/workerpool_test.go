package loadgen

import (
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/config"
)

// The pool bounds throughput on a no-mempool chain -- workers are pinned
// one-per-account and gated on their own account's ack, so aggregate is
// workers/ack_latency. It therefore has to be raisable, and the 4x concurrency
// invariant has to follow it: at 1x a single round of concurrent batches
// saturates the semaphore and serialises sending.
func TestSenderWorkerPool(t *testing.T) {
	tests := []struct {
		name            string
		cfg             *config.Config
		wantPool        int
		wantConcurrency int
	}{
		// Unset must reproduce the previous literals exactly: 4000 and 16000.
		{name: "unset", cfg: &config.Config{}, wantPool: 4000, wantConcurrency: 16000},
		{name: "zero", cfg: &config.Config{L2MaxSenderWorkers: 0}, wantPool: 4000, wantConcurrency: 16000},
		{name: "negative", cfg: &config.Config{L2MaxSenderWorkers: -5}, wantPool: 4000, wantConcurrency: 16000},
		{name: "raised", cfg: &config.Config{L2MaxSenderWorkers: 8000}, wantPool: 8000, wantConcurrency: 32000},
		{name: "nil config", cfg: nil, wantPool: 4000, wantConcurrency: 16000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := senderWorkerPool(tt.cfg); got != tt.wantPool {
				t.Errorf("senderWorkerPool() = %d, want %d", got, tt.wantPool)
			}
			if got := senderConcurrency(tt.cfg); got != tt.wantConcurrency {
				t.Errorf("senderConcurrency() = %d, want %d", got, tt.wantConcurrency)
			}
			if senderConcurrency(tt.cfg) != senderWorkerPool(tt.cfg)*4 {
				t.Error("concurrency must stay 4x the pool")
			}
		})
	}
}

// Depth 1 is strict per-account serialisation and MUST stay the default: on a
// chain with no mempool a nonce arriving early is refused, not queued, so any
// value above 1 is opt-in and depends on the chain parking too-high nonces.
func TestPipelineBatchDepth(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *config.Config
		want int
	}{
		{name: "nil config", cfg: nil, want: 1},
		{name: "unset", cfg: &config.Config{}, want: 1},
		{name: "zero", cfg: &config.Config{L2PipelineBatchDepth: 0}, want: 1},
		{name: "one", cfg: &config.Config{L2PipelineBatchDepth: 1}, want: 1},
		{name: "negative clamps to serial", cfg: &config.Config{L2PipelineBatchDepth: -3}, want: 1},
		{name: "two", cfg: &config.Config{L2PipelineBatchDepth: 2}, want: 2},
		{name: "four", cfg: &config.Config{L2PipelineBatchDepth: 4}, want: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := pipelineBatchDepth(tt.cfg); got != tt.want {
				t.Errorf("pipelineBatchDepth() = %d, want %d", got, tt.want)
			}
		})
	}
}
