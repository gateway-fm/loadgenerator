package uniswapv3

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// DeployedContracts holds addresses of all deployed Uniswap V3 contracts.
type DeployedContracts struct {
	WETH9                      common.Address
	USDC                       common.Address
	Factory                    common.Address
	SwapRouter                 common.Address
	NonfungiblePositionManager common.Address
	Pool                       common.Address
}

// PoolConfig holds configuration for creating and initializing a pool.
type PoolConfig struct {
	Token0        common.Address
	Token1        common.Address
	Fee           uint32   // Fee tier (500 = 0.05%, 3000 = 0.3%, 10000 = 1%)
	SqrtPriceX96  *big.Int // Initial price as sqrt(price) * 2^96
	TickLower     int32    // Lower tick bound for liquidity
	TickUpper     int32    // Upper tick bound for liquidity
	LiquidityWETH *big.Int // Amount of WETH to add as liquidity
	LiquidityUSDC *big.Int // Amount of USDC to add as liquidity
}

// ExactInputSingleParams holds parameters for exactInputSingle swap.
type ExactInputSingleParams struct {
	TokenIn           common.Address
	TokenOut          common.Address
	Fee               uint32
	Recipient         common.Address
	Deadline          *big.Int
	AmountIn          *big.Int
	AmountOutMinimum  *big.Int
	SqrtPriceLimitX96 *big.Int
}

// MaxUint256 is the maximum uint256 value (used for approvals).
var MaxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// DefaultPoolConfig returns a default pool configuration for WETH/USDC.
// Price: ~$2000 USDC per ETH
// Fee: 0.3% (3000)
// Full range liquidity
func DefaultPoolConfig(weth9, usdc common.Address) PoolConfig {
	// sqrtPriceX96 for 1 ETH = 2000 USDC (accounting for decimals)
	// USDC has 6 decimals, WETH has 18
	// Price = 2000 * 10^6 / 10^18 = 2000 / 10^12 = 2 * 10^-9
	// But we need to account for token ordering
	// sqrt(2000 * 10^-12) * 2^96 ≈ 3.543e30
	sqrtPriceX96, _ := new(big.Int).SetString("3543191142285914205922034323214", 10)

	return PoolConfig{
		Token0:        weth9, // Will be sorted by address
		Token1:        usdc,
		Fee:           3000, // 0.3%
		SqrtPriceX96:  sqrtPriceX96,
		TickLower:     -887220, // Full range
		TickUpper:     887220,
		LiquidityWETH: new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18)),   // 1000 ETH
		LiquidityUSDC: new(big.Int).Mul(big.NewInt(2000000), big.NewInt(1e6)), // 2M USDC
	}
}
