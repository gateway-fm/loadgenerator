package uniswapv3

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// Deployer handles Uniswap V3 contract deployment and setup.
type Deployer struct {
	client    rpc.Client
	chainID   *big.Int
	gasPrice  *big.Int
	useLegacy bool // Use legacy (type 0) transactions instead of EIP-1559
	logger    *slog.Logger
}

// ProgressCallback is called after each contract deployment or skip.
type ProgressCallback func(contractName string, deployed, total int)

// NewDeployer creates a new Uniswap V3 deployer.
func NewDeployer(client rpc.Client, chainID, gasPrice *big.Int, logger *slog.Logger) *Deployer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deployer{
		client:   client,
		chainID:  chainID,
		gasPrice: gasPrice,
		logger:   logger,
	}
}

// SetUseLegacy sets whether the deployer should use legacy (type 0) transactions.
func (d *Deployer) SetUseLegacy(useLegacy bool) {
	d.useLegacy = useLegacy
}

// DeployAll deploys all Uniswap V3 contracts in the correct order.
// Returns deployed contract addresses.
func (d *Deployer) DeployAll(ctx context.Context, deployer *account.Account) (*DeployedContracts, error) {
	return d.DeployAllWithProgress(ctx, deployer, nil)
}

// DeployAllWithProgress deploys all Uniswap V3 contracts with progress reporting.
// If onProgress is non-nil, it's called after each contract is deployed or skipped.
// Total contracts: 5 (WETH9, USDC, Factory, SwapRouter, NonfungiblePositionManager)
func (d *Deployer) DeployAllWithProgress(ctx context.Context, deployer *account.Account, onProgress ProgressCallback) (*DeployedContracts, error) {
	contracts := &DeployedContracts{}
	const totalContracts = 5 // Not counting Pool which is created via factory

	startNonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
	if err != nil {
		return nil, fmt.Errorf("get nonce: %w", err)
	}

	// Deploy order matters due to constructor dependencies:
	// 1. WETH9 (no deps)
	// 2. MockUSDC (no deps)
	// 3. UniswapV3Factory (no deps)
	// 4. SwapRouter (needs Factory, WETH9)
	// 5. NonfungiblePositionManager (needs Factory, WETH9)

	// Track how many contracts we've processed
	processed := 0
	nonce := startNonce

	// Helper to check if contract exists and either skip or deploy
	deployOrSkip := func(name string, bytecode []byte, expectedNonceOffset uint64) (common.Address, error) {
		expectedAddr := ComputeContractAddress(deployer.Address, startNonce+expectedNonceOffset)

		exists, err := d.checkContractExists(ctx, expectedAddr)
		if err != nil {
			d.logger.Warn("Failed to check contract existence, will deploy",
				slog.String("name", name),
				slog.String("error", err.Error()),
			)
		} else if exists {
			d.logger.Info("Contract already deployed, skipping",
				slog.String("name", name),
				slog.String("address", expectedAddr.Hex()),
			)
			processed++
			if onProgress != nil {
				onProgress(name, processed, totalContracts)
			}
			return expectedAddr, nil
		}

		// Need to deploy - fetch fresh nonce
		currentNonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
		if err != nil {
			return common.Address{}, fmt.Errorf("get nonce: %w", err)
		}

		d.logger.Info("Deploying "+name+"...", slog.Uint64("nonce", currentNonce))
		addr, err := d.deployContract(ctx, deployer, name, bytecode, currentNonce)
		if err != nil {
			return common.Address{}, err
		}

		processed++
		if onProgress != nil {
			onProgress(name, processed, totalContracts)
		}
		return addr, nil
	}

	// 1. WETH9
	contracts.WETH9, err = deployOrSkip("WETH9", WETH9Bytecode, 0)
	if err != nil {
		return nil, fmt.Errorf("deploy WETH9: %w", err)
	}
	nonce++

	// 2. MockUSDC
	contracts.USDC, err = deployOrSkip("MockUSDC", MockUSDCBytecode, 1)
	if err != nil {
		return nil, fmt.Errorf("deploy MockUSDC: %w", err)
	}
	nonce++

	// 3. UniswapV3Factory
	contracts.Factory, err = deployOrSkip("UniswapV3Factory", UniswapV3FactoryBytecode, 2)
	if err != nil {
		return nil, fmt.Errorf("deploy Factory: %w", err)
	}
	nonce++

	// 4. SwapRouter (needs Factory, WETH9)
	swapRouterBytecode := appendConstructorArgs(SwapRouterBytecode, contracts.Factory, contracts.WETH9)
	contracts.SwapRouter, err = deployOrSkip("SwapRouter", swapRouterBytecode, 3)
	if err != nil {
		return nil, fmt.Errorf("deploy SwapRouter: %w", err)
	}
	nonce++

	// 5. NonfungiblePositionManager (needs Factory, WETH9)
	nftBytecode := appendConstructorArgs(NonfungiblePositionManagerBytecode, contracts.Factory, contracts.WETH9, common.Address{})
	contracts.NonfungiblePositionManager, err = deployOrSkip("NonfungiblePositionManager", nftBytecode, 4)
	if err != nil {
		return nil, fmt.Errorf("deploy NonfungiblePositionManager: %w", err)
	}

	// Suppress unused variable warning
	_ = nonce

	d.logger.Info("All Uniswap V3 contracts deployed",
		slog.String("weth9", contracts.WETH9.Hex()),
		slog.String("usdc", contracts.USDC.Hex()),
		slog.String("factory", contracts.Factory.Hex()),
		slog.String("swapRouter", contracts.SwapRouter.Hex()),
		slog.String("nftPositionManager", contracts.NonfungiblePositionManager.Hex()),
	)

	return contracts, nil
}

// checkContractExists checks if a contract is deployed at the given address.
func (d *Deployer) checkContractExists(ctx context.Context, addr common.Address) (bool, error) {
	code, err := d.client.GetCode(ctx, addr.Hex())
	if err != nil {
		return false, err
	}
	return code != "" && code != "0x", nil
}

// CreateAndInitializePool creates a WETH/USDC pool and initializes it with the given price.
// If the pool already exists and is initialized, this function skips creation.
func (d *Deployer) CreateAndInitializePool(ctx context.Context, deployer *account.Account, contracts *DeployedContracts, config PoolConfig) error {
	return d.CreateAndInitializePoolWithProgress(ctx, deployer, contracts, config, nil)
}

// CreateAndInitializePoolWithProgress creates a pool with progress reporting.
func (d *Deployer) CreateAndInitializePoolWithProgress(ctx context.Context, deployer *account.Account, contracts *DeployedContracts, config PoolConfig, onProgress ProgressCallback) error {
	// First check if the pool already exists by querying the factory
	getPoolData := EncodeGetPool(contracts.WETH9, contracts.USDC, config.Fee)
	callParams := map[string]any{
		"to":   contracts.Factory.Hex(),
		"data": "0x" + common.Bytes2Hex(getPoolData),
	}
	result, err := d.client.Call(ctx, "eth_call", []interface{}{callParams, "latest"})
	if err != nil {
		return fmt.Errorf("query pool address: %w", err)
	}

	var poolAddrHex string
	if err := json.Unmarshal(result, &poolAddrHex); err != nil {
		return fmt.Errorf("parse pool address response: %w", err)
	}
	poolAddr := common.HexToAddress(poolAddrHex)

	// Check if pool exists at that address
	if poolAddr != (common.Address{}) {
		exists, err := d.checkContractExists(ctx, poolAddr)
		if err == nil && exists {
			d.logger.Info("Pool already exists, skipping creation",
				slog.String("pool", poolAddr.Hex()),
			)
			contracts.Pool = poolAddr

			if onProgress != nil {
				onProgress("Pool", 1, 1)
			}
			return nil
		}
	}

	nonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
	if err != nil {
		return fmt.Errorf("get nonce: %w", err)
	}

	// 1. Create pool via Factory.createPool(tokenA, tokenB, fee)
	d.logger.Info("Creating pool...",
		slog.String("token0", contracts.WETH9.Hex()),
		slog.String("token1", contracts.USDC.Hex()),
		slog.Uint64("fee", uint64(config.Fee)),
	)

	createPoolData := EncodeCreatePool(contracts.WETH9, contracts.USDC, config.Fee)
	if err := d.sendTxAndWaitForReceipt(ctx, deployer, contracts.Factory, createPoolData, nil, nonce, "createPool"); err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	nonce++

	// Query factory for actual pool address (don't compute - our INIT_CODE_HASH may differ from mainnet)
	result, err = d.client.Call(ctx, "eth_call", []interface{}{callParams, "latest"})
	if err != nil {
		return fmt.Errorf("query pool address: %w", err)
	}

	if err := json.Unmarshal(result, &poolAddrHex); err != nil {
		return fmt.Errorf("parse pool address response: %w", err)
	}
	poolAddr = common.HexToAddress(poolAddrHex)
	if poolAddr == (common.Address{}) {
		return fmt.Errorf("pool was not created (factory returned zero address)")
	}
	contracts.Pool = poolAddr

	d.logger.Info("POOL_ADDR_FROM_FACTORY_QUERY",
		slog.String("pool", poolAddr.Hex()),
		slog.String("raw_response", poolAddrHex),
	)

	// Verify pool was created by checking its code exists
	code, err := d.client.GetCode(ctx, poolAddr.Hex())
	if err != nil {
		return fmt.Errorf("get pool code: %w", err)
	}
	if code == "0x" || len(code) <= 2 {
		return fmt.Errorf("pool was not created at expected address %s", poolAddr.Hex())
	}

	// 2. Initialize pool with price
	d.logger.Info("Initializing pool...",
		slog.String("pool", poolAddr.Hex()),
		slog.String("sqrtPriceX96", config.SqrtPriceX96.String()),
	)

	initData := EncodeInitialize(config.SqrtPriceX96)
	if err := d.sendTxAndWaitForReceipt(ctx, deployer, poolAddr, initData, nil, nonce, "initializePool"); err != nil {
		return fmt.Errorf("initialize pool: %w", err)
	}

	d.logger.Info("Pool created and initialized",
		slog.String("pool", poolAddr.Hex()),
	)

	if onProgress != nil {
		onProgress("Pool", 1, 1)
	}

	return nil
}

// ProvisionLiquidity adds initial liquidity to the pool.
func (d *Deployer) ProvisionLiquidity(ctx context.Context, deployer *account.Account, contracts *DeployedContracts, config PoolConfig) error {
	nonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
	if err != nil {
		return fmt.Errorf("get nonce: %w", err)
	}

	// 1. Mint USDC to deployer
	d.logger.Info("Minting USDC for liquidity...",
		slog.String("amount", config.LiquidityUSDC.String()),
	)
	mintData := EncodeMint(deployer.Address, config.LiquidityUSDC)
	if err := d.sendTx(ctx, deployer, contracts.USDC, mintData, nil, nonce); err != nil {
		return fmt.Errorf("mint USDC: %w", err)
	}
	nonce++

	// 2. Wrap ETH to WETH (send ETH value to WETH9 contract)
	d.logger.Info("Wrapping ETH to WETH...",
		slog.String("amount", config.LiquidityWETH.String()),
	)
	if err := d.sendTx(ctx, deployer, contracts.WETH9, EncodeDeposit(), config.LiquidityWETH, nonce); err != nil {
		return fmt.Errorf("wrap ETH: %w", err)
	}
	nonce++

	// 3. Approve NFTPositionManager to spend WETH
	d.logger.Info("Approving WETH for NFTPositionManager...")
	approveWETH := EncodeApprove(contracts.NonfungiblePositionManager, MaxUint256)
	if err := d.sendTx(ctx, deployer, contracts.WETH9, approveWETH, nil, nonce); err != nil {
		return fmt.Errorf("approve WETH: %w", err)
	}
	nonce++

	// 4. Approve NFTPositionManager to spend USDC - fire and forget (nonce ordering ensures execution order)
	d.logger.Info("Approving USDC for NFTPositionManager...")
	approveUSDC := EncodeApprove(contracts.NonfungiblePositionManager, MaxUint256)
	if err := d.sendTx(ctx, deployer, contracts.USDC, approveUSDC, nil, nonce); err != nil {
		return fmt.Errorf("approve USDC for NFT position manager: %w", err)
	}
	nonce++

	// 5. Add liquidity via NFTPositionManager.mint
	d.logger.Info("Adding liquidity to pool...")

	// Sort tokens for mint params
	token0, token1 := SortTokens(contracts.WETH9, contracts.USDC)
	var amount0, amount1 *big.Int
	if token0 == contracts.WETH9 {
		amount0 = config.LiquidityWETH
		amount1 = config.LiquidityUSDC
	} else {
		amount0 = config.LiquidityUSDC
		amount1 = config.LiquidityWETH
	}

	deadline := big.NewInt(time.Now().Unix() + 3600)

	// Compute what address the Position Manager thinks the pool is at
	computedPoolAddr := ComputePoolAddress(contracts.Factory, token0, token1, config.Fee)

	d.logger.Info("Mint params",
		slog.String("token0", token0.Hex()),
		slog.String("token1", token1.Hex()),
		slog.Uint64("fee", uint64(config.Fee)),
		slog.Int64("tickLower", int64(config.TickLower)),
		slog.Int64("tickUpper", int64(config.TickUpper)),
		slog.String("amount0", amount0.String()),
		slog.String("amount1", amount1.String()),
		slog.String("recipient", deployer.Address.Hex()),
		slog.String("deadline", deadline.String()),
		slog.String("actualPool", contracts.Pool.Hex()),
		slog.String("computedPool", computedPoolAddr.Hex()),
		slog.Bool("poolAddressMatch", contracts.Pool == computedPoolAddr),
		slog.String("nftManager", contracts.NonfungiblePositionManager.Hex()),
	)

	// Debug: check pool slot0 to verify it's initialized
	slot0Data := SelectorSlot0
	slot0Params := map[string]interface{}{
		"to":   contracts.Pool.Hex(),
		"data": "0x" + common.Bytes2Hex(slot0Data),
	}
	slot0Result, err := d.client.Call(ctx, "eth_call", []interface{}{slot0Params, "latest"})
	if err != nil {
		d.logger.Error("Failed to call pool.slot0()", slog.String("error", err.Error()))
	} else {
		d.logger.Info("Pool slot0 response", slog.String("result", string(slot0Result)))
	}

	// Debug: check deployer balances
	balWETH := EncodeBalanceOf(deployer.Address)
	balWETHParams := map[string]interface{}{
		"to":   contracts.WETH9.Hex(),
		"data": "0x" + common.Bytes2Hex(balWETH),
	}
	wethResult, _ := d.client.Call(ctx, "eth_call", []interface{}{balWETHParams, "latest"})
	d.logger.Info("Deployer WETH balance", slog.String("result", string(wethResult)))

	balUSDC := EncodeBalanceOf(deployer.Address)
	balUSDCParams := map[string]interface{}{
		"to":   contracts.USDC.Hex(),
		"data": "0x" + common.Bytes2Hex(balUSDC),
	}
	usdcResult, _ := d.client.Call(ctx, "eth_call", []interface{}{balUSDCParams, "latest"})
	d.logger.Info("Deployer USDC balance", slog.String("result", string(usdcResult)))

	// Debug: check allowances
	allowWETH := EncodeAllowance(deployer.Address, contracts.NonfungiblePositionManager)
	allowWETHParams := map[string]interface{}{
		"to":   contracts.WETH9.Hex(),
		"data": "0x" + common.Bytes2Hex(allowWETH),
	}
	allowWETHResult, _ := d.client.Call(ctx, "eth_call", []interface{}{allowWETHParams, "latest"})
	d.logger.Info("WETH allowance for NFTManager", slog.String("result", string(allowWETHResult)))

	allowUSDC := EncodeAllowance(deployer.Address, contracts.NonfungiblePositionManager)
	allowUSDCParams := map[string]interface{}{
		"to":   contracts.USDC.Hex(),
		"data": "0x" + common.Bytes2Hex(allowUSDC),
	}
	allowUSDCResult, _ := d.client.Call(ctx, "eth_call", []interface{}{allowUSDCParams, "latest"})
	d.logger.Info("USDC allowance for NFTManager", slog.String("result", string(allowUSDCResult)))

	// For test environments, use 0 minimums to avoid slippage issues during setup.
	// The pool price may not perfectly match expected ratios, and we want setup to succeed.
	// In production, this would use proper slippage protection.
	amount0Min := big.NewInt(0)
	amount1Min := big.NewInt(0)

	mintParams := MintParams{
		Token0:         token0,
		Token1:         token1,
		Fee:            config.Fee,
		TickLower:      config.TickLower,
		TickUpper:      config.TickUpper,
		Amount0Desired: amount0,
		Amount1Desired: amount1,
		Amount0Min:     amount0Min,
		Amount1Min:     amount1Min,
		Recipient:      deployer.Address,
		Deadline:       deadline,
	}

	mintData = EncodeMintPosition(mintParams)

	// Debug: verify Position Manager is deployed correctly by calling WETH9()
	weth9Selector := selector("WETH9()")
	pmCheckParams := map[string]interface{}{
		"to":   contracts.NonfungiblePositionManager.Hex(),
		"data": "0x" + common.Bytes2Hex(weth9Selector),
	}
	pmWeth9Result, err := d.client.Call(ctx, "eth_call", []interface{}{pmCheckParams, "latest"})
	if err != nil {
		d.logger.Error("Position Manager WETH9() call failed", slog.String("error", err.Error()))
	} else {
		d.logger.Info("Position Manager WETH9()", slog.String("result", string(pmWeth9Result)))
	}

	// Verify Position Manager's factory() matches ours
	factorySelector := selector("factory()")
	pmFactoryParams := map[string]interface{}{
		"to":   contracts.NonfungiblePositionManager.Hex(),
		"data": "0x" + common.Bytes2Hex(factorySelector),
	}
	pmFactoryResult, err := d.client.Call(ctx, "eth_call", []interface{}{pmFactoryParams, "latest"})
	if err != nil {
		d.logger.Error("Position Manager factory() call failed", slog.String("error", err.Error()))
	} else {
		d.logger.Info("Position Manager factory()", slog.String("result", string(pmFactoryResult)))
	}

	// Debug: log the call data
	d.logger.Info("Mint call data",
		slog.Int("dataLen", len(mintData)),
		slog.String("selector", common.Bytes2Hex(mintData[:4])),
		slog.String("nftManager", contracts.NonfungiblePositionManager.Hex()),
		slog.Uint64("nonce", nonce),
	)

	// Try debug trace call to get revert reason
	traceParams := map[string]interface{}{
		"from": deployer.Address.Hex(),
		"to":   contracts.NonfungiblePositionManager.Hex(),
		"data": "0x" + common.Bytes2Hex(mintData),
		"gas":  "0x1E8480", // 2M gas for debug
	}
	traceOptions := map[string]interface{}{
		"tracer": "callTracer",
	}
	traceResult, err := d.client.Call(ctx, "debug_traceCall", []interface{}{traceParams, "latest", traceOptions})
	if err != nil {
		d.logger.Error("Debug trace call failed",
			slog.String("error", err.Error()),
		)
		// Fallback to regular eth_call
		staticResult, err := d.client.Call(ctx, "eth_call", []interface{}{traceParams, "latest"})
		if err != nil {
			d.logger.Error("Static call to mint() also failed",
				slog.String("error", err.Error()),
				slog.String("fullData", "0x"+common.Bytes2Hex(mintData)),
			)
		} else {
			d.logger.Info("Static call to mint() succeeded", slog.String("result", string(staticResult)))
		}
	} else {
		d.logger.Info("Debug trace result", slog.String("result", string(traceResult)))
	}

	if err := d.sendTxAndWaitForReceipt(ctx, deployer, contracts.NonfungiblePositionManager, mintData, nil, nonce, "mintLiquidity"); err != nil {
		return fmt.Errorf("add liquidity: %w", err)
	}

	d.logger.Info("Liquidity provisioned successfully",
		slog.String("weth", config.LiquidityWETH.String()),
		slog.String("usdc", config.LiquidityUSDC.String()),
	)

	return nil
}

// AccountSetupConfig holds configuration for setting up test accounts.
type AccountSetupConfig struct {
	USDCAmount *big.Int // Amount of USDC to mint
	WETHAmount *big.Int // Amount of ETH to wrap
}

// DefaultAccountSetupConfig returns a default account setup configuration.
func DefaultAccountSetupConfig() AccountSetupConfig {
	return AccountSetupConfig{
		USDCAmount: new(big.Int).Mul(big.NewInt(10000), big.NewInt(1e6)),  // 10,000 USDC
		WETHAmount: new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18)),     // 5 ETH
	}
}

// SetupAccount sets up a single account for Uniswap swapping.
// This mints USDC, wraps ETH to WETH, and approves the SwapRouter.
// Waits for the final approve to be confirmed to ensure all prior TXs are also confirmed.
func (d *Deployer) SetupAccount(ctx context.Context, acc *account.Account, contracts *DeployedContracts, config AccountSetupConfig) error {
	_, err := d.setupAccountFireAndForget(ctx, acc, contracts, config)
	if err != nil {
		return err
	}
	// Wait handled by setupAccountFireAndForget when txHash is empty
	return nil
}

// setupAccountFireAndForget sends all setup TXs for an account without waiting.
// Returns the last TX hash which can be used to wait for confirmation.
func (d *Deployer) setupAccountFireAndForget(ctx context.Context, acc *account.Account, contracts *DeployedContracts, config AccountSetupConfig) (string, error) {
	nonce, err := d.client.GetNonce(ctx, acc.Address.Hex())
	if err != nil {
		return "", fmt.Errorf("get nonce: %w", err)
	}

	// 1. Mint USDC
	mintData := EncodeMint(acc.Address, config.USDCAmount)
	if err := d.sendTx(ctx, acc, contracts.USDC, mintData, nil, nonce); err != nil {
		return "", fmt.Errorf("mint USDC: %w", err)
	}
	nonce++

	// 2. Wrap ETH to WETH
	if err := d.sendTx(ctx, acc, contracts.WETH9, EncodeDeposit(), config.WETHAmount, nonce); err != nil {
		return "", fmt.Errorf("wrap ETH: %w", err)
	}
	nonce++

	// 3. Approve SwapRouter to spend WETH
	approveWETH := EncodeApprove(contracts.SwapRouter, MaxUint256)
	if err := d.sendTx(ctx, acc, contracts.WETH9, approveWETH, nil, nonce); err != nil {
		return "", fmt.Errorf("approve WETH: %w", err)
	}
	nonce++

	// 4. Approve SwapRouter to spend USDC - return TX hash for later confirmation
	approveUSDC := EncodeApprove(contracts.SwapRouter, MaxUint256)
	txHash, err := d.sendTxReturnHash(ctx, acc, contracts.USDC, approveUSDC, nil, nonce)
	if err != nil {
		return "", fmt.Errorf("approve USDC: %w", err)
	}

	return txHash, nil
}

// SetupAccounts sets up multiple accounts in parallel.
// Uses concurrent goroutines to fire all setup TXs, then waits for confirmations.
func (d *Deployer) SetupAccounts(ctx context.Context, accounts []*account.Account, contracts *DeployedContracts, config AccountSetupConfig) error {
	if len(accounts) == 0 {
		return nil
	}

	// Concurrency limit to avoid overwhelming RPC
	const maxConcurrent = 50
	sem := make(chan struct{}, maxConcurrent)

	// Results collection
	type result struct {
		idx    int
		txHash string
		err    error
	}
	results := make(chan result, len(accounts))

	d.logger.Info("Setting up accounts in parallel",
		slog.Int("numAccounts", len(accounts)),
		slog.Int("concurrency", maxConcurrent),
	)

	// Fire all account setups in parallel
	var wg sync.WaitGroup
	for i, acc := range accounts {
		wg.Add(1)
		go func(idx int, a *account.Account) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			txHash, err := d.setupAccountFireAndForget(ctx, a, contracts, config)
			results <- result{idx: idx, txHash: txHash, err: err}
		}(i, acc)
	}

	// Close results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results and TX hashes
	txHashes := make([]string, len(accounts))
	for r := range results {
		if r.err != nil {
			return fmt.Errorf("setup account %s: %w", accounts[r.idx].Address.Hex(), r.err)
		}
		txHashes[r.idx] = r.txHash
	}

	d.logger.Info("All setup TXs sent, waiting for confirmations",
		slog.Int("numTxs", len(accounts)*4),
	)

	// Wait for all final TXs to confirm (implies all prior TXs confirmed due to nonce ordering)
	// Process in batches to avoid timeout issues
	const batchSize = 20
	for i := 0; i < len(txHashes); i += batchSize {
		end := i + batchSize
		if end > len(txHashes) {
			end = len(txHashes)
		}
		batch := txHashes[i:end]

		var batchWg sync.WaitGroup
		errCh := make(chan error, len(batch))

		for _, txHash := range batch {
			batchWg.Add(1)
			go func(hash string) {
				defer batchWg.Done()
				if err := d.waitForTxReceipt(ctx, hash, 60*time.Second); err != nil {
					errCh <- err
				}
			}(txHash)
		}

		batchWg.Wait()
		close(errCh)

		for err := range errCh {
			return fmt.Errorf("waiting for setup TX confirmation: %w", err)
		}
	}

	d.logger.Info("All setup TXs confirmed")

	// Verify a sample of accounts have the expected balances
	// This catches silent failures in the setup TXs
	sampleIndices := d.getSampleIndices(len(accounts))
	for _, i := range sampleIndices {
		acc := accounts[i]
		// Check USDC balance
		usdcBal, err := d.getTokenBalance(ctx, contracts.USDC, acc.Address)
		if err != nil {
			return fmt.Errorf("failed to get USDC balance for %s: %w", acc.Address.Hex(), err)
		}
		if usdcBal.Cmp(config.USDCAmount) < 0 {
			return fmt.Errorf("account %s has insufficient USDC: got %s, want %s (setup TX may have failed)",
				acc.Address.Hex(), usdcBal.String(), config.USDCAmount.String())
		}
		// Check WETH balance
		wethBal, err := d.getTokenBalance(ctx, contracts.WETH9, acc.Address)
		if err != nil {
			return fmt.Errorf("failed to get WETH balance for %s: %w", acc.Address.Hex(), err)
		}
		if wethBal.Cmp(config.WETHAmount) < 0 {
			return fmt.Errorf("account %s has insufficient WETH: got %s, want %s (setup TX may have failed)",
				acc.Address.Hex(), wethBal.String(), config.WETHAmount.String())
		}
	}
	d.logger.Info("Account setup verification passed", slog.Int("sampleSize", len(sampleIndices)))

	return nil
}

// getSampleIndices returns a deduplicated list of sample indices for verification.
func (d *Deployer) getSampleIndices(n int) []int {
	sampleIndices := []int{}
	if n > 0 {
		sampleIndices = append(sampleIndices, 0) // First account
	}
	if n > 2 {
		sampleIndices = append(sampleIndices, n/4)   // 25%
		sampleIndices = append(sampleIndices, n/2)   // Middle
		sampleIndices = append(sampleIndices, 3*n/4) // 75%
	}
	if n > 1 {
		sampleIndices = append(sampleIndices, n-1) // Last account
	}
	// Deduplicate indices
	seen := make(map[int]bool)
	uniqueIndices := []int{}
	for _, idx := range sampleIndices {
		if !seen[idx] {
			seen[idx] = true
			uniqueIndices = append(uniqueIndices, idx)
		}
	}
	return uniqueIndices
}

// waitForTxReceipt waits for a transaction receipt with timeout.
func (d *Deployer) waitForTxReceipt(ctx context.Context, txHash string, timeout time.Duration) error {
	timeoutCh := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeoutCh:
			return fmt.Errorf("timeout waiting for TX receipt: %s", txHash)
		case <-ticker.C:
			receipt, err := d.client.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				continue
			}
			if receipt != nil {
				if receipt.Status == 0 {
					return fmt.Errorf("TX failed (status=0): %s", txHash)
				}
				return nil
			}
		}
	}
}

// getTokenBalance returns the ERC20 token balance for an address.
func (d *Deployer) getTokenBalance(ctx context.Context, token, addr common.Address) (*big.Int, error) {
	data := EncodeBalanceOf(addr)
	callParams := map[string]interface{}{
		"to":   token.Hex(),
		"data": "0x" + common.Bytes2Hex(data),
	}
	result, err := d.client.Call(ctx, "eth_call", []interface{}{callParams, "latest"})
	if err != nil {
		return nil, err
	}
	// Parse result - json.RawMessage is quoted hex string like "0x..."
	resultStr := string(result)
	// Trim quotes if present
	resultStr = strings.Trim(resultStr, "\"")
	if resultStr == "" || resultStr == "0x" {
		return big.NewInt(0), nil
	}
	resultBytes := common.FromHex(resultStr)
	if len(resultBytes) == 0 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(resultBytes), nil
}

// deployContract deploys a contract and waits for confirmation.
func (d *Deployer) deployContract(ctx context.Context, deployer *account.Account, name string, bytecode []byte, nonce uint64) (common.Address, error) {
	// Calculate expected address
	contractAddr := ComputeContractAddress(deployer.Address, nonce)

	// Build deployment transaction
	var tx *types.Transaction
	if d.useLegacy {
		tx = types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: d.gasPrice,
			Gas:      8000000, // High gas limit for deployment
			To:       nil,     // Contract creation
			Value:    big.NewInt(0),
			Data:     bytecode,
		})
	} else {
		tx = types.NewTx(&types.DynamicFeeTx{
			ChainID:   d.chainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(0),
			GasFeeCap: d.gasPrice,
			Gas:       8000000, // High gas limit for deployment
			To:        nil,     // Contract creation
			Value:     big.NewInt(0),
			Data:      bytecode,
		})
	}

	// Sign and send
	signedTx, err := d.signAndSendReturnHash(ctx, deployer, tx)
	if err != nil {
		return common.Address{}, fmt.Errorf("send deployment tx: %w", err)
	}

	txHash := signedTx.Hash().Hex()
	d.logger.Info("Deploying contract",
		slog.String("name", name),
		slog.String("expected", contractAddr.Hex()),
		slog.String("txHash", txHash),
		slog.Int("bytecodeLen", len(bytecode)),
	)

	// Wait for deployment and check receipt
	return d.waitForDeploymentWithReceipt(ctx, name, contractAddr, txHash)
}

// sendTx sends a transaction to a contract.
func (d *Deployer) sendTx(ctx context.Context, sender *account.Account, to common.Address, data []byte, value *big.Int, nonce uint64) error {
	_, err := d.sendTxReturnHash(ctx, sender, to, data, value, nonce)
	return err
}

// sendTxReturnHash sends a transaction and returns the TX hash for later confirmation.
func (d *Deployer) sendTxReturnHash(ctx context.Context, sender *account.Account, to common.Address, data []byte, value *big.Int, nonce uint64) (string, error) {
	if value == nil {
		value = big.NewInt(0)
	}

	var tx *types.Transaction
	if d.useLegacy {
		tx = types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: d.gasPrice,
			Gas:      500000, // Generous gas limit for contract calls
			To:       &to,
			Value:    value,
			Data:     data,
		})
	} else {
		tx = types.NewTx(&types.DynamicFeeTx{
			ChainID:   d.chainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(0),
			GasFeeCap: d.gasPrice,
			Gas:       500000, // Generous gas limit for contract calls
			To:        &to,
			Value:     value,
			Data:      data,
		})
	}

	signedTx, err := d.signAndSendReturnHash(ctx, sender, tx)
	if err != nil {
		return "", err
	}
	return signedTx.Hash().Hex(), nil
}

// signAndSend signs and sends a transaction with retry logic.
func (d *Deployer) signAndSend(ctx context.Context, sender *account.Account, tx *types.Transaction) error {
	_, err := d.signAndSendReturnHash(ctx, sender, tx)
	return err
}

// signAndSendReturnHash signs and sends a transaction, returning the signed tx for hash access.
func (d *Deployer) signAndSendReturnHash(ctx context.Context, sender *account.Account, tx *types.Transaction) (*types.Transaction, error) {
	signer := types.LatestSignerForChainID(d.chainID)
	signedTx, err := types.SignTx(tx, signer, sender.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}

	rlp, err := signedTx.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal tx: %w", err)
	}

	err = d.client.SendRawTransaction(ctx, rlp)
	if err != nil {
		return nil, fmt.Errorf("send raw tx: %w", err)
	}

	return signedTx, nil
}

// sendTxAndWaitForReceipt sends a transaction and waits for its receipt.
func (d *Deployer) sendTxAndWaitForReceipt(ctx context.Context, sender *account.Account, to common.Address, data []byte, value *big.Int, nonce uint64, name string) error {
	if value == nil {
		value = big.NewInt(0)
	}

	// Use higher gas limit for pool-related transactions (createPool deploys a contract via CREATE2)
	gasLimit := uint64(500000)
	if name == "createPool" || name == "initializePool" || name == "mintLiquidity" {
		gasLimit = 8000000 // High limit for complex operations
	}

	var tx *types.Transaction
	if d.useLegacy {
		tx = types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: d.gasPrice,
			Gas:      gasLimit,
			To:       &to,
			Value:    value,
			Data:     data,
		})
	} else {
		tx = types.NewTx(&types.DynamicFeeTx{
			ChainID:   d.chainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(0),
			GasFeeCap: d.gasPrice,
			Gas:       gasLimit,
			To:        &to,
			Value:     value,
			Data:      data,
		})
	}

	signedTx, err := d.signAndSendReturnHash(ctx, sender, tx)
	if err != nil {
		return err
	}

	txHash := signedTx.Hash().Hex()

	// Wait for receipt
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for %s tx receipt (txHash: %s)", name, txHash)
		case <-ticker.C:
			receipt, err := d.client.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				continue
			}
			if receipt != nil {
				if receipt.Status == 0 {
					return fmt.Errorf("%s tx failed (status=0, gasUsed=%d, txHash=%s)", name, receipt.GasUsed, txHash)
				}
				d.logger.Debug("TX confirmed",
					slog.String("name", name),
					slog.String("txHash", txHash),
					slog.Uint64("gasUsed", receipt.GasUsed),
				)
				return nil
			}
		}
	}
}

// waitForDeploymentWithReceipt waits for deployment and checks receipt status.
func (d *Deployer) waitForDeploymentWithReceipt(ctx context.Context, name string, addr common.Address, txHash string) (common.Address, error) {
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return common.Address{}, ctx.Err()
		case <-timeout:
			return common.Address{}, fmt.Errorf("timeout waiting for %s deployment at %s (txHash: %s)", name, addr.Hex(), txHash)
		case <-ticker.C:
			// First check the receipt for status
			receipt, err := d.client.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				d.logger.Debug("Error getting receipt", slog.String("error", err.Error()))
				continue
			}
			if receipt != nil {
				if receipt.Status == 0 {
					// Transaction failed
					return common.Address{}, fmt.Errorf("%s deployment tx failed (status=0, gasUsed=%d, txHash=%s)", name, receipt.GasUsed, txHash)
				}
				// Status == 1 means success, check for code
				code, err := d.client.GetCode(ctx, addr.Hex())
				if err != nil {
					d.logger.Debug("Error getting code", slog.String("error", err.Error()))
					continue
				}
				if code != "0x" && len(code) > 2 {
					d.logger.Info("Contract deployed",
						slog.String("name", name),
						slog.String("address", addr.Hex()),
						slog.Uint64("gasUsed", receipt.GasUsed),
					)
					return addr, nil
				}
			}
		}
	}
}

// appendConstructorArgs appends constructor arguments to bytecode.
func appendConstructorArgs(bytecode []byte, args ...common.Address) []byte {
	result := make([]byte, len(bytecode)+len(args)*32)
	copy(result, bytecode)
	for i, arg := range args {
		copy(result[len(bytecode)+i*32+12:], arg.Bytes())
	}
	return result
}
