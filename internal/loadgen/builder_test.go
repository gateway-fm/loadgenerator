package loadgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/execnode"
)

func TestParseNodeName(t *testing.T) {
	lg := newTestLoadGenerator(t)

	tests := []struct {
		name           string
		version        string
		executionLayer string
		want           string
	}{
		{"op-reth version string", "reth/v1.9.3-op/linux-x86_64", "op-reth", "op-reth"},
		{"op-reth optimism tag", "reth/v1.9.3-optimism/linux", "op-reth", "op-reth"},
		{"gravity-reth explicit", "gravity-reth/v0.4.1/linux-x86_64", "gravity-reth", "gravity-reth"},
		{"gravity-reth in version", "reth/v0.4.1-gravity/linux-x86_64", "gravity-reth", "gravity-reth"},
		{"cdk-erigon", "erigon/2.60.0/linux-amd64", "cdk-erigon", "cdk-erigon"},
		{"cdk tag only", "cdk-something/v1.0", "cdk-erigon", "cdk-erigon"},
		{"plain reth defaults to op-reth", "reth/v1.9.3/linux-x86_64", "reth", "op-reth"},
		{"plain reth with gravity-reth layer", "reth/v1.9.3/linux-x86_64", "gravity-reth", "gravity-reth"},
		{"unknown falls back to execution layer", "geth/v1.14.0/linux", "geth", "geth"},
		{"empty version falls back", "", "op-reth", "op-reth"},
		{"case insensitive gravity", "Reth/v0.4.1-GRAVITY/Linux", "gravity-reth", "gravity-reth"},
		{"case insensitive op", "Reth/v1.9.3-OP/Linux", "op-reth", "op-reth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lg.parseNodeName(tt.version, tt.executionLayer)
			if got != tt.want {
				t.Errorf("parseNodeName(%q, %q) = %q, want %q", tt.version, tt.executionLayer, got, tt.want)
			}
		})
	}
}

func TestFetchNodeInfo(t *testing.T) {
	tests := []struct {
		name        string
		callFn      func(ctx context.Context, method string, params []interface{}) (json.RawMessage, error)
		wantVersion string
		wantChainID uint64
	}{
		{
			name: "both succeed",
			callFn: func(_ context.Context, method string, _ []interface{}) (json.RawMessage, error) {
				switch method {
				case "web3_clientVersion":
					return json.RawMessage(`"reth/v1.9.3-op/linux"`), nil
				case "eth_chainId":
					return json.RawMessage(`"0xa455"`), nil
				}
				return nil, nil
			},
			wantVersion: "reth/v1.9.3-op/linux",
			wantChainID: 42069,
		},
		{
			name: "version fails, chain succeeds",
			callFn: func(_ context.Context, method string, _ []interface{}) (json.RawMessage, error) {
				switch method {
				case "web3_clientVersion":
					return nil, fmt.Errorf("rpc error")
				case "eth_chainId":
					return json.RawMessage(`"0x1"`), nil
				}
				return nil, nil
			},
			wantVersion: "",
			wantChainID: 1,
		},
		{
			name: "both fail",
			callFn: func(_ context.Context, _ string, _ []interface{}) (json.RawMessage, error) {
				return nil, fmt.Errorf("connection refused")
			},
			wantVersion: "",
			wantChainID: 0,
		},
		{
			name: "nil l2Client",
			callFn: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.callFn != nil {
				opts = append(opts, WithL2Client(&mockRPCClient{CallFn: tt.callFn}))
			} else {
				lg := newTestLoadGenerator(t)
				lg.l2Client = nil
				v, c := lg.fetchNodeInfo(context.Background())
				if v != "" || c != 0 {
					t.Errorf("nil l2Client: got version=%q chainID=%d, want empty/0", v, c)
				}
				return
			}
			lg := newTestLoadGenerator(t, opts...)
			v, c := lg.fetchNodeInfo(context.Background())
			if v != tt.wantVersion {
				t.Errorf("version = %q, want %q", v, tt.wantVersion)
			}
			if c != tt.wantChainID {
				t.Errorf("chainID = %d, want %d", c, tt.wantChainID)
			}
		})
	}
}

func TestFetchBuilderPressure(t *testing.T) {
	tests := []struct {
		name            string
		statusCode      int
		body            string
		gasPrice        uint64
		baseFee         uint64
		wantPressure    float64
		wantBaseFee     float64
		wantGasPrice    float64
		wantGasUsed     uint64
		wantAttestation bool
		wantHSM         string
	}{
		{
			name:       "normal pressure",
			statusCode: http.StatusOK,
			body:       `{"pendingTxCount":25000,"maxTxsPerBlock":50000,"latestBaseFeeGwei":0.5,"latestGasUsed":42000000}`,
			wantPressure: 0.25,
			wantBaseFee:  0.5,
			wantGasUsed:  42000000,
		},
		{
			name:         "full pressure capped at 1.0",
			statusCode:   http.StatusOK,
			body:         `{"pendingTxCount":200000,"maxTxsPerBlock":50000,"latestBaseFeeGwei":1.0}`,
			wantPressure: 1.0,
			wantBaseFee:  1.0,
		},
		{
			name:         "zero capacity uses default",
			statusCode:   http.StatusOK,
			body:         `{"pendingTxCount":10000,"maxTxsPerBlock":0}`,
			wantPressure: 0.2,
		},
		{
			name:         "server error resets pressure",
			statusCode:   http.StatusInternalServerError,
			body:         `{}`,
			wantPressure: 0,
		},
		{
			name:            "attestation fields parsed",
			statusCode:      http.StatusOK,
			body:            `{"pendingTxCount":0,"maxTxsPerBlock":50000,"blockAttestationEnabled":true,"hsmProvider":"aws-kms","hsmKeyIdActive":"key-123","hsmFailoverEnabled":true}`,
			wantPressure:    0,
			wantAttestation: true,
			wantHSM:         "aws-kms",
		},
		{
			name:       "l2 baseFee overrides builder",
			statusCode: http.StatusOK,
			body:       `{"pendingTxCount":0,"maxTxsPerBlock":50000,"latestBaseFeeGwei":0.5}`,
			baseFee:    2000000000,
			wantBaseFee: 2.0,
		},
		{
			name:         "l2 gasPrice reported",
			statusCode:   http.StatusOK,
			body:         `{"pendingTxCount":0,"maxTxsPerBlock":50000}`,
			gasPrice:     3000000000,
			wantGasPrice: 3.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/status" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			l2Mock := &mockRPCClient{
				GetGasPriceFn: func(_ context.Context) (uint64, error) {
					return tt.gasPrice, nil
				},
				GetBaseFeeFn: func(_ context.Context) (uint64, error) {
					return tt.baseFee, nil
				},
			}

			lg := newTestLoadGenerator(t, WithL2Client(l2Mock))
			lg.cfg.BuilderRPCURL = srv.URL

			lg.fetchBuilderPressure()

			lg.builderPressureMu.RLock()
			defer lg.builderPressureMu.RUnlock()

			if diff := lg.builderPressure - tt.wantPressure; diff > 0.001 || diff < -0.001 {
				t.Errorf("pressure = %f, want %f", lg.builderPressure, tt.wantPressure)
			}
			if tt.wantBaseFee > 0 {
				if diff := lg.latestBaseFeeGwei - tt.wantBaseFee; diff > 0.001 || diff < -0.001 {
					t.Errorf("baseFeeGwei = %f, want %f", lg.latestBaseFeeGwei, tt.wantBaseFee)
				}
			}
			if tt.wantGasPrice > 0 {
				if diff := lg.latestGasPriceGwei - tt.wantGasPrice; diff > 0.001 || diff < -0.001 {
					t.Errorf("gasPriceGwei = %f, want %f", lg.latestGasPriceGwei, tt.wantGasPrice)
				}
			}
			if lg.latestGasUsed != tt.wantGasUsed {
				t.Errorf("gasUsed = %d, want %d", lg.latestGasUsed, tt.wantGasUsed)
			}
			if lg.blockAttestationEnabled != tt.wantAttestation {
				t.Errorf("attestationEnabled = %v, want %v", lg.blockAttestationEnabled, tt.wantAttestation)
			}
			if tt.wantHSM != "" && lg.hsmProvider != tt.wantHSM {
				t.Errorf("hsmProvider = %q, want %q", lg.hsmProvider, tt.wantHSM)
			}
		})
	}
}

func TestFetchBuilderPressure_UnreachableServer(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.cfg.BuilderRPCURL = "http://127.0.0.1:1"

	lg.builderPressureMu.Lock()
	lg.builderPressure = 0.5
	lg.builderPressureMu.Unlock()

	lg.fetchBuilderPressure()

	lg.builderPressureMu.RLock()
	defer lg.builderPressureMu.RUnlock()
	if lg.builderPressure != 0 {
		t.Errorf("pressure should reset to 0 on connection error, got %f", lg.builderPressure)
	}
}

func TestFetchBuilderConfig(t *testing.T) {
	t.Run("with builder status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/status" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"blockTimeMs":            1000,
					"gasLimit":               1000000000,
					"maxTxsPerBlock":         50000,
					"txOrdering":             "tip_desc",
					"enablePreconfirmations": true,
					"skipEmptyBlocks":        true,
					"includeDepositTx":       false,
					"blockAttestationEnabled": true,
					"hsmProvider":            "aws-kms",
					"hsmKeyIdActive":         "key-abc",
					"hsmFailoverEnabled":     true,
				})
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		l2Mock := &mockRPCClient{
			CallFn: func(_ context.Context, method string, _ []interface{}) (json.RawMessage, error) {
				switch method {
				case "web3_clientVersion":
					return json.RawMessage(`"reth/v1.9.3-op/linux-x86_64"`), nil
				case "eth_chainId":
					return json.RawMessage(`"0xa455"`), nil
				}
				return nil, nil
			},
		}

		lg := newTestLoadGenerator(t, WithL2Client(l2Mock))
		lg.cfg.BuilderRPCURL = srv.URL

		env := lg.fetchBuilderConfig()

		if env == nil {
			t.Fatal("expected non-nil environment snapshot")
		}
		if env.NodeName != "op-reth" {
			t.Errorf("NodeName = %q, want %q", env.NodeName, "op-reth")
		}
		if env.NodeVersion != "reth/v1.9.3-op/linux-x86_64" {
			t.Errorf("NodeVersion = %q", env.NodeVersion)
		}
		if env.ChainID != 42069 {
			t.Errorf("ChainID = %d, want 42069", env.ChainID)
		}
		if env.BuilderBlockTimeMs != 1000 {
			t.Errorf("BuilderBlockTimeMs = %d, want 1000", env.BuilderBlockTimeMs)
		}
		if env.BuilderGasLimit != 1000000000 {
			t.Errorf("BuilderGasLimit = %d", env.BuilderGasLimit)
		}
		if env.BuilderMaxTxsPerBlock != 50000 {
			t.Errorf("BuilderMaxTxsPerBlock = %d", env.BuilderMaxTxsPerBlock)
		}
		if env.BuilderTxOrdering != "tip_desc" {
			t.Errorf("BuilderTxOrdering = %q", env.BuilderTxOrdering)
		}
		if !env.BuilderEnablePreconfs {
			t.Error("expected BuilderEnablePreconfs=true")
		}
		if !env.BuilderSkipEmptyBlocks {
			t.Error("expected BuilderSkipEmptyBlocks=true")
		}
		if !env.BuilderBlockAttestationEnabled {
			t.Error("expected BuilderBlockAttestationEnabled=true")
		}
		if env.BuilderHSMProvider != "aws-kms" {
			t.Errorf("BuilderHSMProvider = %q", env.BuilderHSMProvider)
		}
		if env.BuilderHSMKeyIDActive != "key-abc" {
			t.Errorf("BuilderHSMKeyIDActive = %q", env.BuilderHSMKeyIDActive)
		}
		if !env.BuilderHSMFailoverEnabled {
			t.Error("expected BuilderHSMFailoverEnabled=true")
		}
		if !env.UseBlockBuilder {
			t.Error("expected UseBlockBuilder=true")
		}
		if lg.txOrdering != "tip_desc" {
			t.Errorf("lg.txOrdering = %q, want tip_desc", lg.txOrdering)
		}
	})

	t.Run("without builder status support", func(t *testing.T) {
		l2Mock := &mockRPCClient{
			CallFn: func(_ context.Context, method string, _ []interface{}) (json.RawMessage, error) {
				switch method {
				case "web3_clientVersion":
					return json.RawMessage(`"reth/v0.4.1-gravity/linux"`), nil
				case "eth_chainId":
					return json.RawMessage(`"0x1"`), nil
				}
				return nil, nil
			},
		}

		lg := newTestLoadGenerator(t, WithL2Client(l2Mock))
		lg.cfg.Capabilities = execnode.GravityRethCapabilities()
		lg.cfg.ExecutionLayer = "gravity-reth"

		env := lg.fetchBuilderConfig()

		if env.NodeName != "gravity-reth" {
			t.Errorf("NodeName = %q, want gravity-reth", env.NodeName)
		}
		if env.UseBlockBuilder {
			t.Error("expected UseBlockBuilder=false for gravity-reth")
		}
		if env.BuilderBlockTimeMs != 0 {
			t.Errorf("expected zero builder config, got BlockTimeMs=%d", env.BuilderBlockTimeMs)
		}
	})

	t.Run("builder returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL

		env := lg.fetchBuilderConfig()
		if env == nil {
			t.Fatal("expected non-nil env even on builder error")
		}
		if env.BuilderBlockTimeMs != 0 {
			t.Error("expected zero builder config on error")
		}
	})

	t.Run("gas config in snapshot", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/status" {
				w.Write([]byte(`{}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL
		lg.cfg.GasTipCap = 2000000000
		lg.cfg.GasFeeCap = 5000000000

		env := lg.fetchBuilderConfig()
		if env.LoadGenGasTipCapGwei != 2.0 {
			t.Errorf("LoadGenGasTipCapGwei = %f, want 2.0", env.LoadGenGasTipCapGwei)
		}
		if env.LoadGenGasFeeCapGwei != 5.0 {
			t.Errorf("LoadGenGasFeeCapGwei = %f, want 5.0", env.LoadGenGasFeeCapGwei)
		}
	})
}

func TestFetchHeaderAttestations(t *testing.T) {
	t.Run("collects attestations across block range", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(builderHeaderAttestationResponse{
				SchemaVersion: 1,
				Status:        "signed",
				Commitment: struct {
					BlockHash        string `json:"blockHash"`
					ParentHash       string `json:"parentHash"`
					StateRoot        string `json:"stateRoot"`
					ReceiptsRoot     string `json:"receiptsRoot"`
					BlockNumber      uint64 `json:"blockNumber"`
					Timestamp        uint64 `json:"timestamp"`
					GasUsed          uint64 `json:"gasUsed"`
					BaseFeePerGasWei any    `json:"baseFeePerGasWei"`
					SequencerAddress string `json:"sequencerAddress"`
				}{
					BlockHash:   "0xabc",
					BlockNumber: 100,
					GasUsed:     21000,
				},
				DigestHex:    "0xdigest",
				SignatureHex: "0xsig",
				KeyID:        "key-1",
				Provider:     "aws-kms",
				SignedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			})
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL

		result := lg.fetchHeaderAttestations(context.Background(), 100, 102)
		if len(result) != 3 {
			t.Fatalf("expected 3 attestations, got %d", len(result))
		}
		if result[0].Status != "signed" {
			t.Errorf("status = %q, want signed", result[0].Status)
		}
		if result[0].BlockHash != "0xabc" {
			t.Errorf("blockHash = %q", result[0].BlockHash)
		}
		if result[0].KeyID != "key-1" {
			t.Errorf("keyID = %q", result[0].KeyID)
		}
		if result[0].Provider != "aws-kms" {
			t.Errorf("provider = %q", result[0].Provider)
		}
	})

	t.Run("skips 404 blocks", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if r.URL.Path == "/block-attestations/101" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(builderHeaderAttestationResponse{
				Status: "signed",
				Commitment: struct {
					BlockHash        string `json:"blockHash"`
					ParentHash       string `json:"parentHash"`
					StateRoot        string `json:"stateRoot"`
					ReceiptsRoot     string `json:"receiptsRoot"`
					BlockNumber      uint64 `json:"blockNumber"`
					Timestamp        uint64 `json:"timestamp"`
					GasUsed          uint64 `json:"gasUsed"`
					BaseFeePerGasWei any    `json:"baseFeePerGasWei"`
					SequencerAddress string `json:"sequencerAddress"`
				}{BlockNumber: 100},
			})
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL

		result := lg.fetchHeaderAttestations(context.Background(), 100, 102)
		if len(result) != 2 {
			t.Errorf("expected 2 attestations (1 skipped 404), got %d", len(result))
		}
	})

	t.Run("returns nil for invalid range", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		if r := lg.fetchHeaderAttestations(context.Background(), 0, 10); r != nil {
			t.Error("expected nil for firstBlock=0")
		}
		if r := lg.fetchHeaderAttestations(context.Background(), 10, 0); r != nil {
			t.Error("expected nil for lastBlock=0")
		}
		if r := lg.fetchHeaderAttestations(context.Background(), 10, 5); r != nil {
			t.Error("expected nil for lastBlock < firstBlock")
		}
	})

	t.Run("returns nil when builder status not supported", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.cfg.Capabilities = execnode.GravityRethCapabilities()

		r := lg.fetchHeaderAttestations(context.Background(), 100, 102)
		if r != nil {
			t.Error("expected nil for unsupported builder")
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			json.NewEncoder(w).Encode(builderHeaderAttestationResponse{Status: "signed"})
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result := lg.fetchHeaderAttestations(ctx, 100, 200)
		if len(result) > 1 {
			t.Errorf("expected early exit on cancelled context, got %d results", len(result))
		}
	})
}

func TestResetBuilderNonces(t *testing.T) {
	t.Run("skips when no external builder", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.cfg.Capabilities = execnode.GravityRethCapabilities()

		if err := lg.resetBuilderNonces(); err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})

	t.Run("succeeds with 200 response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/reset-nonces" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL

		if err := lg.resetBuilderNonces(); err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})

	t.Run("returns error on non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		lg := newTestLoadGenerator(t)
		lg.cfg.BuilderRPCURL = srv.URL

		if err := lg.resetBuilderNonces(); err == nil {
			t.Error("expected error on 500 response")
		}
	})
}

func TestResyncAllNonces(t *testing.T) {
	t.Run("resyncs from builder", func(t *testing.T) {
		lg := newTestLoadGenerator(t)
		lg.nonceResyncNeeded = 1

		lg.resyncAllNonces()

		if lg.nonceResyncNeeded != 0 {
			t.Error("expected nonceResyncNeeded to be reset to 0")
		}
	})
}

func TestFetchBuilderConfig_LoadGenFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Write([]byte(`{"blockTimeMs":500}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	lg := newTestLoadGenerator(t)
	lg.cfg.BuilderRPCURL = srv.URL
	lg.cfg.ExecutionLayer = "op-reth"
	lg.cfg.GasTipCap = 1000000000
	lg.cfg.GasFeeCap = 0
	lg.cfg.Capabilities = &execnode.ExecutionLayerCapabilities{
		Name:                     "op-reth",
		HasExternalBlockBuilder:  true,
		SupportsBuilderStatusAPI: true,
	}

	env := lg.fetchBuilderConfig()
	if env.LoadGenExecutionLayer != "op-reth" {
		t.Errorf("LoadGenExecutionLayer = %q", env.LoadGenExecutionLayer)
	}
	if env.LoadGenGasTipCapGwei != 1.0 {
		t.Errorf("LoadGenGasTipCapGwei = %f, want 1.0", env.LoadGenGasTipCapGwei)
	}
}

func TestFetchBuilderPressure_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	lg := newTestLoadGenerator(t)
	lg.cfg.BuilderRPCURL = srv.URL

	lg.builderPressureMu.Lock()
	lg.builderPressure = 0.75
	lg.builderPressureMu.Unlock()

	lg.fetchBuilderPressure()

	lg.builderPressureMu.RLock()
	defer lg.builderPressureMu.RUnlock()
	if lg.builderPressure != 0.75 {
		t.Errorf("pressure should remain unchanged on invalid JSON, got %f", lg.builderPressure)
	}
}

func TestFetchBuilderConfig_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Write([]byte(`{invalid`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	lg := newTestLoadGenerator(t)
	lg.cfg.BuilderRPCURL = srv.URL
	lg.cfg.Capabilities = &execnode.ExecutionLayerCapabilities{
		Name:                     "op-reth",
		HasExternalBlockBuilder:  true,
		SupportsBuilderStatusAPI: true,
	}

	env := lg.fetchBuilderConfig()
	if env == nil {
		t.Fatal("expected non-nil env even with invalid JSON")
	}
	if env.BuilderBlockTimeMs != 0 {
		t.Error("expected zero builder config on JSON parse error")
	}
}

func TestFetchBuilderConfig_StoresIncludeDepositTx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"txOrdering":       "fifo",
				"includeDepositTx": true,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	lg := newTestLoadGenerator(t)
	lg.cfg.BuilderRPCURL = srv.URL
	lg.cfg.Capabilities = &execnode.ExecutionLayerCapabilities{
		Name:                     "op-reth",
		HasExternalBlockBuilder:  true,
		SupportsBuilderStatusAPI: true,
	}

	env := lg.fetchBuilderConfig()
	if !env.BuilderIncludeDepositTx {
		t.Error("expected BuilderIncludeDepositTx=true")
	}
	if lg.txOrdering != "fifo" {
		t.Errorf("lg.txOrdering = %q, want fifo", lg.txOrdering)
	}
	if !lg.includeDepositTx {
		t.Error("expected lg.includeDepositTx=true")
	}
}

func TestFetchNodeInfo_ChainIDFormats(t *testing.T) {
	tests := []struct {
		name    string
		chainID string
		want    uint64
	}{
		{"hex with 0x prefix", `"0xa455"`, 42069},
		{"hex chain 1", `"0x1"`, 1},
		{"large hex", `"0xaa36a7"`, 11155111},
		{"missing 0x prefix ignored", `"ff"`, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l2Mock := &mockRPCClient{
				CallFn: func(_ context.Context, method string, _ []interface{}) (json.RawMessage, error) {
					if method == "eth_chainId" {
						return json.RawMessage(tt.chainID), nil
					}
					return nil, nil
				},
			}
			lg := newTestLoadGenerator(t, WithL2Client(l2Mock))
			_, chainID := lg.fetchNodeInfo(context.Background())
			if chainID != tt.want {
				t.Errorf("chainID = %d, want %d", chainID, tt.want)
			}
		})
	}
}

// Ensure config.Config is used correctly (compile-time check)
var _ = &config.Config{}

// TestInitializeNonces_DowngradesAfterFailedTest is the regression test for
// RD-892. Before the fix, Account.Resync used a "set if higher" guard that
// kept a previous failed test's inflated local nonce instead of downgrading
// to the builder's view, requiring a process restart to recover.
//
// This test simulates that exact scenario: local nonce sits at 1000 (from
// a hypothetical prior catastrophic test), builder's pending view says 50.
// After InitializeNonces, the local nonce MUST equal 50.
func TestInitializeNonces_DowngradesAfterFailedTest(t *testing.T) {
	mgr, err := account.NewManager(big.NewInt(42069), big.NewInt(1e9), false, slog.Default())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	accounts := mgr.GetAccounts()
	if len(accounts) == 0 {
		t.Fatal("manager has no built-in accounts")
	}

	const inflated = uint64(1000)
	const chainPending = uint64(50)
	for _, a := range accounts {
		a.SetNonce(inflated)
	}

	mockBuilder := &mockRPCClient{
		GetNonceFn: func(_ context.Context, _ string) (uint64, error) {
			return chainPending, nil
		},
	}

	if err := mgr.InitializeNonces(context.Background(), mockBuilder, len(accounts)); err != nil {
		t.Fatalf("InitializeNonces: %v", err)
	}

	for i, a := range accounts {
		if got := a.PeekNonce(); got != chainPending {
			t.Errorf("account[%d].PeekNonce() = %d after InitializeNonces; want %d (downgrade from inflated %d)", i, got, chainPending, inflated)
		}
	}
}
