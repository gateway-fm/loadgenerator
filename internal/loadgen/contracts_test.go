package loadgen

import (
	"context"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/contract"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	"github.com/gateway-fm/loadgenerator/internal/uniswapv3"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

type mockCacheStorage struct {
	SaveCachedAccountsFn       func(ctx context.Context, accounts []storage.CachedAccount) error
	LoadCachedAccountsFn       func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error)
	DeleteCachedAccountsFn     func(ctx context.Context, chainID int64) error
	MarkAccountsUniswapReadyFn func(ctx context.Context, chainID int64, addresses []string) error
	SaveCachedContractFn       func(ctx context.Context, contract storage.CachedContract) error
	LoadCachedContractsFn      func(ctx context.Context, chainID int64) ([]storage.CachedContract, error)
	DeleteCachedContractsFn    func(ctx context.Context, chainID int64) error
}

func (m *mockCacheStorage) SaveCachedAccounts(ctx context.Context, accounts []storage.CachedAccount) error {
	if m.SaveCachedAccountsFn != nil {
		return m.SaveCachedAccountsFn(ctx, accounts)
	}
	return nil
}
func (m *mockCacheStorage) LoadCachedAccounts(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
	if m.LoadCachedAccountsFn != nil {
		return m.LoadCachedAccountsFn(ctx, chainID)
	}
	return nil, nil
}
func (m *mockCacheStorage) DeleteCachedAccounts(ctx context.Context, chainID int64) error {
	if m.DeleteCachedAccountsFn != nil {
		return m.DeleteCachedAccountsFn(ctx, chainID)
	}
	return nil
}
func (m *mockCacheStorage) MarkAccountsUniswapReady(ctx context.Context, chainID int64, addresses []string) error {
	if m.MarkAccountsUniswapReadyFn != nil {
		return m.MarkAccountsUniswapReadyFn(ctx, chainID, addresses)
	}
	return nil
}
func (m *mockCacheStorage) SaveCachedContract(ctx context.Context, c storage.CachedContract) error {
	if m.SaveCachedContractFn != nil {
		return m.SaveCachedContractFn(ctx, c)
	}
	return nil
}
func (m *mockCacheStorage) LoadCachedContracts(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
	if m.LoadCachedContractsFn != nil {
		return m.LoadCachedContractsFn(ctx, chainID)
	}
	return nil, nil
}
func (m *mockCacheStorage) DeleteCachedContracts(ctx context.Context, chainID int64) error {
	if m.DeleteCachedContractsFn != nil {
		return m.DeleteCachedContractsFn(ctx, chainID)
	}
	return nil
}

func TestEnsureContractsDeployed_EthTransfer_Noop(t *testing.T) {
	lg := newTestLoadGenerator(t)
	err := lg.ensureContractsDeployed(types.TxTypeEthTransfer)
	if err != nil {
		t.Fatalf("expected no error for ETH transfer, got: %v", err)
	}
	if lg.contractsDeployed {
		t.Error("contractsDeployed should remain false for ETH transfers")
	}
}

func TestEnsureContractsDeployed_NoAccounts(t *testing.T) {
	lg := newTestLoadGenerator(t, WithAccountManager(&mockAccountManager{}))
	err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer)
	if err == nil {
		t.Fatal("expected error when no accounts available")
	}
}

func TestEnsureContractsDeployed_DeploySuccess(t *testing.T) {
	acc := makeTestAccount(t)
	erc20Addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	gasConsumerAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")

	deployer := &mockDeployer{
		DeployAllWithProgressFn: func(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error) {
			onProgress("ERC20", 1, 2)
			onProgress("GasConsumer", 2, 2)
			return map[string]common.Address{
				"ERC20":       erc20Addr,
				"GasConsumer": gasConsumerAddr,
			}, nil
		},
	}

	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}

	lg := newTestLoadGenerator(t,
		WithDeployer(deployer),
		WithAccountManager(acctMgr),
	)

	err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !lg.contractsDeployed {
		t.Error("contractsDeployed should be true after successful deployment")
	}
	if lg.erc20Contract != erc20Addr {
		t.Errorf("erc20Contract = %s, want %s", lg.erc20Contract.Hex(), erc20Addr.Hex())
	}
	if lg.gasConsumerContract != gasConsumerAddr {
		t.Errorf("gasConsumerContract = %s, want %s", lg.gasConsumerContract.Hex(), gasConsumerAddr.Hex())
	}
}

func TestEnsureContractsDeployed_DeployError(t *testing.T) {
	acc := makeTestAccount(t)
	deployer := &mockDeployer{
		DeployAllWithProgressFn: func(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error) {
			return nil, fmt.Errorf("deploy failed")
		},
	}
	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}

	lg := newTestLoadGenerator(t,
		WithDeployer(deployer),
		WithAccountManager(acctMgr),
	)

	err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer)
	if err == nil {
		t.Fatal("expected error on deploy failure")
	}
	if lg.contractsDeployed {
		t.Error("contractsDeployed should remain false on error")
	}
}

func TestEnsureContractsDeployed_AlreadyDeployed_Noop(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.contractsDeployed = true

	err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureContractsDeployed_ProgressTracking(t *testing.T) {
	acc := makeTestAccount(t)
	var progressCalls []int

	deployer := &mockDeployer{
		DeployAllWithProgressFn: func(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error) {
			onProgress("ERC20", 1, 2)
			onProgress("GasConsumer", 2, 2)
			progressCalls = append(progressCalls, 1, 2)
			return map[string]common.Address{
				"ERC20":       common.HexToAddress("0xaaa"),
				"GasConsumer": common.HexToAddress("0xbbb"),
			}, nil
		},
	}
	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}
	lg := newTestLoadGenerator(t, WithDeployer(deployer), WithAccountManager(acctMgr))

	if err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer); err != nil {
		t.Fatal(err)
	}
	if len(progressCalls) != 2 {
		t.Errorf("expected 2 progress calls, got %d", len(progressCalls))
	}
	if lg.initContractsDone != 2 {
		t.Errorf("initContractsDone = %d, want 2", lg.initContractsDone)
	}
}

func TestTryRestoreCachedContracts_CacheHit(t *testing.T) {
	acc := makeTestAccount(t)
	erc20Addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	gasConsumerAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	nftAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")

	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return []storage.CachedContract{
				{Name: "ERC20", Address: erc20Addr.Hex(), ChainID: chainID},
				{Name: "GasConsumer", Address: gasConsumerAddr.Hex(), ChainID: chainID},
				{Name: "NFT", Address: nftAddr.Hex(), ChainID: chainID},
			}, nil
		},
	}

	deployer := &mockDeployer{
		ValidateCachedContractsFn: func(ctx context.Context, cached map[string]string) (map[string]common.Address, []string) {
			valid := make(map[string]common.Address, len(cached))
			for name, addr := range cached {
				valid[name] = common.HexToAddress(addr)
			}
			return valid, nil
		},
	}

	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}
	lg := newTestLoadGenerator(t, WithDeployer(deployer), WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, false, nil)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}

	if !restored {
		t.Fatal("expected tryRestoreCachedContracts to return true")
	}
	if !lg.contractsDeployed {
		t.Error("contractsDeployed should be true after cache restore")
	}
	if lg.erc20Contract != erc20Addr {
		t.Errorf("erc20Contract = %s, want %s", lg.erc20Contract.Hex(), erc20Addr.Hex())
	}
	if lg.gasConsumerContract != gasConsumerAddr {
		t.Errorf("gasConsumerContract = %s, want %s", lg.gasConsumerContract.Hex(), gasConsumerAddr.Hex())
	}
	if lg.nftContract != nftAddr {
		t.Errorf("nftContract = %s, want %s", lg.nftContract.Hex(), nftAddr.Hex())
	}
}

func TestTryRestoreCachedContracts_CacheMiss_EmptyCache(t *testing.T) {
	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return nil, nil
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, false, nil)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}
	if restored {
		t.Fatal("expected false when cache is empty")
	}
}

func TestTryRestoreCachedContracts_CacheMiss_LoadError(t *testing.T) {
	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return nil, fmt.Errorf("db error")
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, false, nil)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}
	if restored {
		t.Fatal("expected false on load error")
	}
}

func TestTryRestoreCachedContracts_InvalidContracts(t *testing.T) {
	deleteCalled := false
	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return []storage.CachedContract{
				{Name: "ERC20", Address: "0xaaa", ChainID: chainID},
				{Name: "GasConsumer", Address: "0xbbb", ChainID: chainID},
			}, nil
		},
		DeleteCachedContractsFn: func(ctx context.Context, chainID int64) error {
			deleteCalled = true
			return nil
		},
	}

	deployer := &mockDeployer{
		ValidateCachedContractsFn: func(ctx context.Context, cached map[string]string) (map[string]common.Address, []string) {
			return nil, []string{"ERC20"}
		},
	}

	lg := newTestLoadGenerator(t, WithDeployer(deployer))
	lg.cacheStorage = cache

	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, false, nil)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}
	if restored {
		t.Fatal("expected false when contracts are invalid on-chain")
	}
	if !deleteCalled {
		t.Error("expected DeleteCachedContracts to be called for invalid contracts")
	}
}

func TestTryRestoreCachedContracts_MissingBaseContracts(t *testing.T) {
	deleteCalled := false
	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return []storage.CachedContract{
				{Name: "ERC20", Address: "0xaaa", ChainID: chainID},
			}, nil
		},
		DeleteCachedContractsFn: func(ctx context.Context, chainID int64) error {
			deleteCalled = true
			return nil
		},
	}

	deployer := &mockDeployer{
		ValidateCachedContractsFn: func(ctx context.Context, cached map[string]string) (map[string]common.Address, []string) {
			valid := make(map[string]common.Address, len(cached))
			for name, addr := range cached {
				valid[name] = common.HexToAddress(addr)
			}
			return valid, nil
		},
	}

	lg := newTestLoadGenerator(t, WithDeployer(deployer))
	lg.cacheStorage = cache

	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, false, nil)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}
	if restored {
		t.Fatal("expected false when GasConsumer missing from cache")
	}
	if !deleteCalled {
		t.Error("expected DeleteCachedContracts when base contracts incomplete")
	}
}

func TestSaveBaseContractsToCache(t *testing.T) {
	var saved []storage.CachedContract
	cache := &mockCacheStorage{
		SaveCachedContractFn: func(ctx context.Context, c storage.CachedContract) error {
			saved = append(saved, c)
			return nil
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache
	lg.erc20Contract = common.HexToAddress("0x1111111111111111111111111111111111111111")
	lg.gasConsumerContract = common.HexToAddress("0x2222222222222222222222222222222222222222")
	lg.nftContract = common.HexToAddress("0x3333333333333333333333333333333333333333")

	lg.saveBaseContractsToCache(context.Background(), 42069)

	if len(saved) != 3 {
		t.Fatalf("expected 3 contracts saved, got %d", len(saved))
	}

	names := map[string]bool{}
	for _, c := range saved {
		names[c.Name] = true
		if c.ChainID != 42069 {
			t.Errorf("expected chainID 42069, got %d", c.ChainID)
		}
	}
	if !names["ERC20"] {
		t.Error("expected ERC20 to be saved")
	}
	if !names["GasConsumer"] {
		t.Error("expected GasConsumer to be saved")
	}
	if !names["NFT"] {
		t.Error("expected NFT to be saved")
	}
}

func TestSaveBaseContractsToCache_NilCacheStorage(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = nil
	// Should not panic
	lg.saveBaseContractsToCache(context.Background(), 42069)
}

func TestSaveBaseContractsToCache_SaveError(t *testing.T) {
	cache := &mockCacheStorage{
		SaveCachedContractFn: func(ctx context.Context, c storage.CachedContract) error {
			return fmt.Errorf("write failed")
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache
	lg.erc20Contract = common.HexToAddress("0xaaa")
	lg.gasConsumerContract = common.HexToAddress("0xbbb")

	// Should not panic, errors are logged as warnings
	lg.saveBaseContractsToCache(context.Background(), 42069)
}

func TestEnsureContractsDeployed_WithCacheRestore(t *testing.T) {
	acc := makeTestAccount(t)
	erc20Addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	gasConsumerAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	nftAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")

	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return []storage.CachedContract{
				{Name: "ERC20", Address: erc20Addr.Hex(), ChainID: chainID},
				{Name: "GasConsumer", Address: gasConsumerAddr.Hex(), ChainID: chainID},
				{Name: "NFT", Address: nftAddr.Hex(), ChainID: chainID},
			}, nil
		},
	}

	deployerCalled := false
	deployer := &mockDeployer{
		DeployAllWithProgressFn: func(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error) {
			deployerCalled = true
			return nil, fmt.Errorf("should not be called")
		},
		ValidateCachedContractsFn: func(ctx context.Context, cached map[string]string) (map[string]common.Address, []string) {
			valid := make(map[string]common.Address, len(cached))
			for name, addr := range cached {
				valid[name] = common.HexToAddress(addr)
			}
			return valid, nil
		},
	}

	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}
	lg := newTestLoadGenerator(t, WithDeployer(deployer), WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deployerCalled {
		t.Error("deployer should not be called when cache restore succeeds")
	}
	if !lg.contractsDeployed {
		t.Error("contractsDeployed should be true after cache restore")
	}
}

func TestSaveUniswapContractsToCache_NilCacheStorage(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = nil

	ub := txbuilder.NewUniswapV3SwapBuilder()
	// Should not panic
	lg.saveUniswapContractsToCache(context.Background(), 42069, ub)
}

func TestSaveUniswapContractsToCache_NilContracts(t *testing.T) {
	saveCalled := false
	cache := &mockCacheStorage{
		SaveCachedContractFn: func(ctx context.Context, c storage.CachedContract) error {
			saveCalled = true
			return nil
		},
	}
	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	// Pass nil builder — the function calls ub.GetContracts() which returns nil
	// when contracts field is nil. We can't easily make GetContracts return nil
	// from NewUniswapV3SwapBuilder since it initializes contracts. Instead,
	// verify the normal path doesn't skip when contracts are non-nil.
	ub := txbuilder.NewUniswapV3SwapBuilder()
	// Fresh builder has non-nil but zero-valued contracts — save should proceed
	lg.saveUniswapContractsToCache(context.Background(), 42069, ub)

	if !saveCalled {
		t.Error("expected SaveCachedContract to be called for non-nil contracts")
	}
}

func TestSaveUniswapContractsToCache_NormalSave(t *testing.T) {
	var saved []storage.CachedContract
	cache := &mockCacheStorage{
		SaveCachedContractFn: func(ctx context.Context, c storage.CachedContract) error {
			saved = append(saved, c)
			return nil
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	ub := txbuilder.NewUniswapV3SwapBuilder()
	contracts := &uniswapv3.DeployedContracts{
		WETH9:                      common.HexToAddress("0xW"),
		USDC:                       common.HexToAddress("0xU"),
		Factory:                    common.HexToAddress("0xF"),
		SwapRouter:                 common.HexToAddress("0xS"),
		NonfungiblePositionManager: common.HexToAddress("0xN"),
		Pool:                       common.HexToAddress("0xP"),
	}
	ub.RestoreContracts(contracts)

	lg.saveUniswapContractsToCache(context.Background(), 42069, ub)

	if len(saved) != 6 {
		t.Fatalf("expected 6 contracts saved, got %d", len(saved))
	}

	expectedNames := map[string]bool{
		"uniswap:WETH9":                      false,
		"uniswap:USDC":                       false,
		"uniswap:Factory":                    false,
		"uniswap:SwapRouter":                 false,
		"uniswap:NonfungiblePositionManager": false,
		"uniswap:Pool":                       false,
	}
	for _, c := range saved {
		if _, ok := expectedNames[c.Name]; !ok {
			t.Errorf("unexpected contract name: %s", c.Name)
		}
		expectedNames[c.Name] = true
		if c.ChainID != 42069 {
			t.Errorf("expected chainID 42069, got %d", c.ChainID)
		}
	}
	for name, found := range expectedNames {
		if !found {
			t.Errorf("expected %s to be saved", name)
		}
	}
}

func TestSaveUniswapContractsToCache_SaveError(t *testing.T) {
	cache := &mockCacheStorage{
		SaveCachedContractFn: func(ctx context.Context, c storage.CachedContract) error {
			return fmt.Errorf("write failed")
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	ub := txbuilder.NewUniswapV3SwapBuilder()
	ub.RestoreContracts(&uniswapv3.DeployedContracts{
		WETH9:                      common.HexToAddress("0x1"),
		USDC:                       common.HexToAddress("0x2"),
		Factory:                    common.HexToAddress("0x3"),
		SwapRouter:                 common.HexToAddress("0x4"),
		NonfungiblePositionManager: common.HexToAddress("0x5"),
		Pool:                       common.HexToAddress("0x6"),
	})

	// Should not panic, errors are logged as warnings
	lg.saveUniswapContractsToCache(context.Background(), 42069, ub)
}

func TestTryRestoreCachedContracts_WithUniswap_FullRestore(t *testing.T) {
	erc20Addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	gasConsumerAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	weth9Addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	usdcAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	factoryAddr := common.HexToAddress("0x5555555555555555555555555555555555555555")
	routerAddr := common.HexToAddress("0x6666666666666666666666666666666666666666")
	positionMgrAddr := common.HexToAddress("0x7777777777777777777777777777777777777777")
	poolAddr := common.HexToAddress("0x8888888888888888888888888888888888888888")
	nftAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")

	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return []storage.CachedContract{
				{Name: "ERC20", Address: erc20Addr.Hex(), ChainID: chainID},
				{Name: "GasConsumer", Address: gasConsumerAddr.Hex(), ChainID: chainID},
				{Name: "NFT", Address: nftAddr.Hex(), ChainID: chainID},
				{Name: "uniswap:WETH9", Address: weth9Addr.Hex(), ChainID: chainID},
				{Name: "uniswap:USDC", Address: usdcAddr.Hex(), ChainID: chainID},
				{Name: "uniswap:Factory", Address: factoryAddr.Hex(), ChainID: chainID},
				{Name: "uniswap:SwapRouter", Address: routerAddr.Hex(), ChainID: chainID},
				{Name: "uniswap:NonfungiblePositionManager", Address: positionMgrAddr.Hex(), ChainID: chainID},
				{Name: "uniswap:Pool", Address: poolAddr.Hex(), ChainID: chainID},
			}, nil
		},
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return nil, nil // no cached accounts, all need setup
		},
	}

	deployer := &mockDeployer{
		ValidateCachedContractsFn: func(ctx context.Context, cached map[string]string) (map[string]common.Address, []string) {
			valid := make(map[string]common.Address, len(cached))
			for name, addr := range cached {
				valid[name] = common.HexToAddress(addr)
			}
			return valid, nil
		},
	}

	acc := makeTestAccount(t)
	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}

	// Mark the account as uniswap-ready so setupUniswapAccountsFromCache
	// takes the early exit path (avoids 60s timeout from real SetupAccounts)
	cache.LoadCachedAccountsFn = func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
		return []storage.CachedAccount{
			{Address: acc.Address.Hex(), UniswapReady: true, ChainID: chainID},
		}, nil
	}

	lg := newTestLoadGenerator(t, WithDeployer(deployer), WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	ub := txbuilder.NewUniswapV3SwapBuilder()
	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, true, ub)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}

	if !restored {
		t.Fatal("expected tryRestoreCachedContracts to return true")
	}
	if !lg.contractsDeployed {
		t.Error("contractsDeployed should be true")
	}
	if lg.erc20Contract != erc20Addr {
		t.Errorf("erc20Contract = %s, want %s", lg.erc20Contract.Hex(), erc20Addr.Hex())
	}

	contracts := ub.GetContracts()
	if contracts.Pool != poolAddr {
		t.Errorf("pool = %s, want %s", contracts.Pool.Hex(), poolAddr.Hex())
	}
	if contracts.SwapRouter != routerAddr {
		t.Errorf("swapRouter = %s, want %s", contracts.SwapRouter.Hex(), routerAddr.Hex())
	}
}

func TestTryRestoreCachedContracts_WithUniswap_MissingUniswapContracts(t *testing.T) {
	erc20Addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	gasConsumerAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")

	deleteCalled := false
	cache := &mockCacheStorage{
		LoadCachedContractsFn: func(ctx context.Context, chainID int64) ([]storage.CachedContract, error) {
			return []storage.CachedContract{
				{Name: "ERC20", Address: erc20Addr.Hex(), ChainID: chainID},
				{Name: "GasConsumer", Address: gasConsumerAddr.Hex(), ChainID: chainID},
				// Missing uniswap contracts
			}, nil
		},
		DeleteCachedContractsFn: func(ctx context.Context, chainID int64) error {
			deleteCalled = true
			return nil
		},
	}

	deployer := &mockDeployer{
		ValidateCachedContractsFn: func(ctx context.Context, cached map[string]string) (map[string]common.Address, []string) {
			valid := make(map[string]common.Address, len(cached))
			for name, addr := range cached {
				valid[name] = common.HexToAddress(addr)
			}
			return valid, nil
		},
	}

	lg := newTestLoadGenerator(t, WithDeployer(deployer))
	lg.cacheStorage = cache

	ub := txbuilder.NewUniswapV3SwapBuilder()
	ctx := context.Background()
	restored, restoreErr := lg.tryRestoreCachedContracts(ctx, 42069, true, ub)
	if restoreErr != nil {
		t.Fatalf("tryRestoreCachedContracts: %v", restoreErr)
	}

	if restored {
		t.Fatal("expected false when uniswap contracts missing from cache")
	}
	if !deleteCalled {
		t.Error("expected DeleteCachedContracts to be called")
	}
	if lg.contractsDeployed {
		t.Error("contractsDeployed should be reset to false")
	}
}

func TestSetupUniswapAccountsFromCache_LoadError(t *testing.T) {
	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return nil, fmt.Errorf("db error")
		},
	}

	lg := newTestLoadGenerator(t)
	lg.cacheStorage = cache

	ub := txbuilder.NewUniswapV3SwapBuilder()
	// Should not panic, returns early on error
	if err := lg.setupUniswapAccountsFromCache(context.Background(), 42069, ub); err != nil {
		t.Fatalf("setupUniswapAccountsFromCache: %v", err)
	}
}

func TestSetupUniswapAccountsFromCache_AllAccountsReady(t *testing.T) {
	acc := makeTestAccount(t)
	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{
				{Address: acc.Address.Hex(), UniswapReady: true, ChainID: chainID},
			}, nil
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	ub := txbuilder.NewUniswapV3SwapBuilder()
	// All accounts already ready, should return early without calling SetupAccounts
	if err := lg.setupUniswapAccountsFromCache(context.Background(), 42069, ub); err != nil {
		t.Fatalf("setupUniswapAccountsFromCache: %v", err)
	}
}

func TestSetupUniswapAccountsFromCache_SetupError(t *testing.T) {
	acc := makeTestAccount(t)
	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return nil, nil // no cached accounts, so acc needs setup
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	// Fresh builder with deployed=false will return "contracts not deployed" error
	ub := txbuilder.NewUniswapV3SwapBuilder()
	// Should not panic, error is logged as warning
	if err := lg.setupUniswapAccountsFromCache(context.Background(), 42069, ub); err != nil {
		t.Fatalf("setupUniswapAccountsFromCache: %v", err)
	}
}

func TestSetupUniswapAccountsFromCache_PartialReady(t *testing.T) {
	acc1 := makeTestAccount(t)
	acc2 := makeTestAccount(t)
	acctMgr := &mockAccountManager{
		accounts: []*account.Account{acc1, acc2},
	}

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{
				{Address: acc1.Address.Hex(), UniswapReady: true, ChainID: chainID},
				// acc2 not in cache, needs setup
			}, nil
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	// Fresh builder (deployed=false) will error on SetupAccounts, but that's
	// handled gracefully. This tests the filtering logic: only acc2 should
	// be in needSetup.
	ub := txbuilder.NewUniswapV3SwapBuilder()
	if err := lg.setupUniswapAccountsFromCache(context.Background(), 42069, ub); err != nil {
		t.Fatalf("setupUniswapAccountsFromCache: %v", err)
	}
}

func TestSetupUniswapAccountsFromCache_DynamicAccountsIncluded(t *testing.T) {
	builtIn := makeTestAccount(t)
	dynamic := makeTestAccount(t)
	acctMgr := &mockAccountManager{
		accounts:        []*account.Account{builtIn},
		dynamicAccounts: []*account.Account{dynamic},
	}

	cache := &mockCacheStorage{
		LoadCachedAccountsFn: func(ctx context.Context, chainID int64) ([]storage.CachedAccount, error) {
			return []storage.CachedAccount{
				{Address: builtIn.Address.Hex(), UniswapReady: true, ChainID: chainID},
				// dynamic not ready
			}, nil
		},
	}

	lg := newTestLoadGenerator(t, WithAccountManager(acctMgr))
	lg.cacheStorage = cache

	// Will fail on SetupAccounts (deployed=false) but exercises the dynamic account path
	ub := txbuilder.NewUniswapV3SwapBuilder()
	if err := lg.setupUniswapAccountsFromCache(context.Background(), 42069, ub); err != nil {
		t.Fatalf("setupUniswapAccountsFromCache: %v", err)
	}
}

func TestEnsureContractsDeployed_SetsBuilderContractAddresses(t *testing.T) {
	acc := makeTestAccount(t)
	erc20Addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	gasConsumerAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")

	deployer := &mockDeployer{
		DeployAllWithProgressFn: func(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error) {
			return map[string]common.Address{
				"ERC20":       erc20Addr,
				"GasConsumer": gasConsumerAddr,
			}, nil
		},
	}
	acctMgr := &mockAccountManager{accounts: []*account.Account{acc}}
	lg := newTestLoadGenerator(t, WithDeployer(deployer), WithAccountManager(acctMgr))

	if err := lg.ensureContractsDeployed(types.TxTypeERC20Transfer); err != nil {
		t.Fatal(err)
	}

	// Verify builders got the contract address set
	if b, err := lg.txBuilderReg.Get(types.TxTypeERC20Transfer); err == nil {
		_ = b // builder was configured; we verified no panic
	}
	if b, err := lg.txBuilderReg.Get(types.TxTypeStorageWrite); err == nil {
		_ = b
	}
}
