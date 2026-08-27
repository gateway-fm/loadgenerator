package loadgen

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	"github.com/gateway-fm/loadgenerator/internal/uniswapv3"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func (lg *LoadGenerator) ensureContractsDeployed(txType types.TransactionType) error {
	lg.contractsMu.Lock()
	defer lg.contractsMu.Unlock()

	// Only deploy if needed
	if txType == types.TxTypeEthTransfer {
		return nil
	}

	// Check if Uniswap contracts are needed but not yet deployed
	uniswapNeeded := false
	var uniswapBuilder *txbuilder.UniswapV3SwapBuilder
	if cb, ok := lg.txBuilderReg.GetComplexBuilder(txType); ok && cb.IsComplex() {
		if ub, ok := cb.(*txbuilder.UniswapV3SwapBuilder); ok {
			uniswapBuilder = ub
			uniswapNeeded = !ub.IsDeployed()
		}
	}

	// If base contracts deployed AND uniswap not needed (or already deployed), we're done
	if lg.contractsDeployed && !uniswapNeeded {
		return nil
	}

	lg.logger.Info("deploying contracts", slog.Bool("uniswapNeeded", uniswapNeeded), slog.Bool("baseDeployed", lg.contractsDeployed))

	// Get deployer account
	accounts := lg.accountMgr.GetAccounts()
	if len(accounts) == 0 {
		return fmt.Errorf("no accounts available for deployment")
	}
	deployerAcc := accounts[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cacheChainID := lg.cfg.ChainID

	// Try to restore contracts from cache
	if lg.cacheStorage != nil && !lg.contractsDeployed {
		restored, err := lg.tryRestoreCachedContracts(ctx, cacheChainID, uniswapNeeded, uniswapBuilder)
		if err != nil {
			return err
		}
		if restored {
			return nil
		}
	}

	// Deploy Uniswap V3 contracts if needed (and not already deployed)
	if uniswapNeeded && uniswapBuilder != nil {
		lg.logger.Info("deploying Uniswap V3 contracts (complex builder)")

		chainID := big.NewInt(lg.cfg.ChainID)
		gasPrice := big.NewInt(lg.cfg.GasPrice)

		uniswapProgress := func(name string, done, total int) {
			lg.statusMu.Lock()
			lg.initContractsDone = done
			lg.initProgress = fmt.Sprintf("Deploying %s (%d/%d)...", name, done, total)
			lg.statusMu.Unlock()
		}

		if err := uniswapBuilder.DeployContractsWithProgress(ctx, lg.builderClient, deployerAcc, chainID, gasPrice, lg.logger, uniswapProgress); err != nil {
			return fmt.Errorf("failed to deploy Uniswap V3 contracts: %w", err)
		}

		// Set up all accounts for Uniswap swapping
		dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
		totalAccounts := len(accounts) + len(dynamicAccounts)
		lg.logger.Info("setting up accounts for Uniswap swaps...", slog.Int("builtIn", len(accounts)), slog.Int("dynamic", len(dynamicAccounts)))
		allAccounts := make([]any, 0, totalAccounts)
		for _, acc := range accounts {
			allAccounts = append(allAccounts, acc)
		}
		for _, acc := range dynamicAccounts {
			allAccounts = append(allAccounts, acc)
		}
		if err := uniswapBuilder.SetupAccounts(ctx, allAccounts, lg.builderClient, chainID, gasPrice); err != nil {
			return fmt.Errorf("failed to setup accounts for Uniswap: %w", err)
		}

		lg.logger.Info("Uniswap V3 contracts deployed and accounts setup")
		lg.logger.Info("waiting for Uniswap transactions to settle...")
		time.Sleep(5 * time.Second)

		// Re-sync every account's nonce from the chain after Uniswap setup.
		//
		// SetupAccounts sends 4 TXs per account (mint USDC, wrap ETH, approve
		// WETH, approve USDC) via setupAccountFireAndForget, which reads the
		// nonce straight from the RPC into a LOCAL variable and increments that.
		// It never advances the Account's own counter — the one ReserveNonce
		// hands out during the load phase. So without this resync the load phase
		// starts from the pre-setup nonce and the chain rejects EVERY
		// transaction with "nonce too low: tx: 0 state: 4", giving txFailed ==
		// txSent and a near-idle chain that looks like a throughput ceiling.
		//
		// Safe to do here: SetupAccounts already blocked on receipts for the
		// last TX of every account, and nonce ordering means the earlier three
		// are confirmed too, so the confirmed on-chain nonce is the true value.
		if err := lg.accountMgr.InitializeNoncesFromChain(ctx, lg.l2Client, totalAccounts); err != nil {
			return fmt.Errorf("failed to resync nonces after Uniswap setup: %w", err)
		}

		// Cache Uniswap contract addresses
		lg.saveUniswapContractsToCache(ctx, cacheChainID, uniswapBuilder)

		// Mark all dynamic accounts as uniswap-ready in cache
		if lg.cacheStorage != nil {
			dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
			addresses := make([]string, len(dynamicAccounts))
			for i, acc := range dynamicAccounts {
				addresses[i] = acc.Address.Hex()
			}
			if err := lg.cacheStorage.MarkAccountsUniswapReady(ctx, cacheChainID, addresses); err != nil {
				lg.logger.Warn("failed to mark accounts as uniswap-ready", "error", err)
			}
		}
	}

	// Deploy base contracts (ERC20, GasConsumer, etc) if not already deployed
	if !lg.contractsDeployed {
		lg.logger.Info("Deploying base contracts...")

		baseContractOffset := 0
		if uniswapNeeded {
			baseContractOffset = 7
		}
		baseProgress := func(name string, done, total int) {
			lg.statusMu.Lock()
			lg.initContractsDone = baseContractOffset + done
			lg.initProgress = fmt.Sprintf("Deploying %s (%d/%d)...", name, baseContractOffset+done, lg.initContractsTotal)
			lg.statusMu.Unlock()
		}

		results, err := lg.deployer.DeployAllWithProgress(ctx, deployerAcc, baseProgress)
		if err != nil {
			return fmt.Errorf("failed to deploy contracts: %w", err)
		}

		lg.erc20Contract = results["ERC20"]
		lg.gasConsumerContract = results["GasConsumer"]
		lg.nftContract = results["NFT"]
		lg.contractsDeployed = true

		if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Transfer); err == nil {
			builder.SetContractAddress(lg.erc20Contract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Approve); err == nil {
			builder.SetContractAddress(lg.erc20Contract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeERC721Transfer); err == nil {
			builder.SetContractAddress(lg.nftContract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeStorageWrite); err == nil {
			builder.SetContractAddress(lg.gasConsumerContract)
		}
		if builder, err := lg.txBuilderReg.Get(types.TxTypeHeavyCompute); err == nil {
			builder.SetContractAddress(lg.gasConsumerContract)
		}

		lg.logger.Info("contracts deployed",
			"erc20", lg.erc20Contract.Hex(),
			"gasConsumer", lg.gasConsumerContract.Hex(),
			"nft", lg.nftContract.Hex(),
		)

		// Cache base contract addresses
		lg.saveBaseContractsToCache(ctx, cacheChainID)
	}

	return nil
}

// tryRestoreCachedContracts attempts to restore contracts from cache.
// Returns true if all needed contracts were restored successfully.
func (lg *LoadGenerator) tryRestoreCachedContracts(ctx context.Context, chainID int64, uniswapNeeded bool, uniswapBuilder *txbuilder.UniswapV3SwapBuilder) (bool, error) {
	cached, err := lg.cacheStorage.LoadCachedContracts(ctx, chainID)
	if err != nil {
		lg.logger.Warn("failed to load cached contracts", "error", err)
		return false, nil
	}
	if len(cached) == 0 {
		return false, nil
	}

	// Build name→address map for validation
	cachedMap := make(map[string]string, len(cached))
	for _, c := range cached {
		cachedMap[c.Name] = c.Address
	}

	// Validate all cached contracts still exist on-chain
	valid, invalid := lg.deployer.ValidateCachedContracts(ctx, cachedMap)
	if len(invalid) > 0 {
		lg.logger.Info("some cached contracts are invalid, deploying fresh",
			slog.Int("valid", len(valid)),
			slog.Int("invalid", len(invalid)),
		)
		lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
		return false, nil
	}

	// Restore base contracts
	erc20Addr, hasERC20 := valid["ERC20"]
	gasConsumerAddr, hasGasConsumer := valid["GasConsumer"]
	nftAddr, hasNFT := valid["NFT"]
	if !hasERC20 || !hasGasConsumer || !hasNFT {
		lg.logger.Info("base contracts not in cache, deploying fresh")
		lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
		return false, nil
	}

	lg.erc20Contract = erc20Addr
	lg.gasConsumerContract = gasConsumerAddr
	lg.nftContract = nftAddr
	lg.contractsDeployed = true

	if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Transfer); err == nil {
		builder.SetContractAddress(lg.erc20Contract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeERC20Approve); err == nil {
		builder.SetContractAddress(lg.erc20Contract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeERC721Transfer); err == nil {
		builder.SetContractAddress(lg.nftContract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeStorageWrite); err == nil {
		builder.SetContractAddress(lg.gasConsumerContract)
	}
	if builder, err := lg.txBuilderReg.Get(types.TxTypeHeavyCompute); err == nil {
		builder.SetContractAddress(lg.gasConsumerContract)
	}

	lg.logger.Info("restored cached base contracts",
		"erc20", lg.erc20Contract.Hex(),
		"gasConsumer", lg.gasConsumerContract.Hex(),
		"nft", lg.nftContract.Hex(),
	)

	// Restore Uniswap contracts if needed
	if uniswapNeeded && uniswapBuilder != nil {
		weth9, hasWETH9 := valid["uniswap:WETH9"]
		usdc, hasUSDC := valid["uniswap:USDC"]
		factory, hasFactory := valid["uniswap:Factory"]
		swapRouter, hasRouter := valid["uniswap:SwapRouter"]
		nftManager, hasNFT := valid["uniswap:NonfungiblePositionManager"]
		pool, hasPool := valid["uniswap:Pool"]

		if !hasWETH9 || !hasUSDC || !hasFactory || !hasRouter || !hasNFT || !hasPool {
			lg.logger.Info("Uniswap contracts not fully cached, deploying fresh")
			lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
			// Reset base contract state so they get re-cached with uniswap
			lg.contractsDeployed = false
			return false, nil
		}

		contracts := &uniswapv3.DeployedContracts{
			WETH9:                      weth9,
			USDC:                       usdc,
			Factory:                    factory,
			SwapRouter:                 swapRouter,
			NonfungiblePositionManager: nftManager,
			Pool:                       pool,
		}
		uniswapBuilder.RestoreContracts(contracts)

		lg.logger.Info("restored cached Uniswap V3 contracts",
			"pool", pool.Hex(),
			"swapRouter", swapRouter.Hex(),
		)

		// Setup accounts that aren't yet uniswap-ready
		if err := lg.setupUniswapAccountsFromCache(ctx, chainID, uniswapBuilder); err != nil {
			return false, err
		}
	}

	return true, nil
}

// setupUniswapAccountsFromCache sets up only accounts that aren't marked as uniswap-ready.
func (lg *LoadGenerator) setupUniswapAccountsFromCache(ctx context.Context, chainID int64, uniswapBuilder *txbuilder.UniswapV3SwapBuilder) error {
	// Load cached accounts to check uniswap_ready flag
	cached, err := lg.cacheStorage.LoadCachedAccounts(ctx, chainID)
	if err != nil {
		lg.logger.Warn("failed to load cached accounts for uniswap check", "error", err)
		return nil
	}

	readySet := make(map[string]bool, len(cached))
	for _, ca := range cached {
		if ca.UniswapReady {
			readySet[ca.Address] = true
		}
	}

	// Collect accounts that need Uniswap setup
	allBuiltIn := lg.accountMgr.GetAccounts()
	dynamicAccounts := lg.accountMgr.GetDynamicAccounts()

	var needSetup []any
	for _, acc := range allBuiltIn {
		if !readySet[acc.Address.Hex()] {
			needSetup = append(needSetup, acc)
		}
	}
	for _, acc := range dynamicAccounts {
		if !readySet[acc.Address.Hex()] {
			needSetup = append(needSetup, acc)
		}
	}

	if len(needSetup) == 0 {
		lg.logger.Info("all accounts already uniswap-ready from cache")
		return nil
	}

	lg.logger.Info("setting up non-ready accounts for Uniswap",
		slog.Int("needSetup", len(needSetup)),
		slog.Int("alreadyReady", len(readySet)),
	)

	bigChainID := big.NewInt(lg.cfg.ChainID)
	gasPrice := big.NewInt(lg.cfg.GasPrice)
	if err := uniswapBuilder.SetupAccounts(ctx, needSetup, lg.builderClient, bigChainID, gasPrice); err != nil {
		lg.logger.Warn("failed to setup accounts for Uniswap", "error", err)
		return nil
	}

	// Same stale-nonce hazard as the fresh-deploy path above: the 4 setup TXs per
	// account bypass the Account nonce counter, so resync from chain before the
	// load phase reserves any nonce. See the long comment at the other call site.
	//
	// FATAL, matching the fresh-deploy path. Warning and carrying on left those
	// accounts with counters behind chain state and then marked them uniswap-ready,
	// so the load phase started with stale nonces and could fail every submission —
	// precisely the failure this resync was added to prevent (PR #62 review).
	if err := lg.accountMgr.InitializeNoncesFromChain(ctx, lg.l2Client,
		len(lg.accountMgr.GetAccounts())+len(dynamicAccounts)); err != nil {
		return fmt.Errorf("resync nonces after incremental Uniswap setup: %w", err)
	}

	// Mark newly-setup accounts as uniswap-ready in cache
	newAddresses := make([]string, 0, len(needSetup))
	for _, a := range needSetup {
		if acc, ok := a.(*account.Account); ok {
			newAddresses = append(newAddresses, acc.Address.Hex())
		}
	}
	if err := lg.cacheStorage.MarkAccountsUniswapReady(ctx, chainID, newAddresses); err != nil {
		lg.logger.Warn("failed to mark accounts as uniswap-ready", "error", err)
	}
	return nil
}

func (lg *LoadGenerator) saveBaseContractsToCache(ctx context.Context, chainID int64) {
	if lg.cacheStorage == nil {
		return
	}

	now := time.Now()
	contracts := []storage.CachedContract{
		{Name: "ERC20", Address: lg.erc20Contract.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "GasConsumer", Address: lg.gasConsumerContract.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "NFT", Address: lg.nftContract.Hex(), ChainID: chainID, CreatedAt: now},
	}
	for _, c := range contracts {
		if err := lg.cacheStorage.SaveCachedContract(ctx, c); err != nil {
			lg.logger.Warn("failed to cache contract", "name", c.Name, "error", err)
		}
	}
	lg.logger.Info("cached base contract addresses")
}

func (lg *LoadGenerator) saveUniswapContractsToCache(ctx context.Context, chainID int64, ub *txbuilder.UniswapV3SwapBuilder) {
	if lg.cacheStorage == nil {
		return
	}

	contracts := ub.GetContracts()
	if contracts == nil {
		return
	}

	now := time.Now()
	entries := []storage.CachedContract{
		{Name: "uniswap:WETH9", Address: contracts.WETH9.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:USDC", Address: contracts.USDC.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:Factory", Address: contracts.Factory.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:SwapRouter", Address: contracts.SwapRouter.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:NonfungiblePositionManager", Address: contracts.NonfungiblePositionManager.Hex(), ChainID: chainID, CreatedAt: now},
		{Name: "uniswap:Pool", Address: contracts.Pool.Hex(), ChainID: chainID, CreatedAt: now},
	}
	for _, c := range entries {
		if err := lg.cacheStorage.SaveCachedContract(ctx, c); err != nil {
			lg.logger.Warn("failed to cache uniswap contract", "name", c.Name, "error", err)
		}
	}
	lg.logger.Info("cached Uniswap V3 contract addresses")
}
