package execnode

import (
	"testing"
)

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()

	tests := []struct {
		name     string
		expected *ExecutionLayerCapabilities
	}{
		{"reth", RethCapabilities()},
		{"op-reth", OpRethCapabilities()},
		{"gravity-reth", GravityRethCapabilities()},
		{"cdk-erigon", CDKErigonCapabilities()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := r.Get(tt.name)
			if caps == nil {
				t.Fatalf("expected %s to be registered, got nil", tt.name)
			}
			if caps.Name != tt.expected.Name {
				t.Errorf("Name mismatch: got %s, want %s", caps.Name, tt.expected.Name)
			}
			if caps.HasExternalBlockBuilder != tt.expected.HasExternalBlockBuilder {
				t.Errorf("HasExternalBlockBuilder mismatch for %s: got %v, want %v",
					tt.name, caps.HasExternalBlockBuilder, tt.expected.HasExternalBlockBuilder)
			}
			if caps.SupportsPreconfirmations != tt.expected.SupportsPreconfirmations {
				t.Errorf("SupportsPreconfirmations mismatch for %s: got %v, want %v",
					tt.name, caps.SupportsPreconfirmations, tt.expected.SupportsPreconfirmations)
			}
		})
	}
}

func TestRegistryUnknown(t *testing.T) {
	r := DefaultRegistry()
	caps := r.Get("unknown-node")
	if caps != nil {
		t.Errorf("expected nil for unknown node, got %+v", caps)
	}
}

func TestRegistryRegisterCustom(t *testing.T) {
	r := NewRegistry()

	custom := &ExecutionLayerCapabilities{
		Name:                     "custom-node",
		HasExternalBlockBuilder:  true,
		SupportsPreconfirmations: false,
		SupportsBuilderStatusAPI: true,
		SupportsBlockMetricsWS:   false,
	}

	r.Register(custom)
	caps := r.Get("custom-node")
	if caps == nil {
		t.Fatal("expected custom-node to be registered")
	}
	if caps.Name != "custom-node" {
		t.Errorf("Name mismatch: got %s, want custom-node", caps.Name)
	}
	if !caps.HasExternalBlockBuilder {
		t.Error("HasExternalBlockBuilder should be true")
	}
	if caps.SupportsPreconfirmations {
		t.Error("SupportsPreconfirmations should be false")
	}
}

func TestRethAliasEqualsOpReth(t *testing.T) {
	reth := RethCapabilities()
	opReth := OpRethCapabilities()

	// Name differs (that's the alias)
	if reth.Name != "reth" {
		t.Errorf("reth Name should be 'reth', got %s", reth.Name)
	}
	if opReth.Name != "op-reth" {
		t.Errorf("op-reth Name should be 'op-reth', got %s", opReth.Name)
	}

	// But capabilities should match
	if reth.HasExternalBlockBuilder != opReth.HasExternalBlockBuilder {
		t.Error("reth and op-reth should have same HasExternalBlockBuilder")
	}
	if reth.SupportsPreconfirmations != opReth.SupportsPreconfirmations {
		t.Error("reth and op-reth should have same SupportsPreconfirmations")
	}
	if reth.SupportsBuilderStatusAPI != opReth.SupportsBuilderStatusAPI {
		t.Error("reth and op-reth should have same SupportsBuilderStatusAPI")
	}
	if reth.SupportsBlockMetricsWS != opReth.SupportsBlockMetricsWS {
		t.Error("reth and op-reth should have same SupportsBlockMetricsWS")
	}
}

func TestCapabilitiesString(t *testing.T) {
	caps := OpRethCapabilities()
	if caps.String() != "op-reth" {
		t.Errorf("String() should return 'op-reth', got %s", caps.String())
	}

	var nilCaps *ExecutionLayerCapabilities
	if nilCaps.String() != "unknown" {
		t.Errorf("nil.String() should return 'unknown', got %s", nilCaps.String())
	}
}

func TestRegistryNames(t *testing.T) {
	r := DefaultRegistry()
	names := r.Names()

	if len(names) != 4 {
		t.Errorf("expected 4 registered names, got %d", len(names))
	}

	// Convert to map for easier lookup
	nameMap := make(map[string]bool)
	for _, n := range names {
		nameMap[n] = true
	}

	expected := []string{"reth", "op-reth", "gravity-reth", "cdk-erigon"}
	for _, e := range expected {
		if !nameMap[e] {
			t.Errorf("expected %s to be in registry names", e)
		}
	}
}
