package loadgen

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/config"
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
