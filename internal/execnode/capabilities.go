// Package execnode provides execution layer capability definitions and registry.
// This allows the load-generator to adapt to different execution layer nodes
// (op-reth, gravity-reth, cdk-erigon, etc.) without scattered conditionals.
package execnode

// ExecutionLayerCapabilities defines what features an execution layer supports.
// This enables capability-based conditionals instead of string-matching node names.
type ExecutionLayerCapabilities struct {
	// Name is the canonical identifier for this execution layer (e.g., "op-reth", "cdk-erigon")
	Name string

	// HasExternalBlockBuilder indicates whether transactions go through an external
	// block-builder service (true) or directly to the node's RPC (false).
	// When true: load-generator -> block-builder:13000 -> node Engine API
	// When false: load-generator -> node:8545 (direct sequencer)
	HasExternalBlockBuilder bool

	// SupportsPreconfirmations indicates whether the node/builder provides
	// WebSocket preconfirmation events for transaction inclusion.
	SupportsPreconfirmations bool

	// SupportsBuilderStatusAPI indicates whether the block-builder's /status
	// endpoint is available for fetching builder configuration and metrics.
	SupportsBuilderStatusAPI bool

	// SupportsBlockMetricsWS indicates whether the block-builder's
	// /ws/block-metrics WebSocket endpoint is available.
	SupportsBlockMetricsWS bool
}

// String returns the canonical name of the execution layer.
func (c *ExecutionLayerCapabilities) String() string {
	if c == nil {
		return "unknown"
	}
	return c.Name
}
