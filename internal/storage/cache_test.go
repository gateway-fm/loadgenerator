package storage

import (
	"context"
	"testing"
	"time"
)

func TestSaveAndLoadCachedAccounts(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	accounts := []CachedAccount{
		{Address: "0xAAA", PrivateKeyHex: "aaa111", ChainID: 42069, CreatedAt: now},
		{Address: "0xBBB", PrivateKeyHex: "bbb222", ChainID: 42069, CreatedAt: now},
		{Address: "0xCCC", PrivateKeyHex: "ccc333", ChainID: 42069, CreatedAt: now},
	}

	if err := store.SaveCachedAccounts(ctx, accounts); err != nil {
		t.Fatalf("SaveCachedAccounts: %v", err)
	}

	loaded, err := store.LoadCachedAccounts(ctx, 42069)
	if err != nil {
		t.Fatalf("LoadCachedAccounts: %v", err)
	}

	if len(loaded) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(loaded))
	}

	for i, a := range loaded {
		if a.Address != accounts[i].Address {
			t.Errorf("account[%d].Address = %q, want %q", i, a.Address, accounts[i].Address)
		}
		if a.PrivateKeyHex != accounts[i].PrivateKeyHex {
			t.Errorf("account[%d].PrivateKeyHex = %q, want %q", i, a.PrivateKeyHex, accounts[i].PrivateKeyHex)
		}
		if a.ChainID != 42069 {
			t.Errorf("account[%d].ChainID = %d, want 42069", i, a.ChainID)
		}
		if a.UniswapReady {
			t.Errorf("account[%d].UniswapReady should be false", i)
		}
	}
}

func TestCachedAccountsChainIDScoping(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// Save accounts on two different chains
	chain1 := []CachedAccount{
		{Address: "0xAAA", PrivateKeyHex: "aaa", ChainID: 1, CreatedAt: now},
	}
	chain2 := []CachedAccount{
		{Address: "0xBBB", PrivateKeyHex: "bbb", ChainID: 2, CreatedAt: now},
		{Address: "0xCCC", PrivateKeyHex: "ccc", ChainID: 2, CreatedAt: now},
	}

	if err := store.SaveCachedAccounts(ctx, chain1); err != nil {
		t.Fatalf("SaveCachedAccounts chain1: %v", err)
	}
	if err := store.SaveCachedAccounts(ctx, chain2); err != nil {
		t.Fatalf("SaveCachedAccounts chain2: %v", err)
	}

	loaded1, _ := store.LoadCachedAccounts(ctx, 1)
	loaded2, _ := store.LoadCachedAccounts(ctx, 2)

	if len(loaded1) != 1 {
		t.Errorf("chain 1: expected 1 account, got %d", len(loaded1))
	}
	if len(loaded2) != 2 {
		t.Errorf("chain 2: expected 2 accounts, got %d", len(loaded2))
	}
}

func TestDeleteCachedAccounts(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	accounts := []CachedAccount{
		{Address: "0xAAA", PrivateKeyHex: "aaa", ChainID: 42069, CreatedAt: now},
		{Address: "0xBBB", PrivateKeyHex: "bbb", ChainID: 42069, CreatedAt: now},
	}
	store.SaveCachedAccounts(ctx, accounts)

	if err := store.DeleteCachedAccounts(ctx, 42069); err != nil {
		t.Fatalf("DeleteCachedAccounts: %v", err)
	}

	loaded, _ := store.LoadCachedAccounts(ctx, 42069)
	if len(loaded) != 0 {
		t.Errorf("expected 0 accounts after delete, got %d", len(loaded))
	}
}

func TestMarkAccountsUniswapReady(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	accounts := []CachedAccount{
		{Address: "0xAAA", PrivateKeyHex: "aaa", ChainID: 42069, CreatedAt: now},
		{Address: "0xBBB", PrivateKeyHex: "bbb", ChainID: 42069, CreatedAt: now},
		{Address: "0xCCC", PrivateKeyHex: "ccc", ChainID: 42069, CreatedAt: now},
	}
	store.SaveCachedAccounts(ctx, accounts)

	// Mark only first two as uniswap-ready
	if err := store.MarkAccountsUniswapReady(ctx, 42069, []string{"0xAAA", "0xBBB"}); err != nil {
		t.Fatalf("MarkAccountsUniswapReady: %v", err)
	}

	loaded, _ := store.LoadCachedAccounts(ctx, 42069)
	if !loaded[0].UniswapReady {
		t.Error("account 0xAAA should be uniswap-ready")
	}
	if !loaded[1].UniswapReady {
		t.Error("account 0xBBB should be uniswap-ready")
	}
	if loaded[2].UniswapReady {
		t.Error("account 0xCCC should NOT be uniswap-ready")
	}
}

func TestMarkAccountsUniswapReady_EmptyList(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	// Should be a no-op, not an error
	if err := store.MarkAccountsUniswapReady(context.Background(), 42069, nil); err != nil {
		t.Fatalf("MarkAccountsUniswapReady with nil: %v", err)
	}
}

func TestSaveAndLoadCachedContracts(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	contracts := []CachedContract{
		{Name: "ERC20", Address: "0x111", ChainID: 42069, CreatedAt: now},
		{Name: "GasConsumer", Address: "0x222", ChainID: 42069, CreatedAt: now},
	}

	for _, c := range contracts {
		if err := store.SaveCachedContract(ctx, c); err != nil {
			t.Fatalf("SaveCachedContract(%s): %v", c.Name, err)
		}
	}

	loaded, err := store.LoadCachedContracts(ctx, 42069)
	if err != nil {
		t.Fatalf("LoadCachedContracts: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 contracts, got %d", len(loaded))
	}

	if loaded[0].Name != "ERC20" || loaded[0].Address != "0x111" {
		t.Errorf("contract 0: got %+v", loaded[0])
	}
	if loaded[1].Name != "GasConsumer" || loaded[1].Address != "0x222" {
		t.Errorf("contract 1: got %+v", loaded[1])
	}
}

func TestSaveCachedContract_Upsert(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// Save contract
	c := CachedContract{Name: "ERC20", Address: "0x111", ChainID: 42069, CreatedAt: now}
	store.SaveCachedContract(ctx, c)

	// Update with new address (same name + chain)
	c.Address = "0x999"
	store.SaveCachedContract(ctx, c)

	loaded, _ := store.LoadCachedContracts(ctx, 42069)
	if len(loaded) != 1 {
		t.Fatalf("expected 1 contract after upsert, got %d", len(loaded))
	}
	if loaded[0].Address != "0x999" {
		t.Errorf("expected updated address 0x999, got %s", loaded[0].Address)
	}
}

func TestDeleteCachedContracts(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	store.SaveCachedContract(ctx, CachedContract{Name: "ERC20", Address: "0x111", ChainID: 42069, CreatedAt: now})
	store.SaveCachedContract(ctx, CachedContract{Name: "GasConsumer", Address: "0x222", ChainID: 42069, CreatedAt: now})

	if err := store.DeleteCachedContracts(ctx, 42069); err != nil {
		t.Fatalf("DeleteCachedContracts: %v", err)
	}

	loaded, _ := store.LoadCachedContracts(ctx, 42069)
	if len(loaded) != 0 {
		t.Errorf("expected 0 contracts after delete, got %d", len(loaded))
	}
}

func TestCachedContractsChainIDScoping(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	store.SaveCachedContract(ctx, CachedContract{Name: "ERC20", Address: "0x111", ChainID: 1, CreatedAt: now})
	store.SaveCachedContract(ctx, CachedContract{Name: "ERC20", Address: "0x222", ChainID: 2, CreatedAt: now})

	loaded1, _ := store.LoadCachedContracts(ctx, 1)
	loaded2, _ := store.LoadCachedContracts(ctx, 2)

	if len(loaded1) != 1 || loaded1[0].Address != "0x111" {
		t.Errorf("chain 1: expected address 0x111, got %v", loaded1)
	}
	if len(loaded2) != 1 || loaded2[0].Address != "0x222" {
		t.Errorf("chain 2: expected address 0x222, got %v", loaded2)
	}
}

func TestSaveCachedAccounts_Empty(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	// Should be a no-op, not an error
	if err := store.SaveCachedAccounts(context.Background(), nil); err != nil {
		t.Fatalf("SaveCachedAccounts with nil: %v", err)
	}
}

func TestLoadCachedAccounts_NoResults(t *testing.T) {
	store, cleanup := createTestStorage(t)
	defer cleanup()

	loaded, err := store.LoadCachedAccounts(context.Background(), 99999)
	if err != nil {
		t.Fatalf("LoadCachedAccounts: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected empty result, got %d", len(loaded))
	}
}
