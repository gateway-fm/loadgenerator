package rpc

import (
	"testing"
	"time"
)

func TestRPCError(t *testing.T) {
	err := &RPCError{Code: -32000, Message: "nonce too low"}

	// Test Error() method
	errStr := err.Error()
	if errStr != "RPC error -32000: nonce too low" {
		t.Errorf("RPCError.Error() = %q, want %q", errStr, "RPC error -32000: nonce too low")
	}

	// Test isRPCError
	if !isRPCError(err) {
		t.Error("isRPCError should return true for *RPCError")
	}
}

func TestHTTPStatusError(t *testing.T) {
	tests := []struct {
		name       string
		err        HTTPStatusError
		wantString string
		wantRetry  bool
	}{
		{
			name:       "429 Too Many Requests",
			err:        HTTPStatusError{StatusCode: 429, Body: "rate limited"},
			wantString: "HTTP 429: Too Many Requests (body: rate limited)",
			wantRetry:  true,
		},
		{
			name:       "502 Bad Gateway",
			err:        HTTPStatusError{StatusCode: 502},
			wantString: "HTTP 502: Bad Gateway",
			wantRetry:  true,
		},
		{
			name:       "503 Service Unavailable",
			err:        HTTPStatusError{StatusCode: 503},
			wantString: "HTTP 503: Service Unavailable",
			wantRetry:  true,
		},
		{
			name:       "504 Gateway Timeout",
			err:        HTTPStatusError{StatusCode: 504},
			wantString: "HTTP 504: Gateway Timeout",
			wantRetry:  true,
		},
		{
			name:       "400 Bad Request not retryable",
			err:        HTTPStatusError{StatusCode: 400, Body: "invalid request"},
			wantString: "HTTP 400: Bad Request (body: invalid request)",
			wantRetry:  false,
		},
		{
			name:       "500 Internal Server Error not retryable",
			err:        HTTPStatusError{StatusCode: 500},
			wantString: "HTTP 500: Internal Server Error",
			wantRetry:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantString {
				t.Errorf("HTTPStatusError.Error() = %q, want %q", got, tt.wantString)
			}
			if got := tt.err.IsRetryable(); got != tt.wantRetry {
				t.Errorf("HTTPStatusError.IsRetryable() = %v, want %v", got, tt.wantRetry)
			}
		})
	}
}

func TestIsRetryableHTTPError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantBool bool
	}{
		{
			name:     "retryable HTTP error",
			err:      &HTTPStatusError{StatusCode: 429},
			wantBool: true,
		},
		{
			name:     "non-retryable HTTP error",
			err:      &HTTPStatusError{StatusCode: 400},
			wantBool: false,
		},
		{
			name:     "RPC error",
			err:      &RPCError{Code: -32000, Message: "test"},
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableHTTPError(tt.err); got != tt.wantBool {
				t.Errorf("isRetryableHTTPError() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestGetRetryDelay(t *testing.T) {
	defaultBackoff := 100 * time.Millisecond

	tests := []struct {
		name      string
		err       error
		wantDelay time.Duration
	}{
		{
			name:      "HTTP error with Retry-After",
			err:       &HTTPStatusError{StatusCode: 429, RetryAfter: 2 * time.Second},
			wantDelay: 2 * time.Second,
		},
		{
			name:      "HTTP error without Retry-After",
			err:       &HTTPStatusError{StatusCode: 503},
			wantDelay: defaultBackoff,
		},
		{
			name:      "RPC error uses default",
			err:       &RPCError{Code: -32000, Message: "test"},
			wantDelay: defaultBackoff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getRetryDelay(tt.err, defaultBackoff); got != tt.wantDelay {
				t.Errorf("getRetryDelay() = %v, want %v", got, tt.wantDelay)
			}
		})
	}
}

func TestDefaultClientConfig(t *testing.T) {
	url := "http://localhost:8545"
	cfg := DefaultClientConfig(url)

	if cfg.URL != url {
		t.Errorf("URL = %q, want %q", cfg.URL, url)
	}
	if cfg.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want %v", cfg.Timeout, 2*time.Second)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.InitialBackoff != 100*time.Millisecond {
		t.Errorf("InitialBackoff = %v, want 100ms", cfg.InitialBackoff)
	}
	if cfg.MaxBackoff != 500*time.Millisecond {
		t.Errorf("MaxBackoff = %v, want 500ms", cfg.MaxBackoff)
	}
}

func TestTransactionIsDeposit(t *testing.T) {
	tests := []struct {
		name     string
		tx       Transaction
		wantBool bool
	}{
		{
			name:     "deposit transaction type 126",
			tx:       Transaction{Type: 126},
			wantBool: true,
		},
		{
			name:     "legacy transaction type 0",
			tx:       Transaction{Type: 0},
			wantBool: false,
		},
		{
			name:     "EIP-1559 transaction type 2",
			tx:       Transaction{Type: 2},
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tx.IsDeposit(); got != tt.wantBool {
				t.Errorf("Transaction.IsDeposit() = %v, want %v", got, tt.wantBool)
			}
		})
	}
}
