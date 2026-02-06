package account

import (
	"log/slog"
	"math/big"
	"testing"
)

func TestExportDynamicAccountKeys(t *testing.T) {
	mgr, err := NewManager(big.NewInt(42069), big.NewInt(1e9), slog.Default())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Generate some accounts
	if err := mgr.GenerateDynamicAccounts(5); err != nil {
		t.Fatalf("GenerateDynamicAccounts: %v", err)
	}

	pairs := mgr.ExportDynamicAccountKeys()
	if len(pairs) != 5 {
		t.Fatalf("expected 5 pairs, got %d", len(pairs))
	}

	// Each pair should have a non-empty address and hex key
	for i, p := range pairs {
		if p.Address == "" {
			t.Errorf("pair[%d].Address is empty", i)
		}
		if p.PrivateKeyHex == "" {
			t.Errorf("pair[%d].PrivateKeyHex is empty", i)
		}
		// Verify we can reconstruct the account
		acc, err := NewAccountFromHex(p.PrivateKeyHex)
		if err != nil {
			t.Errorf("pair[%d]: NewAccountFromHex failed: %v", i, err)
			continue
		}
		if acc.Address.Hex() != p.Address {
			t.Errorf("pair[%d]: reconstructed address %s != exported %s", i, acc.Address.Hex(), p.Address)
		}
	}
}

func TestSetDynamicAccounts(t *testing.T) {
	mgr, err := NewManager(big.NewInt(42069), big.NewInt(1e9), slog.Default())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Generate accounts and export
	if err := mgr.GenerateDynamicAccounts(3); err != nil {
		t.Fatalf("GenerateDynamicAccounts: %v", err)
	}
	pairs := mgr.ExportDynamicAccountKeys()

	// Reconstruct accounts
	accs := make([]*Account, len(pairs))
	for i, p := range pairs {
		acc, err := NewAccountFromHex(p.PrivateKeyHex)
		if err != nil {
			t.Fatalf("NewAccountFromHex: %v", err)
		}
		accs[i] = acc
	}

	// Create fresh manager and set accounts
	mgr2, _ := NewManager(big.NewInt(42069), big.NewInt(1e9), slog.Default())
	mgr2.SetDynamicAccounts(accs)

	if got := mgr2.GetAccountsFunded(); got != 3 {
		t.Errorf("GetAccountsFunded() = %d, want 3", got)
	}
	if got := len(mgr2.GetDynamicAccounts()); got != 3 {
		t.Errorf("len(GetDynamicAccounts()) = %d, want 3", got)
	}

	// Verify addresses match
	for i, acc := range mgr2.GetDynamicAccounts() {
		if acc.Address.Hex() != pairs[i].Address {
			t.Errorf("account[%d].Address = %s, want %s", i, acc.Address.Hex(), pairs[i].Address)
		}
	}
}

func TestExportDynamicAccountKeys_Empty(t *testing.T) {
	mgr, err := NewManager(big.NewInt(42069), big.NewInt(1e9), slog.Default())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	pairs := mgr.ExportDynamicAccountKeys()
	if len(pairs) != 0 {
		t.Errorf("expected 0 pairs, got %d", len(pairs))
	}
}

func TestSetDynamicAccountsOverwrite(t *testing.T) {
	mgr, err := NewManager(big.NewInt(42069), big.NewInt(1e9), slog.Default())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Generate initial accounts
	mgr.GenerateDynamicAccounts(5)
	if got := len(mgr.GetDynamicAccounts()); got != 5 {
		t.Fatalf("initial: expected 5, got %d", got)
	}

	// Overwrite with fewer accounts
	acc, _ := NewAccountFromHex(TestPrivateKeys[0])
	mgr.SetDynamicAccounts([]*Account{acc})

	if got := len(mgr.GetDynamicAccounts()); got != 1 {
		t.Errorf("after set: expected 1, got %d", got)
	}
	if got := mgr.GetAccountsFunded(); got != 1 {
		t.Errorf("after set: GetAccountsFunded() = %d, want 1", got)
	}
}
