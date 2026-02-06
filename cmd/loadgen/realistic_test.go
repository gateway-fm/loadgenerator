package main

import (
	"math"
	"math/big"
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// TestSelectRandomTxType_Distribution verifies the distribution matches configured ratios.
func TestSelectRandomTxType_Distribution(t *testing.T) {
	ratios := types.TxTypeRatio{
		EthTransfer:   40,
		ERC20Transfer: 30,
		ERC20Approve:  10,
		UniswapSwap:   10,
		StorageWrite:  5,
		HeavyCompute:  5,
	}

	rnd := account.NewRand()
	counts := make(map[types.TransactionType]int)
	samples := 10000

	for i := 0; i < samples; i++ {
		txType := selectRandomTxType(ratios, rnd)
		counts[txType]++
	}

	// Check each type is within 3% of expected (generous tolerance for randomness)
	tolerance := 0.03
	testCases := []struct {
		txType   types.TransactionType
		expected float64
	}{
		{types.TxTypeEthTransfer, 0.40},
		{types.TxTypeERC20Transfer, 0.30},
		{types.TxTypeERC20Approve, 0.10},
		{types.TxTypeUniswapSwap, 0.10},
		{types.TxTypeStorageWrite, 0.05},
		{types.TxTypeHeavyCompute, 0.05},
	}

	for _, tc := range testCases {
		actual := float64(counts[tc.txType]) / float64(samples)
		if math.Abs(actual-tc.expected) > tolerance {
			t.Errorf("%s: expected %.2f%%, got %.2f%% (%d/%d)",
				tc.txType, tc.expected*100, actual*100, counts[tc.txType], samples)
		}
	}
}

// TestSelectRandomTxType_SingleType100Percent verifies 100% allocation to one type.
func TestSelectRandomTxType_SingleType100Percent(t *testing.T) {
	ratios := types.TxTypeRatio{
		EthTransfer:   100,
		ERC20Transfer: 0,
		ERC20Approve:  0,
		UniswapSwap:   0,
		StorageWrite:  0,
		HeavyCompute:  0,
	}

	rnd := account.NewRand()
	samples := 1000

	for i := 0; i < samples; i++ {
		txType := selectRandomTxType(ratios, rnd)
		if txType != types.TxTypeEthTransfer {
			t.Errorf("expected TxTypeEthTransfer, got %s", txType)
		}
	}
}

// TestSelectRandomTxType_FallbackToHeavyCompute verifies fallback behavior.
func TestSelectRandomTxType_FallbackToHeavyCompute(t *testing.T) {
	// With ratios summing to 0, all rolls should fall through to HeavyCompute
	ratios := types.TxTypeRatio{
		EthTransfer:   0,
		ERC20Transfer: 0,
		ERC20Approve:  0,
		UniswapSwap:   0,
		StorageWrite:  0,
		HeavyCompute:  0, // Even with 0, it's the fallback
	}

	rnd := account.NewRand()
	samples := 100

	for i := 0; i < samples; i++ {
		txType := selectRandomTxType(ratios, rnd)
		if txType != types.TxTypeHeavyCompute {
			t.Errorf("expected TxTypeHeavyCompute as fallback, got %s", txType)
		}
	}
}

// TestGenerateRandomTip_Exponential verifies exponential distribution skews toward lower values.
func TestGenerateRandomTip_Exponential(t *testing.T) {
	cfg := &types.RealisticTestConfig{
		MinTipGwei:      1.0,
		MaxTipGwei:      10.0,
		TipDistribution: types.TipDistExponential,
	}

	rnd := account.NewRand()
	samples := 10000
	var sum float64
	belowMidpoint := 0
	midpoint := (cfg.MaxTipGwei + cfg.MinTipGwei) / 2

	for i := 0; i < samples; i++ {
		tipWei := generateRandomTip(cfg, rnd)
		tipGwei := float64(tipWei.Int64()) / 1e9
		sum += tipGwei

		// Verify bounds
		if tipGwei < cfg.MinTipGwei || tipGwei > cfg.MaxTipGwei {
			t.Errorf("tip %.4f gwei out of bounds [%.1f, %.1f]", tipGwei, cfg.MinTipGwei, cfg.MaxTipGwei)
		}

		if tipGwei < midpoint {
			belowMidpoint++
		}
	}

	// Exponential should have most values below midpoint (>70%)
	belowRatio := float64(belowMidpoint) / float64(samples)
	if belowRatio < 0.60 { // Generous tolerance
		t.Errorf("exponential distribution: expected >60%% below midpoint, got %.1f%%", belowRatio*100)
	}

	// Average should be closer to min than max
	avg := sum / float64(samples)
	if avg > midpoint {
		t.Errorf("exponential avg %.2f should be below midpoint %.2f", avg, midpoint)
	}
}

// TestGenerateRandomTip_PowerLaw verifies power-law distribution.
func TestGenerateRandomTip_PowerLaw(t *testing.T) {
	cfg := &types.RealisticTestConfig{
		MinTipGwei:      1.0,
		MaxTipGwei:      10.0,
		TipDistribution: types.TipDistPowerLaw,
	}

	rnd := account.NewRand()
	samples := 10000
	var sum float64
	belowMidpoint := 0
	midpoint := (cfg.MaxTipGwei + cfg.MinTipGwei) / 2

	for i := 0; i < samples; i++ {
		tipWei := generateRandomTip(cfg, rnd)
		tipGwei := float64(tipWei.Int64()) / 1e9
		sum += tipGwei

		// Verify bounds
		if tipGwei < cfg.MinTipGwei || tipGwei > cfg.MaxTipGwei {
			t.Errorf("tip %.4f gwei out of bounds [%.1f, %.1f]", tipGwei, cfg.MinTipGwei, cfg.MaxTipGwei)
		}

		if tipGwei < midpoint {
			belowMidpoint++
		}
	}

	// Power-law should also skew toward lower values (>55%)
	belowRatio := float64(belowMidpoint) / float64(samples)
	if belowRatio < 0.50 { // Generous tolerance
		t.Errorf("power-law distribution: expected >50%% below midpoint, got %.1f%%", belowRatio*100)
	}
}

// TestGenerateRandomTip_Uniform verifies uniform distribution covers the range evenly.
func TestGenerateRandomTip_Uniform(t *testing.T) {
	cfg := &types.RealisticTestConfig{
		MinTipGwei:      1.0,
		MaxTipGwei:      10.0,
		TipDistribution: types.TipDistUniform,
	}

	rnd := account.NewRand()
	samples := 10000
	var sum float64

	for i := 0; i < samples; i++ {
		tipWei := generateRandomTip(cfg, rnd)
		tipGwei := float64(tipWei.Int64()) / 1e9
		sum += tipGwei

		// Verify bounds
		if tipGwei < cfg.MinTipGwei || tipGwei > cfg.MaxTipGwei {
			t.Errorf("tip %.4f gwei out of bounds [%.1f, %.1f]", tipGwei, cfg.MinTipGwei, cfg.MaxTipGwei)
		}
	}

	// Uniform distribution average should be close to midpoint
	expectedAvg := (cfg.MaxTipGwei + cfg.MinTipGwei) / 2
	actualAvg := sum / float64(samples)
	tolerance := 0.3 // 0.3 gwei tolerance

	if math.Abs(actualAvg-expectedAvg) > tolerance {
		t.Errorf("uniform avg %.2f should be close to midpoint %.2f", actualAvg, expectedAvg)
	}
}

// TestGenerateRandomTip_EdgeCases verifies edge case handling.
func TestGenerateRandomTip_EdgeCases(t *testing.T) {
	rnd := account.NewRand()

	t.Run("nil config", func(t *testing.T) {
		tip := generateRandomTip(nil, rnd)
		if tip.Cmp(big.NewInt(0)) != 0 {
			t.Errorf("expected 0 for nil config, got %s", tip.String())
		}
	})

	t.Run("min equals max", func(t *testing.T) {
		cfg := &types.RealisticTestConfig{
			MinTipGwei:      5.0,
			MaxTipGwei:      5.0, // Will be adjusted to min + 1
			TipDistribution: types.TipDistUniform,
		}

		for i := 0; i < 100; i++ {
			tip := generateRandomTip(cfg, rnd)
			tipGwei := float64(tip.Int64()) / 1e9
			// Should be in range [5, 6] due to adjustment
			if tipGwei < 5.0 || tipGwei > 6.0 {
				t.Errorf("tip %.4f out of adjusted range [5, 6]", tipGwei)
			}
		}
	})

	t.Run("max less than min", func(t *testing.T) {
		cfg := &types.RealisticTestConfig{
			MinTipGwei:      10.0,
			MaxTipGwei:      5.0, // Will be adjusted to min + 1 = 11
			TipDistribution: types.TipDistUniform,
		}

		for i := 0; i < 100; i++ {
			tip := generateRandomTip(cfg, rnd)
			tipGwei := float64(tip.Int64()) / 1e9
			// Should be in range [10, 11] due to adjustment
			if tipGwei < 10.0 || tipGwei > 11.0 {
				t.Errorf("tip %.4f out of adjusted range [10, 11]", tipGwei)
			}
		}
	})
}

// TestValidateTxTypeRatios verifies ratio validation.
func TestValidateTxTypeRatios(t *testing.T) {
	t.Run("valid ratios summing to 100", func(t *testing.T) {
		ratios := types.TxTypeRatio{
			EthTransfer:   40,
			ERC20Transfer: 30,
			ERC20Approve:  10,
			UniswapSwap:   10,
			StorageWrite:  5,
			HeavyCompute:  5,
		}
		if err := validateTxTypeRatios(ratios); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("all zeros (sum != 100)", func(t *testing.T) {
		ratios := types.TxTypeRatio{}
		err := validateTxTypeRatios(ratios)
		if err == nil {
			t.Error("expected error for sum != 100")
		}
	})

	t.Run("sum less than 100", func(t *testing.T) {
		ratios := types.TxTypeRatio{
			EthTransfer:   40,
			ERC20Transfer: 30,
			// Sum = 70
		}
		err := validateTxTypeRatios(ratios)
		if err == nil {
			t.Error("expected error for sum < 100")
		}
	})

	t.Run("sum greater than 100", func(t *testing.T) {
		ratios := types.TxTypeRatio{
			EthTransfer:   60,
			ERC20Transfer: 60,
			// Sum = 120
		}
		err := validateTxTypeRatios(ratios)
		if err == nil {
			t.Error("expected error for sum > 100")
		}
	})

	t.Run("negative value", func(t *testing.T) {
		ratios := types.TxTypeRatio{
			EthTransfer:   110,
			ERC20Transfer: -10,
			// Sum = 100 but has negative
		}
		err := validateTxTypeRatios(ratios)
		if err == nil {
			t.Error("expected error for negative ratio")
		}
	})

	t.Run("value over 100", func(t *testing.T) {
		ratios := types.TxTypeRatio{
			EthTransfer:   150,
			ERC20Transfer: -50,
			// Sum = 100 but has >100
		}
		err := validateTxTypeRatios(ratios)
		if err == nil {
			t.Error("expected error for ratio > 100")
		}
	})

	t.Run("100% single type", func(t *testing.T) {
		ratios := types.TxTypeRatio{
			EthTransfer: 100,
		}
		if err := validateTxTypeRatios(ratios); err != nil {
			t.Errorf("unexpected error for 100%% single type: %v", err)
		}
	})
}
