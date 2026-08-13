package workload

import (
	"fmt"
	"math"
	"math/big"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// UsesRealisticMix reports whether a load pattern drives a weighted mix of
// transaction types from RealisticConfig.TxTypeRatios, rather than a single fixed type.
//
// This predicate exists so that every decision derived from "does this run use a tx
// mix" is made the same way. Previously the worker's type SELECTION covered both
// realistic patterns while contract DEPLOYMENT, ratio VALIDATION and metrics init
// checked PatternRealistic alone. For adaptive-realistic the two disagreed: an unset
// transactionType defaults to eth-transfer, so the deploy step concluded no contracts
// were needed and deployed none, while workers went on selecting erc20Transfer and
// uniswapSwap. Those transactions were then built against the zero address —
// 21,000 gas plus calldata, measured at 21,375 gas/tx where the intended 80/20 mix
// should have cost 75,700. The run looked valid and was not (PRST-4262/PRST-4293).
func UsesRealisticMix(pattern types.LoadPattern) bool {
	return pattern == types.PatternRealistic || pattern == types.PatternAdaptiveRealistic
}

// SelectRandomTxType selects a transaction type based on the configured ratios.
// Uses cumulative probability distribution to select based on weights.
func SelectRandomTxType(ratios types.TxTypeRatio, rnd *account.Rand) types.TransactionType {
	roll := rnd.IntN(100)
	cumulative := 0

	cumulative += ratios.EthTransfer
	if roll < cumulative {
		return types.TxTypeEthTransfer
	}

	cumulative += ratios.ERC20Transfer
	if roll < cumulative {
		return types.TxTypeERC20Transfer
	}

	cumulative += ratios.ERC20Approve
	if roll < cumulative {
		return types.TxTypeERC20Approve
	}

	cumulative += ratios.UniswapSwap
	if roll < cumulative {
		return types.TxTypeUniswapSwap
	}

	cumulative += ratios.StorageWrite
	if roll < cumulative {
		return types.TxTypeStorageWrite
	}

	return types.TxTypeHeavyCompute
}

// GenerateRandomTip generates a random tip based on the configured distribution.
// Returns the tip in wei. Supports exponential, power-law, and uniform distributions.
func GenerateRandomTip(cfg *types.RealisticTestConfig, rnd *account.Rand) *big.Int {
	if cfg == nil {
		return big.NewInt(0)
	}

	minGwei := cfg.MinTipGwei
	maxGwei := cfg.MaxTipGwei
	if maxGwei <= minGwei {
		maxGwei = minGwei + 1
	}

	var tipGwei float64
	switch cfg.TipDistribution {
	case types.TipDistUniform:
		tipGwei = minGwei + rnd.Float64()*(maxGwei-minGwei)

	case types.TipDistPowerLaw:
		u := rnd.Float64()
		if u < 0.001 {
			u = 0.001
		}
		alpha := 2.0
		tipGwei = minGwei + (maxGwei-minGwei)*(1-math.Pow(u, 1/alpha))

	case types.TipDistExponential:
		fallthrough
	default:
		u := rnd.Float64()
		if u >= 0.999 {
			u = 0.999
		}
		lambda := 3.0 / (maxGwei - minGwei)
		tipGwei = minGwei - (1/lambda)*math.Log(1-u)
		if tipGwei > maxGwei {
			tipGwei = maxGwei
		}
	}

	tipWei := big.NewInt(int64(tipGwei * 1e9))
	return tipWei
}

// DefaultRealisticConfig returns the default realistic test configuration.
// Used by adaptive-realistic pattern which uses sensible defaults.
func DefaultRealisticConfig() *types.RealisticTestConfig {
	return &types.RealisticTestConfig{
		NumAccounts: 100,
		TargetTPS:   500,
		TxTypeRatios: types.TxTypeRatio{
			EthTransfer:   50,
			ERC20Transfer: 20,
			ERC20Approve:  5,
			UniswapSwap:   15,
			StorageWrite:  5,
			HeavyCompute:  5,
		},
		TipDistribution: types.TipDistExponential,
		MinTipGwei:      0,
		MaxTipGwei:      10,
	}
}

// ValidateTxTypeRatios validates that transaction type ratios are valid.
// Returns an error if ratios don't sum to 100 or contain invalid values.
func ValidateTxTypeRatios(ratios types.TxTypeRatio) error {
	sum := ratios.EthTransfer + ratios.ERC20Transfer + ratios.ERC20Approve +
		ratios.UniswapSwap + ratios.StorageWrite + ratios.HeavyCompute

	if sum != 100 {
		return fmt.Errorf("txTypeRatios must sum to 100, got %d", sum)
	}

	for name, v := range map[string]int{
		"ethTransfer":   ratios.EthTransfer,
		"erc20Transfer": ratios.ERC20Transfer,
		"erc20Approve":  ratios.ERC20Approve,
		"uniswapSwap":   ratios.UniswapSwap,
		"storageWrite":  ratios.StorageWrite,
		"heavyCompute":  ratios.HeavyCompute,
	} {
		if v < 0 || v > 100 {
			return fmt.Errorf("ratio %s must be 0-100, got %d", name, v)
		}
	}

	return nil
}
