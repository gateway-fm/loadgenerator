package loadgen

import (
	"os"
	"testing"
)

func TestParseHexUint64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint64
		wantErr bool
	}{
		{"with 0x prefix", "0xff", 255, false},
		{"without prefix", "ff", 255, false},
		{"zero", "0x0", 0, false},
		{"large number", "0x1234abcd", 0x1234abcd, false},
		{"block number", "0xa", 10, false},
		{"empty string", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHexUint64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseHexUint64(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseHexUint64(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		envVal     string
		defaultVal string
		want       string
	}{
		{"env set", "TEST_HELPERS_VAR", "from-env", "default", "from-env"},
		{"env not set", "TEST_HELPERS_UNSET", "", "default", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				os.Setenv(tt.key, tt.envVal)
				defer os.Unsetenv(tt.key)
			}
			got := GetEnvOrDefault(tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("GetEnvOrDefault(%q, %q) = %q, want %q", tt.key, tt.defaultVal, got, tt.want)
			}
		})
	}
}

func TestGetEnvIntOrDefault(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		envVal     string
		defaultVal int
		want       int
	}{
		{"valid int", "TEST_INT_VAR", "42", 0, 42},
		{"not set", "TEST_INT_UNSET", "", 99, 99},
		{"invalid int", "TEST_INT_INVALID", "abc", 99, 99},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				os.Setenv(tt.key, tt.envVal)
				defer os.Unsetenv(tt.key)
			}
			got := GetEnvIntOrDefault(tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("GetEnvIntOrDefault(%q, %d) = %d, want %d", tt.key, tt.defaultVal, got, tt.want)
			}
		})
	}
}
