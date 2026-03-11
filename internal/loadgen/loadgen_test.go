package loadgen

import (
	"log/slog"
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/execnode"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// newTestLoadGenerator creates a LoadGenerator with all mock dependencies
// for unit testing. Override specific mocks by passing additional options.
func newTestLoadGenerator(t *testing.T, opts ...Option) *LoadGenerator {
	t.Helper()

	cfg := &config.Config{
		BuilderRPCURL:  "http://mock-builder:3000",
		L2RPCURL:       "http://mock-l2:8545",
		PreconfWSURL:   "",
		ChainID:        42069,
		GasPrice:       1000000000,
		GasTipCap:      1000000000,
		GasLimit:       21000,
		BlockTimeMS:    150,
		ExecutionLayer: "op-reth",
		Capabilities:   execnode.DefaultRegistry().Get("op-reth"),
	}

	mockStore := &mockStorage{}
	logger := slog.Default()

	defaultOpts := []Option{
		WithBuilderClient(&mockRPCClient{}),
		WithL2Client(&mockRPCClient{}),
		WithMetricsCollector(metrics.NewInMemoryCollector(true)),
		WithSender(&mockSender{}),
		WithDeployer(&mockDeployer{}),
		WithAccountManager(&mockAccountManager{}),
	}

	// User-provided options override defaults
	allOpts := append(defaultOpts, opts...)

	lg, err := NewLoadGenerator(cfg, mockStore, logger, allOpts...)
	if err != nil {
		t.Fatalf("newTestLoadGenerator: %v", err)
	}
	return lg
}

func TestNewLoadGenerator_WithMocks(t *testing.T) {
	lg := newTestLoadGenerator(t)

	if lg == nil {
		t.Fatal("expected non-nil LoadGenerator")
	}
	if lg.status != types.StatusIdle {
		t.Errorf("expected status %q, got %q", types.StatusIdle, lg.status)
	}
	if lg.builderClient == nil {
		t.Error("expected builderClient to be set")
	}
	if lg.l2Client == nil {
		t.Error("expected l2Client to be set")
	}
	if lg.metricsCol == nil {
		t.Error("expected metricsCol to be set")
	}
	if lg.sender == nil {
		t.Error("expected sender to be set")
	}
	if lg.deployer == nil {
		t.Error("expected deployer to be set")
	}
	if lg.accountMgr == nil {
		t.Error("expected accountMgr to be set")
	}
}

func TestNewLoadGenerator_OptionOverridesDefault(t *testing.T) {
	customClient := &mockRPCClient{}
	lg := newTestLoadGenerator(t, WithBuilderClient(customClient))

	if lg.builderClient != customClient {
		t.Error("expected custom builder client to override default")
	}
}
