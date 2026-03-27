package loadgen

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/storage"
)

func validCachedAccount(t *testing.T) (storage.CachedAccount, *account.Account) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	acc := account.NewAccount(key)
	return storage.CachedAccount{
		PrivateKeyHex: hex.EncodeToString(crypto.FromECDSA(key)),
		Address:       acc.Address.Hex(),
		ChainID:       42069,
	}, acc
}

func TestTryWarmStartAccounts_NoCachedAccounts(t *testing.T) {
	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return nil, nil
		},
	}
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	if lg.tryWarmStartAccounts(5, 42069) {
		t.Error("expected false when no cached accounts")
	}
}

func TestTryWarmStartAccounts_CacheLoadError(t *testing.T) {
	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return nil, fmt.Errorf("db error")
		},
	}
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	if lg.tryWarmStartAccounts(5, 42069) {
		t.Error("expected false on cache load error")
	}
}

func TestTryWarmStartAccounts_InvalidCachedKey(t *testing.T) {
	var deleteCalled int32
	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{
				{PrivateKeyHex: "not-a-valid-hex-key", Address: "0xaaa", ChainID: chainID},
			}, nil
		},
		DeleteCachedAccountsFn: func(ctx context.Context, chainID int64) error {
			atomic.StoreInt32(&deleteCalled, 1)
			return nil
		},
	}
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	if lg.tryWarmStartAccounts(5, 42069) {
		t.Error("expected false for invalid cached key")
	}
	if atomic.LoadInt32(&deleteCalled) != 1 {
		t.Error("expected DeleteCachedAccounts to be called")
	}
}

func TestTryWarmStartAccounts_AllZeroBalance_ReGenesis(t *testing.T) {
	ca, _ := validCachedAccount(t)
	var deleteAcctsCalled, deleteContractsCalled int32

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{ca}, nil
		},
		DeleteCachedAccountsFn: func(ctx context.Context, chainID int64) error {
			atomic.StoreInt32(&deleteAcctsCalled, 1)
			return nil
		},
		DeleteCachedContractsFn: func(ctx context.Context, chainID int64) error {
			atomic.StoreInt32(&deleteContractsCalled, 1)
			return nil
		},
	}

	acctMgr := &mockAccountManager{
		ValidateBalancesFn: func(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account) {
			return nil, accounts
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	if lg.tryWarmStartAccounts(1, 42069) {
		t.Error("expected false when all accounts have zero balance")
	}
	if atomic.LoadInt32(&deleteAcctsCalled) != 1 {
		t.Error("expected DeleteCachedAccounts called on re-genesis")
	}
	if atomic.LoadInt32(&deleteContractsCalled) != 1 {
		t.Error("expected DeleteCachedContracts called on re-genesis")
	}
}

func TestTryWarmStartAccounts_EnoughFunded(t *testing.T) {
	ca1, acc1 := validCachedAccount(t)
	ca2, acc2 := validCachedAccount(t)
	ca3, _ := validCachedAccount(t)

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{ca1, ca2, ca3}, nil
		},
	}

	acctMgr := &mockAccountManager{
		ValidateBalancesFn: func(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account) {
			return accounts, nil
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	if !lg.tryWarmStartAccounts(2, 42069) {
		t.Fatal("expected true when enough funded accounts")
	}
	if acctMgr.dynamicAccounts == nil {
		t.Fatal("expected dynamic accounts to be set")
	}
	if len(acctMgr.dynamicAccounts) != 2 {
		t.Errorf("expected 2 accounts, got %d", len(acctMgr.dynamicAccounts))
	}
	if acctMgr.dynamicAccounts[0].Address != acc1.Address {
		t.Error("expected first account to be acc1")
	}
	if acctMgr.dynamicAccounts[1].Address != acc2.Address {
		t.Error("expected second account to be acc2")
	}
	if lg.initAccountsGen != 2 {
		t.Errorf("initAccountsGen = %d, want 2", lg.initAccountsGen)
	}
}

func TestTryWarmStartAccounts_MixFundedUnfunded_Refunding(t *testing.T) {
	ca1, _ := validCachedAccount(t)
	ca2, acc2 := validCachedAccount(t)

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{ca1, ca2}, nil
		},
	}

	var fundAccountsCalled int32
	acctMgr := &mockAccountManager{
		ValidateBalancesFn: func(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account) {
			return accounts[:1], accounts[1:]
		},
		FundAccountsFn: func(ctx context.Context, sendClient, syncClient rpc.Client, accounts []*account.Account) error {
			atomic.StoreInt32(&fundAccountsCalled, 1)
			return nil
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache
	lg.cfg.Capabilities.HasExternalBlockBuilder = false

	if !lg.tryWarmStartAccounts(2, 42069) {
		t.Fatal("expected true after re-funding")
	}
	if atomic.LoadInt32(&fundAccountsCalled) != 1 {
		t.Error("expected FundAccounts to be called for unfunded accounts")
	}
	if len(acctMgr.dynamicAccounts) != 2 {
		t.Errorf("expected 2 accounts, got %d", len(acctMgr.dynamicAccounts))
	}
	if acctMgr.dynamicAccounts[1].Address != acc2.Address {
		t.Error("expected second account to be the re-funded one")
	}
}

func TestTryWarmStartAccounts_NotEnoughCached_GeneratesNew(t *testing.T) {
	ca1, _ := validCachedAccount(t)
	newAcc := makeTestAccount(t)

	var saveCalled int32
	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{ca1}, nil
		},
		SaveCachedAccountsFn: func(ctx context.Context, accounts []storage.CachedAccount) error {
			atomic.StoreInt32(&saveCalled, 1)
			return nil
		},
	}

	var generateCalled int32
	acctMgr := &mockAccountManager{
		ValidateBalancesFn: func(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account) {
			return accounts, nil
		},
		GenerateDynamicAccountsFn: func(count int) error {
			atomic.StoreInt32(&generateCalled, 1)
			if count != 2 {
				t.Errorf("expected generate count 2, got %d", count)
			}
			return nil
		},
		ExportDynamicAccountKeysFn: func() []account.AccountKeyPair {
			return []account.AccountKeyPair{
				{Address: newAcc.Address.Hex(), PrivateKeyHex: "abcd1234"},
			}
		},
	}
	acctMgr.dynamicAccounts = []*account.Account{newAcc}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache
	lg.cfg.Capabilities.HasExternalBlockBuilder = false

	if !lg.tryWarmStartAccounts(3, 42069) {
		t.Fatal("expected true after generating new accounts")
	}
	if atomic.LoadInt32(&generateCalled) != 1 {
		t.Error("expected GenerateDynamicAccounts to be called")
	}
	if atomic.LoadInt32(&saveCalled) != 1 {
		t.Error("expected cache save for newly generated accounts")
	}
}

func TestTryWarmStartAccounts_NonceInitError_StillReturnsTrue(t *testing.T) {
	ca1, _ := validCachedAccount(t)

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{ca1}, nil
		},
	}

	acctMgr := &mockAccountManager{
		ValidateBalancesFn: func(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account) {
			return accounts, nil
		},
		InitializeDynamicNoncesFn: func(ctx context.Context, client rpc.Client) error {
			return fmt.Errorf("nonce init failed")
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	if !lg.tryWarmStartAccounts(1, 42069) {
		t.Error("expected true even when nonce init fails (non-fatal)")
	}
}

func TestSaveDynamicAccountsToCache_NilCacheStorage(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = nil
	lg.saveDynamicAccountsToCache(42069)
}

func TestSaveDynamicAccountsToCache_NormalSave(t *testing.T) {
	acc := makeTestAccount(t)
	hexKey := hex.EncodeToString(crypto.FromECDSA(acc.PrivateKey))

	var saved []storage.CachedAccount
	cache := &mockCacheStorage{
		SaveCachedAccountsFn: func(ctx context.Context, accounts []storage.CachedAccount) error {
			saved = accounts
			return nil
		},
	}

	acctMgr := &mockAccountManager{
		ExportDynamicAccountKeysFn: func() []account.AccountKeyPair {
			return []account.AccountKeyPair{
				{Address: acc.Address.Hex(), PrivateKeyHex: hexKey},
			}
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	lg.saveDynamicAccountsToCache(42069)

	if len(saved) != 1 {
		t.Fatalf("expected 1 saved account, got %d", len(saved))
	}
	if saved[0].Address != acc.Address.Hex() {
		t.Errorf("address = %s, want %s", saved[0].Address, acc.Address.Hex())
	}
	if saved[0].PrivateKeyHex != hexKey {
		t.Errorf("privateKeyHex mismatch")
	}
	if saved[0].ChainID != 42069 {
		t.Errorf("chainID = %d, want 42069", saved[0].ChainID)
	}
}

func TestSaveDynamicAccountsToCache_SaveError(t *testing.T) {
	cache := &mockCacheStorage{
		SaveCachedAccountsFn: func(ctx context.Context, accounts []storage.CachedAccount) error {
			return fmt.Errorf("write failed")
		},
	}

	acctMgr := &mockAccountManager{
		ExportDynamicAccountKeysFn: func() []account.AccountKeyPair {
			return []account.AccountKeyPair{
				{Address: "0xaaa", PrivateKeyHex: "deadbeef"},
			}
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	lg.saveDynamicAccountsToCache(42069)
}
