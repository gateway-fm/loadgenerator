package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Authorization header is what a rate-limited proxy edge classifies a caller
// on, so both request paths must carry it: Call (single) and the batch path the
// hot send loop actually uses. Equally, an unkeyed run must send no header at
// all, or previously published throughput figures stop meaning what they meant.
func TestHTTPClientAuthorizationHeader(t *testing.T) {
	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbb"

	tokenCases := []struct {
		name      string
		authToken string
		wantAuth  string
		wantSet   bool
	}{
		{name: "token configured", authToken: token, wantAuth: "Bearer " + token, wantSet: true},
		{name: "no token", authToken: "", wantSet: false},
	}

	// batch=true drives SendRawTransactionBatch, which is the load generator's
	// real submission path (BatchCall -> doBatchRequest), not a synthetic call.
	paths := []struct {
		name  string
		batch bool
		body  string
		call  func(context.Context, *HTTPClient) error
	}{
		{
			name: "Call",
			body: `{"jsonrpc":"2.0","id":1,"result":"0x1"}`,
			call: func(ctx context.Context, c *HTTPClient) error {
				_, err := c.Call(ctx, "eth_blockNumber", nil)
				return err
			},
		},
		{
			name:  "SendRawTransactionBatch",
			batch: true,
			body:  `[{"jsonrpc":"2.0","id":1,"result":"0xdeadbeef"}]`,
			call: func(ctx context.Context, c *HTTPClient) error {
				for _, err := range c.SendRawTransactionBatch(ctx, [][]byte{{0x01, 0x02}}) {
					if err != nil {
						return err
					}
				}
				return nil
			},
		},
	}

	for _, tc := range tokenCases {
		for _, p := range paths {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				var gotAuth string
				var gotSet bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotAuth = r.Header.Get("Authorization")
					_, gotSet = r.Header["Authorization"]
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(p.body))
				}))
				defer srv.Close()

				cfg := DefaultClientConfig(srv.URL)
				cfg.AuthToken = tc.authToken
				c := NewHTTPClient(cfg)

				if err := p.call(context.Background(), c); err != nil {
					t.Fatalf("request failed: %v", err)
				}
				if gotSet != tc.wantSet {
					t.Fatalf("Authorization header present = %v, want %v (got %q)", gotSet, tc.wantSet, gotAuth)
				}
				if gotAuth != tc.wantAuth {
					t.Errorf("Authorization = %q, want %q", gotAuth, tc.wantAuth)
				}
			})
		}
	}
}
