package uniswapv3

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Test addresses
var (
	testAddress1 = common.HexToAddress("0x1111111111111111111111111111111111111111")
	testAddress2 = common.HexToAddress("0x2222222222222222222222222222222222222222")
	testAddress3 = common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
)

func TestSelector(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		expected []byte
	}{
		{
			name:     "deposit()",
			sig:      "deposit()",
			expected: crypto.Keccak256([]byte("deposit()"))[:4],
		},
		{
			name:     "approve(address,uint256)",
			sig:      "approve(address,uint256)",
			expected: crypto.Keccak256([]byte("approve(address,uint256)"))[:4],
		},
		{
			name:     "transfer(address,uint256)",
			sig:      "transfer(address,uint256)",
			expected: crypto.Keccak256([]byte("transfer(address,uint256)"))[:4],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selector(tt.sig)
			if !bytes.Equal(got, tt.expected) {
				t.Errorf("selector(%q) = %x, want %x", tt.sig, got, tt.expected)
			}
			if len(got) != 4 {
				t.Errorf("selector length = %d, want 4", len(got))
			}
		})
	}
}

func TestSortTokens(t *testing.T) {
	tests := []struct {
		name        string
		tokenA      common.Address
		tokenB      common.Address
		wantToken0  common.Address
		wantToken1  common.Address
	}{
		{
			name:       "already sorted",
			tokenA:     testAddress1,
			tokenB:     testAddress2,
			wantToken0: testAddress1,
			wantToken1: testAddress2,
		},
		{
			name:       "needs sorting",
			tokenA:     testAddress2,
			tokenB:     testAddress1,
			wantToken0: testAddress1,
			wantToken1: testAddress2,
		},
		{
			name:       "same address",
			tokenA:     testAddress1,
			tokenB:     testAddress1,
			wantToken0: testAddress1,
			wantToken1: testAddress1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotToken0, gotToken1 := SortTokens(tt.tokenA, tt.tokenB)
			if gotToken0 != tt.wantToken0 {
				t.Errorf("SortTokens() token0 = %s, want %s", gotToken0.Hex(), tt.wantToken0.Hex())
			}
			if gotToken1 != tt.wantToken1 {
				t.Errorf("SortTokens() token1 = %s, want %s", gotToken1.Hex(), tt.wantToken1.Hex())
			}
		})
	}
}

func TestComputeContractAddress(t *testing.T) {
	tests := []struct {
		name   string
		sender common.Address
		nonce  uint64
	}{
		{
			name:   "nonce 0",
			sender: testAddress1,
			nonce:  0,
		},
		{
			name:   "nonce 1",
			sender: testAddress1,
			nonce:  1,
		},
		{
			name:   "nonce 100",
			sender: testAddress2,
			nonce:  100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := ComputeContractAddress(tt.sender, tt.nonce)
			// Address should be 20 bytes
			if len(addr) != 20 {
				t.Errorf("ComputeContractAddress length = %d, want 20", len(addr))
			}
			// Same inputs should produce same output
			addr2 := ComputeContractAddress(tt.sender, tt.nonce)
			if addr != addr2 {
				t.Error("ComputeContractAddress is not deterministic")
			}
			// Different nonce should produce different address
			addrDiff := ComputeContractAddress(tt.sender, tt.nonce+1)
			if addr == addrDiff {
				t.Error("Different nonces should produce different addresses")
			}
		})
	}
}

func TestComputePoolAddress(t *testing.T) {
	factory := common.HexToAddress("0x1234567890123456789012345678901234567890")

	tests := []struct {
		name   string
		tokenA common.Address
		tokenB common.Address
		fee    uint32
	}{
		{
			name:   "standard pool",
			tokenA: testAddress1,
			tokenB: testAddress2,
			fee:    3000,
		},
		{
			name:   "reversed tokens",
			tokenA: testAddress2,
			tokenB: testAddress1,
			fee:    3000,
		},
		{
			name:   "different fee",
			tokenA: testAddress1,
			tokenB: testAddress2,
			fee:    500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := ComputePoolAddress(factory, tt.tokenA, tt.tokenB, tt.fee)
			// Address should be 20 bytes
			if len(addr) != 20 {
				t.Errorf("ComputePoolAddress length = %d, want 20", len(addr))
			}
			// Same inputs should produce same output
			addr2 := ComputePoolAddress(factory, tt.tokenA, tt.tokenB, tt.fee)
			if addr != addr2 {
				t.Error("ComputePoolAddress is not deterministic")
			}
		})
	}

	// Token order shouldn't matter (should get same pool address)
	addr1 := ComputePoolAddress(factory, testAddress1, testAddress2, 3000)
	addr2 := ComputePoolAddress(factory, testAddress2, testAddress1, 3000)
	if addr1 != addr2 {
		t.Error("ComputePoolAddress should be independent of token order")
	}

	// Different fee should produce different address
	addr3 := ComputePoolAddress(factory, testAddress1, testAddress2, 500)
	if addr1 == addr3 {
		t.Error("Different fees should produce different addresses")
	}
}

func TestEncodeDeposit(t *testing.T) {
	data := EncodeDeposit()
	if len(data) != 4 {
		t.Errorf("EncodeDeposit length = %d, want 4", len(data))
	}
	if !bytes.Equal(data, SelectorDeposit) {
		t.Errorf("EncodeDeposit = %x, want %x", data, SelectorDeposit)
	}
}

func TestEncodeWithdraw(t *testing.T) {
	amount := big.NewInt(1000)
	data := EncodeWithdraw(amount)

	if len(data) != 36 {
		t.Errorf("EncodeWithdraw length = %d, want 36", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorWithdraw) {
		t.Errorf("EncodeWithdraw selector = %x, want %x", data[:4], SelectorWithdraw)
	}
}

func TestEncodeApprove(t *testing.T) {
	spender := testAddress1
	amount := big.NewInt(1000000)
	data := EncodeApprove(spender, amount)

	if len(data) != 68 {
		t.Errorf("EncodeApprove length = %d, want 68", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorApprove) {
		t.Errorf("EncodeApprove selector = %x, want %x", data[:4], SelectorApprove)
	}
	// Check spender address is in the right place (after 12 padding bytes)
	if !bytes.Equal(data[16:36], spender.Bytes()) {
		t.Errorf("EncodeApprove spender not encoded correctly")
	}
}

func TestEncodeTransfer(t *testing.T) {
	to := testAddress2
	amount := big.NewInt(500000)
	data := EncodeTransfer(to, amount)

	if len(data) != 68 {
		t.Errorf("EncodeTransfer length = %d, want 68", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorTransfer) {
		t.Errorf("EncodeTransfer selector = %x, want %x", data[:4], SelectorTransfer)
	}
}

func TestEncodeMint(t *testing.T) {
	to := testAddress1
	amount := new(big.Int).Mul(big.NewInt(1000000), big.NewInt(1e6)) // 1M USDC
	data := EncodeMint(to, amount)

	if len(data) != 68 {
		t.Errorf("EncodeMint length = %d, want 68", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorMint) {
		t.Errorf("EncodeMint selector = %x, want %x", data[:4], SelectorMint)
	}
}

func TestEncodeBurn(t *testing.T) {
	from := testAddress2
	amount := big.NewInt(100000)
	data := EncodeBurn(from, amount)

	if len(data) != 68 {
		t.Errorf("EncodeBurn length = %d, want 68", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorBurn) {
		t.Errorf("EncodeBurn selector = %x, want %x", data[:4], SelectorBurn)
	}
}

func TestEncodeBalanceOf(t *testing.T) {
	account := testAddress1
	data := EncodeBalanceOf(account)

	if len(data) != 36 {
		t.Errorf("EncodeBalanceOf length = %d, want 36", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorBalanceOf) {
		t.Errorf("EncodeBalanceOf selector = %x, want %x", data[:4], SelectorBalanceOf)
	}
}

func TestEncodeAllowance(t *testing.T) {
	owner := testAddress1
	spender := testAddress2
	data := EncodeAllowance(owner, spender)

	if len(data) != 68 {
		t.Errorf("EncodeAllowance length = %d, want 68", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorAllowance) {
		t.Errorf("EncodeAllowance selector = %x, want %x", data[:4], SelectorAllowance)
	}
}

func TestEncodeCreatePool(t *testing.T) {
	tokenA := testAddress1
	tokenB := testAddress2
	fee := uint32(3000)
	data := EncodeCreatePool(tokenA, tokenB, fee)

	if len(data) != 100 {
		t.Errorf("EncodeCreatePool length = %d, want 100", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorCreatePool) {
		t.Errorf("EncodeCreatePool selector = %x, want %x", data[:4], SelectorCreatePool)
	}
}

func TestEncodeGetPool(t *testing.T) {
	tokenA := testAddress1
	tokenB := testAddress2
	fee := uint32(3000)
	data := EncodeGetPool(tokenA, tokenB, fee)

	if len(data) != 100 {
		t.Errorf("EncodeGetPool length = %d, want 100", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorGetPool) {
		t.Errorf("EncodeGetPool selector = %x, want %x", data[:4], SelectorGetPool)
	}
}

func TestEncodeInitialize(t *testing.T) {
	sqrtPriceX96, _ := new(big.Int).SetString("79228162514264337593543950336", 10)
	data := EncodeInitialize(sqrtPriceX96)

	if len(data) != 36 {
		t.Errorf("EncodeInitialize length = %d, want 36", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorInitialize) {
		t.Errorf("EncodeInitialize selector = %x, want %x", data[:4], SelectorInitialize)
	}
}

func TestEncodeExactInputSingle(t *testing.T) {
	params := ExactInputSingleParams{
		TokenIn:           testAddress1,
		TokenOut:          testAddress2,
		Fee:               3000,
		Recipient:         testAddress3,
		Deadline:          big.NewInt(9999999999),
		AmountIn:          big.NewInt(1e18),
		AmountOutMinimum:  big.NewInt(0),
		SqrtPriceLimitX96: big.NewInt(0),
	}

	data := EncodeExactInputSingle(params)

	// 4 (selector) + 8*32 (8 fields) = 260 bytes
	if len(data) != 260 {
		t.Errorf("EncodeExactInputSingle length = %d, want 260", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorExactInputSingle) {
		t.Errorf("EncodeExactInputSingle selector = %x, want %x", data[:4], SelectorExactInputSingle)
	}
}

func TestEncodeExactInputSingle_NilSqrtPriceLimit(t *testing.T) {
	params := ExactInputSingleParams{
		TokenIn:           testAddress1,
		TokenOut:          testAddress2,
		Fee:               3000,
		Recipient:         testAddress3,
		Deadline:          big.NewInt(9999999999),
		AmountIn:          big.NewInt(1e18),
		AmountOutMinimum:  big.NewInt(0),
		SqrtPriceLimitX96: nil, // Test nil case
	}

	// Should not panic
	data := EncodeExactInputSingle(params)
	if len(data) != 260 {
		t.Errorf("EncodeExactInputSingle with nil SqrtPriceLimitX96 length = %d, want 260", len(data))
	}
}

func TestEncodeMintPosition(t *testing.T) {
	params := MintParams{
		Token0:         testAddress1,
		Token1:         testAddress2,
		Fee:            3000,
		TickLower:      -887220,
		TickUpper:      887220,
		Amount0Desired: big.NewInt(1e18),
		Amount1Desired: big.NewInt(2000e6),
		Amount0Min:     big.NewInt(0),
		Amount1Min:     big.NewInt(0),
		Recipient:      testAddress3,
		Deadline:       big.NewInt(9999999999),
	}

	data := EncodeMintPosition(params)

	// 4 (selector) + 11*32 (11 fields) = 356 bytes
	if len(data) != 356 {
		t.Errorf("EncodeMintPosition length = %d, want 356", len(data))
	}
	// Check selector
	if !bytes.Equal(data[:4], SelectorMintPosition) {
		t.Errorf("EncodeMintPosition selector = %x, want %x", data[:4], SelectorMintPosition)
	}
}

func TestEncodeMintPosition_PositiveTicks(t *testing.T) {
	params := MintParams{
		Token0:         testAddress1,
		Token1:         testAddress2,
		Fee:            3000,
		TickLower:      100, // Positive ticks
		TickUpper:      200,
		Amount0Desired: big.NewInt(1e18),
		Amount1Desired: big.NewInt(2000e6),
		Amount0Min:     big.NewInt(0),
		Amount1Min:     big.NewInt(0),
		Recipient:      testAddress3,
		Deadline:       big.NewInt(9999999999),
	}

	data := EncodeMintPosition(params)
	if len(data) != 356 {
		t.Errorf("EncodeMintPosition with positive ticks length = %d, want 356", len(data))
	}
}

func TestDefaultPoolConfig(t *testing.T) {
	weth := testAddress1
	usdc := testAddress2

	config := DefaultPoolConfig(weth, usdc)

	if config.Token0 != weth {
		t.Errorf("Token0 = %s, want %s", config.Token0.Hex(), weth.Hex())
	}
	if config.Token1 != usdc {
		t.Errorf("Token1 = %s, want %s", config.Token1.Hex(), usdc.Hex())
	}
	if config.Fee != 3000 {
		t.Errorf("Fee = %d, want 3000", config.Fee)
	}
	if config.TickLower != -887220 {
		t.Errorf("TickLower = %d, want -887220", config.TickLower)
	}
	if config.TickUpper != 887220 {
		t.Errorf("TickUpper = %d, want 887220", config.TickUpper)
	}
	if config.SqrtPriceX96 == nil {
		t.Error("SqrtPriceX96 should not be nil")
	}
	if config.LiquidityWETH == nil {
		t.Error("LiquidityWETH should not be nil")
	}
	if config.LiquidityUSDC == nil {
		t.Error("LiquidityUSDC should not be nil")
	}

	// Check liquidity values
	expectedWETH := new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18))
	if config.LiquidityWETH.Cmp(expectedWETH) != 0 {
		t.Errorf("LiquidityWETH = %v, want %v", config.LiquidityWETH, expectedWETH)
	}
	expectedUSDC := new(big.Int).Mul(big.NewInt(2000000), big.NewInt(1e6))
	if config.LiquidityUSDC.Cmp(expectedUSDC) != 0 {
		t.Errorf("LiquidityUSDC = %v, want %v", config.LiquidityUSDC, expectedUSDC)
	}
}

func TestMaxUint256(t *testing.T) {
	// MaxUint256 should be 2^256 - 1
	expected := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if MaxUint256.Cmp(expected) != 0 {
		t.Errorf("MaxUint256 = %v, want %v", MaxUint256, expected)
	}

	// Should have 256 bits
	if MaxUint256.BitLen() != 256 {
		t.Errorf("MaxUint256 bit length = %d, want 256", MaxUint256.BitLen())
	}
}

func TestSelectors(t *testing.T) {
	// Verify all selectors are 4 bytes
	selectors := []struct {
		name     string
		selector []byte
	}{
		{"SelectorDeposit", SelectorDeposit},
		{"SelectorWithdraw", SelectorWithdraw},
		{"SelectorApprove", SelectorApprove},
		{"SelectorTransfer", SelectorTransfer},
		{"SelectorBalanceOf", SelectorBalanceOf},
		{"SelectorMint", SelectorMint},
		{"SelectorBurn", SelectorBurn},
		{"SelectorAllowance", SelectorAllowance},
		{"SelectorCreatePool", SelectorCreatePool},
		{"SelectorGetPool", SelectorGetPool},
		{"SelectorInitialize", SelectorInitialize},
		{"SelectorExactInputSingle", SelectorExactInputSingle},
		{"SelectorMintPosition", SelectorMintPosition},
	}

	for _, s := range selectors {
		t.Run(s.name, func(t *testing.T) {
			if len(s.selector) != 4 {
				t.Errorf("%s length = %d, want 4", s.name, len(s.selector))
			}
		})
	}
}

func TestPoolInitCodeHash(t *testing.T) {
	// POOL_INIT_CODE_HASH should be 32 bytes
	if len(POOL_INIT_CODE_HASH) != 32 {
		t.Errorf("POOL_INIT_CODE_HASH length = %d, want 32", len(POOL_INIT_CODE_HASH))
	}

	// Should match the known Uniswap V3 pool init code hash
	expected := common.HexToHash("0xe34f199b19b2b4f47f68442619d555527d244f78a3297ea89325f843f87b8b54")
	if POOL_INIT_CODE_HASH != expected {
		t.Errorf("POOL_INIT_CODE_HASH = %s, want %s", POOL_INIT_CODE_HASH.Hex(), expected.Hex())
	}
}
