package txbuilder

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/uniswapv3"
	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// UniswapV3SwapBuilder builds real Uniswap V3 swap transactions.
type UniswapV3SwapBuilder struct {
	contracts   *uniswapv3.DeployedContracts
	poolConfig  uniswapv3.PoolConfig
	setupConfig uniswapv3.AccountSetupConfig
	deployed    bool
	mu          sync.RWMutex
}

// NewUniswapV3SwapBuilder creates a new Uniswap V3 swap builder.
func NewUniswapV3SwapBuilder() *UniswapV3SwapBuilder {
	return &UniswapV3SwapBuilder{
		contracts: &uniswapv3.DeployedContracts{},
	}
}

// Type returns the transaction type identifier.
func (b *UniswapV3SwapBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeUniswapSwap
}

// GasLimit returns the gas limit for Uniswap V3 swaps (~250k).
func (b *UniswapV3SwapBuilder) GasLimit() uint64 {
	return 250000
}

// Build creates a swap transaction using SwapRouter.exactInputSingle.
func (b *UniswapV3SwapBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.deployed {
		return nil, fmt.Errorf("Uniswap V3 contracts not deployed")
	}

	// Alternate swap direction based on nonce for balance
	var tokenIn, tokenOut common.Address
	if params.Nonce%2 == 0 {
		// Even nonce: WETH → USDC
		tokenIn = b.contracts.WETH9
		tokenOut = b.contracts.USDC
	} else {
		// Odd nonce: USDC → WETH
		tokenIn = b.contracts.USDC
		tokenOut = b.contracts.WETH9
	}

	// Small swap amount to avoid depleting balances
	var amountIn *big.Int
	if tokenIn == b.contracts.WETH9 {
		amountIn = big.NewInt(1e15) // 0.001 ETH
	} else {
		amountIn = big.NewInt(1e6) // 1 USDC
	}

	// Use a far-future deadline (24 hours) to prevent expiry during high-throughput tests.
	// Transactions can queue in the mempool for extended periods under load.
	swapParams := uniswapv3.ExactInputSingleParams{
		TokenIn:           tokenIn,
		TokenOut:          tokenOut,
		Fee:               b.poolConfig.Fee,
		Recipient:         params.From,                           // Send output tokens back to sender
		Deadline:          big.NewInt(time.Now().Unix() + 86400), // 24 hours
		AmountIn:          amountIn,
		AmountOutMinimum:  big.NewInt(0), // Accept any output for testing
		SqrtPriceLimitX96: big.NewInt(0), // No price limit
	}

	data := uniswapv3.EncodeExactInputSingle(swapParams)

	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   params.ChainID,
		Nonce:     params.Nonce,
		GasTipCap: params.GasTipCap,
		GasFeeCap: params.GasFeeCap,
		Gas:       b.GasLimit(),
		To:        &b.contracts.SwapRouter,
		Value:     big.NewInt(0),
		Data:      data,
	}), nil
}

// RequiresContract returns true - Uniswap V3 needs contracts deployed.
func (b *UniswapV3SwapBuilder) RequiresContract() bool {
	return true
}

// ContractBytecode returns nil - we deploy multiple contracts via DeployContracts.
func (b *UniswapV3SwapBuilder) ContractBytecode() []byte {
	return nil
}

// SetContractAddress is a no-op for complex builders.
func (b *UniswapV3SwapBuilder) SetContractAddress(addr common.Address) {
	// No-op - we use DeployContracts instead
}

// IsComplex returns true - this is a complex builder.
func (b *UniswapV3SwapBuilder) IsComplex() bool {
	return true
}

// DeployContracts deploys all Uniswap V3 contracts.
func (b *UniswapV3SwapBuilder) DeployContracts(ctx context.Context, client any, deployer any, chainID, gasPrice *big.Int, logger any) error {
	return b.DeployContractsWithProgress(ctx, client, deployer, chainID, gasPrice, logger, nil)
}

// DeployContractsWithProgress deploys all Uniswap V3 contracts with progress reporting.
// Total contracts: 7 (WETH9, USDC, Factory, SwapRouter, NonfungiblePositionManager, Pool, Liquidity)
func (b *UniswapV3SwapBuilder) DeployContractsWithProgress(ctx context.Context, client any, deployer any, chainID, gasPrice *big.Int, logger any, onProgress func(name string, done, total int)) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	rpcClient, ok := client.(rpc.Client)
	if !ok {
		return fmt.Errorf("client must be rpc.Client")
	}

	deployerAccount, ok := deployer.(*account.Account)
	if !ok {
		return fmt.Errorf("deployer must be *account.Account")
	}

	var slogger *slog.Logger
	if logger != nil {
		slogger, _ = logger.(*slog.Logger)
	}
	if slogger == nil {
		slogger = slog.Default()
	}

	slogger.Info("Deploying Uniswap V3 contracts...")

	d := uniswapv3.NewDeployer(rpcClient, chainID, gasPrice, slogger)

	// Total: 5 contracts + 1 pool + 1 liquidity = 7 steps
	const totalSteps = 7
	currentStep := 0

	// Wrapper to adapt uniswapv3.ProgressCallback to our callback
	contractProgress := func(name string, _, _ int) {
		currentStep++
		if onProgress != nil {
			onProgress(name, currentStep, totalSteps)
		}
	}

	// Deploy all contracts (5 contracts)
	contracts, err := d.DeployAllWithProgress(ctx, deployerAccount, contractProgress)
	if err != nil {
		return fmt.Errorf("deploy contracts: %w", err)
	}
	b.contracts = contracts

	// Initialize pool config with deployed addresses
	b.poolConfig = uniswapv3.DefaultPoolConfig(contracts.WETH9, contracts.USDC)

	// Create and initialize pool (step 6)
	poolProgress := func(name string, _, _ int) {
		currentStep++
		if onProgress != nil {
			onProgress(name, currentStep, totalSteps)
		}
	}
	if err := d.CreateAndInitializePoolWithProgress(ctx, deployerAccount, contracts, b.poolConfig, poolProgress); err != nil {
		return fmt.Errorf("create pool: %w", err)
	}

	// Provision liquidity (step 7)
	if err := d.ProvisionLiquidity(ctx, deployerAccount, contracts, b.poolConfig); err != nil {
		return fmt.Errorf("provision liquidity: %w", err)
	}
	currentStep++
	if onProgress != nil {
		onProgress("Liquidity", currentStep, totalSteps)
	}

	b.setupConfig = uniswapv3.DefaultAccountSetupConfig()
	b.deployed = true

	slogger.Info("Uniswap V3 deployment complete",
		slog.String("pool", contracts.Pool.Hex()),
		slog.String("swapRouter", contracts.SwapRouter.Hex()),
	)

	return nil
}

// SetupAccounts prepares accounts for Uniswap swapping.
func (b *UniswapV3SwapBuilder) SetupAccounts(ctx context.Context, accounts []any, client any, chainID, gasPrice *big.Int) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.deployed {
		return fmt.Errorf("contracts not deployed")
	}

	rpcClient, ok := client.(rpc.Client)
	if !ok {
		return fmt.Errorf("client must be rpc.Client")
	}

	// Convert []any to []*account.Account
	accs := make([]*account.Account, 0, len(accounts))
	for _, a := range accounts {
		acc, ok := a.(*account.Account)
		if !ok {
			return fmt.Errorf("account must be *account.Account")
		}
		accs = append(accs, acc)
	}

	d := uniswapv3.NewDeployer(rpcClient, chainID, gasPrice, nil)
	return d.SetupAccounts(ctx, accs, b.contracts, b.setupConfig)
}

// GetContracts returns the deployed contract addresses.
func (b *UniswapV3SwapBuilder) GetContracts() *uniswapv3.DeployedContracts {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.contracts
}

// RestoreContracts restores previously deployed contracts from cache,
// allowing warm starts without redeployment.
func (b *UniswapV3SwapBuilder) RestoreContracts(contracts *uniswapv3.DeployedContracts) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.contracts = contracts
	b.poolConfig = uniswapv3.DefaultPoolConfig(contracts.WETH9, contracts.USDC)
	b.setupConfig = uniswapv3.DefaultAccountSetupConfig()
	b.deployed = true
}

// IsDeployed returns whether contracts have been deployed.
func (b *UniswapV3SwapBuilder) IsDeployed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.deployed
}
