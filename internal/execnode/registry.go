package execnode

import (
	"sync"
)

// Registry holds registered execution layer capability definitions.
// It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*ExecutionLayerCapabilities
}

// NewRegistry creates a new empty registry.
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]*ExecutionLayerCapabilities),
	}
}

// Register adds or updates an execution layer capability definition.
func (r *Registry) Register(caps *ExecutionLayerCapabilities) {
	if caps == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[caps.Name] = caps
}

// Get retrieves capabilities by name. Returns nil if not found.
func (r *Registry) Get(name string) *ExecutionLayerCapabilities {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[name]
}

// Names returns all registered execution layer names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry returns a registry pre-populated with built-in execution layers.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(OpRethCapabilities())
	r.Register(GravityRethCapabilities())
	r.Register(CDKErigonCapabilities())
	// Legacy alias: "reth" maps to op-reth
	r.Register(RethCapabilities())
	return r
}

// OpRethCapabilities returns the capabilities for op-reth with external block-builder.
// Architecture: load-generator -> block-builder:13000 -> op-reth Engine API:8551
func OpRethCapabilities() *ExecutionLayerCapabilities {
	return &ExecutionLayerCapabilities{
		Name:                     "op-reth",
		HasExternalBlockBuilder:  true,
		SupportsPreconfirmations: true,
		SupportsBuilderStatusAPI: true,
		SupportsBlockMetricsWS:   true,
	}
}

// RethCapabilities returns capabilities for the "reth" alias (same as op-reth).
// This provides backwards compatibility with EXECUTION_LAYER=reth.
func RethCapabilities() *ExecutionLayerCapabilities {
	caps := OpRethCapabilities()
	caps.Name = "reth"
	return caps
}

// GravityRethCapabilities returns the capabilities for gravity-reth (standalone sequencer).
// Architecture: load-generator -> gravity-reth:8545 (direct sequencer)
func GravityRethCapabilities() *ExecutionLayerCapabilities {
	return &ExecutionLayerCapabilities{
		Name:                     "gravity-reth",
		HasExternalBlockBuilder:  false,
		SupportsPreconfirmations: false,
		SupportsBuilderStatusAPI: false,
		SupportsBlockMetricsWS:   false,
	}
}

// CDKErigonCapabilities returns the capabilities for cdk-erigon (standalone sequencer).
// Architecture: load-generator -> cdk-erigon:8545 (direct sequencer)
func CDKErigonCapabilities() *ExecutionLayerCapabilities {
	return &ExecutionLayerCapabilities{
		Name:                     "cdk-erigon",
		HasExternalBlockBuilder:  false,
		SupportsPreconfirmations: false,
		SupportsBuilderStatusAPI: false,
		SupportsBlockMetricsWS:   false,
		RequiresLegacyTx:         true,
	}
}
