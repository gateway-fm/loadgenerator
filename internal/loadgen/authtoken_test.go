package loadgen

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestL2AuthToken(t *testing.T) {
	dir := t.TempDir()

	// A credential mounted from a Kubernetes Secret arrives with a trailing
	// newline; sending it verbatim yields a token the edge rejects.
	withNewline := filepath.Join(dir, "token-newline")
	if err := os.WriteFile(withNewline, []byte("  secret32.public16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "token-empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "unconfigured", file: "", want: ""},
		{name: "trims surrounding whitespace", file: withNewline, want: "secret32.public16"},
		{name: "empty file yields no token", file: empty, want: ""},
		// A missing file must not stop the run: the failure then surfaces as the
		// edge's anonymous rate limit, which is a legible symptom, rather than as
		// a crash loop.
		{name: "missing file yields no token", file: filepath.Join(dir, "absent"), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l2AuthToken(&config.Config{L2AuthTokenFile: tt.file}, discardLogger())
			if got != tt.want {
				t.Errorf("l2AuthToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An unset config must leave DefaultClientConfig alone: these overrides exist for
// proxy-edge runs, and a devnet run that does not set them has to keep sending
// exactly as it did before.
func TestApplyL2ClientTuning(t *testing.T) {
	base := rpc.DefaultClientConfig("http://example.invalid")

	tests := []struct {
		name        string
		cfg         config.Config
		wantTimeout time.Duration
		wantRetries int
	}{
		{
			name:        "unset keeps defaults",
			cfg:         config.Config{},
			wantTimeout: base.Timeout,
			wantRetries: base.MaxRetries,
		},
		{
			name:        "timeout only",
			cfg:         config.Config{L2ClientTimeout: 30 * time.Second},
			wantTimeout: 30 * time.Second,
			wantRetries: base.MaxRetries,
		},
		{
			// 0 is the field's zero value, so it must mean "keep the default"
			// rather than "no retries" -- otherwise an unset config silently
			// changes send behaviour.
			name:        "zero retries keeps default",
			cfg:         config.Config{L2ClientMaxRetries: 0},
			wantTimeout: base.Timeout,
			wantRetries: base.MaxRetries,
		},
		{
			name:        "single attempt",
			cfg:         config.Config{L2ClientTimeout: 45 * time.Second, L2ClientMaxRetries: 1},
			wantTimeout: 45 * time.Second,
			wantRetries: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rpc.DefaultClientConfig("http://example.invalid")
			applyL2ClientTuning(&tt.cfg, &got)
			if got.Timeout != tt.wantTimeout {
				t.Errorf("Timeout = %v, want %v", got.Timeout, tt.wantTimeout)
			}
			if got.MaxRetries != tt.wantRetries {
				t.Errorf("MaxRetries = %d, want %d", got.MaxRetries, tt.wantRetries)
			}
		})
	}
}
