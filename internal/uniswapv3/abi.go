package uniswapv3

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Function selectors (first 4 bytes of keccak256(signature))
var (
	// WETH9 selectors
	SelectorDeposit  = selector("deposit()")
	SelectorWithdraw = selector("withdraw(uint256)")

	// ERC20 selectors
	SelectorApprove    = selector("approve(address,uint256)")
	SelectorTransfer   = selector("transfer(address,uint256)")
	SelectorBalanceOf  = selector("balanceOf(address)")
	SelectorMint       = selector("mint(address,uint256)")
	SelectorBurn       = selector("burn(address,uint256)")
	SelectorAllowance  = selector("allowance(address,address)")

	// UniswapV3Factory selectors
	SelectorCreatePool = selector("createPool(address,address,uint24)")
	SelectorGetPool    = selector("getPool(address,address,uint24)")

	// UniswapV3Pool selectors
	SelectorInitialize = selector("initialize(uint160)")
	SelectorSlot0      = selector("slot0()")

	// SwapRouter selectors
	SelectorExactInputSingle = selector("exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))")

	// NonfungiblePositionManager selectors
	SelectorMintPosition = selector("mint((address,address,uint24,int24,int24,uint256,uint256,uint256,uint256,address,uint256))")
)

// selector computes the 4-byte function selector from signature.
func selector(sig string) []byte {
	return crypto.Keccak256([]byte(sig))[:4]
}

// EncodeDeposit encodes WETH9.deposit() call (no args, just send ETH).
func EncodeDeposit() []byte {
	return SelectorDeposit
}

// EncodeWithdraw encodes WETH9.withdraw(uint256) call.
func EncodeWithdraw(amount *big.Int) []byte {
	data := make([]byte, 4+32)
	copy(data[:4], SelectorWithdraw)
	amount.FillBytes(data[4:36])
	return data
}

// EncodeApprove encodes ERC20.approve(address,uint256) call.
func EncodeApprove(spender common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], SelectorApprove)
	copy(data[4+12:36], spender.Bytes())
	amount.FillBytes(data[36:68])
	return data
}

// EncodeTransfer encodes ERC20.transfer(address,uint256) call.
func EncodeTransfer(to common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], SelectorTransfer)
	copy(data[4+12:36], to.Bytes())
	amount.FillBytes(data[36:68])
	return data
}

// EncodeMint encodes MockUSDC.mint(address,uint256) call.
func EncodeMint(to common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], SelectorMint)
	copy(data[4+12:36], to.Bytes())
	amount.FillBytes(data[36:68])
	return data
}

// EncodeBurn encodes MockUSDC.burn(address,uint256) call.
func EncodeBurn(from common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], SelectorBurn)
	copy(data[4+12:36], from.Bytes())
	amount.FillBytes(data[36:68])
	return data
}

// EncodeBalanceOf encodes ERC20.balanceOf(address) call.
func EncodeBalanceOf(account common.Address) []byte {
	data := make([]byte, 4+32)
	copy(data[:4], SelectorBalanceOf)
	copy(data[4+12:36], account.Bytes())
	return data
}

// EncodeAllowance encodes ERC20.allowance(address,address) call.
func EncodeAllowance(owner, spender common.Address) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], SelectorAllowance)
	copy(data[4+12:36], owner.Bytes())
	copy(data[36+12:68], spender.Bytes())
	return data
}

// EncodeCreatePool encodes UniswapV3Factory.createPool(address,address,uint24) call.
func EncodeCreatePool(tokenA, tokenB common.Address, fee uint32) []byte {
	data := make([]byte, 4+32+32+32)
	copy(data[:4], SelectorCreatePool)
	copy(data[4+12:36], tokenA.Bytes())
	copy(data[36+12:68], tokenB.Bytes())
	big.NewInt(int64(fee)).FillBytes(data[68:100])
	return data
}

// EncodeGetPool encodes UniswapV3Factory.getPool(address,address,uint24) call.
func EncodeGetPool(tokenA, tokenB common.Address, fee uint32) []byte {
	data := make([]byte, 4+32+32+32)
	copy(data[:4], SelectorGetPool)
	copy(data[4+12:36], tokenA.Bytes())
	copy(data[36+12:68], tokenB.Bytes())
	big.NewInt(int64(fee)).FillBytes(data[68:100])
	return data
}

// EncodeInitialize encodes UniswapV3Pool.initialize(uint160) call.
func EncodeInitialize(sqrtPriceX96 *big.Int) []byte {
	data := make([]byte, 4+32)
	copy(data[:4], SelectorInitialize)
	sqrtPriceX96.FillBytes(data[4:36])
	return data
}

// EncodeExactInputSingle encodes SwapRouter.exactInputSingle(...) call.
// The struct is: (tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum, sqrtPriceLimitX96)
// The struct has all static types, so no offset pointer is needed - fields are encoded directly.
func EncodeExactInputSingle(params ExactInputSingleParams) []byte {
	// Total size: 4 (selector) + 8*32 (8 fields) = 260 bytes
	data := make([]byte, 4+8*32)
	copy(data[:4], SelectorExactInputSingle)

	offset := 4
	// tokenIn
	copy(data[offset+12:offset+32], params.TokenIn.Bytes())
	offset += 32
	// tokenOut
	copy(data[offset+12:offset+32], params.TokenOut.Bytes())
	offset += 32
	// fee
	big.NewInt(int64(params.Fee)).FillBytes(data[offset : offset+32])
	offset += 32
	// recipient
	copy(data[offset+12:offset+32], params.Recipient.Bytes())
	offset += 32
	// deadline
	params.Deadline.FillBytes(data[offset : offset+32])
	offset += 32
	// amountIn
	params.AmountIn.FillBytes(data[offset : offset+32])
	offset += 32
	// amountOutMinimum
	params.AmountOutMinimum.FillBytes(data[offset : offset+32])
	offset += 32
	// sqrtPriceLimitX96
	if params.SqrtPriceLimitX96 != nil {
		params.SqrtPriceLimitX96.FillBytes(data[offset : offset+32])
	}

	return data
}

// MintParams holds parameters for NFTPositionManager.mint
type MintParams struct {
	Token0         common.Address
	Token1         common.Address
	Fee            uint32
	TickLower      int32
	TickUpper      int32
	Amount0Desired *big.Int
	Amount1Desired *big.Int
	Amount0Min     *big.Int
	Amount1Min     *big.Int
	Recipient      common.Address
	Deadline       *big.Int
}

// EncodeMintPosition encodes NonfungiblePositionManager.mint(...) call.
// The struct has all static types, so no offset pointer is needed - fields are encoded directly.
func EncodeMintPosition(params MintParams) []byte {
	// Total size: 4 (selector) + 11*32 (11 fields) = 356 bytes
	data := make([]byte, 4+11*32)
	copy(data[:4], SelectorMintPosition)

	offset := 4
	// token0
	copy(data[offset+12:offset+32], params.Token0.Bytes())
	offset += 32
	// token1
	copy(data[offset+12:offset+32], params.Token1.Bytes())
	offset += 32
	// fee
	big.NewInt(int64(params.Fee)).FillBytes(data[offset : offset+32])
	offset += 32
	// tickLower (int24, sign-extended to int256)
	tickLowerBig := big.NewInt(int64(params.TickLower))
	if params.TickLower < 0 {
		// Two's complement for negative numbers in 256-bit representation
		tickLowerBig.Add(tickLowerBig, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	tickLowerBig.FillBytes(data[offset : offset+32])
	offset += 32
	// tickUpper (int24, sign-extended to int256)
	tickUpperBig := big.NewInt(int64(params.TickUpper))
	if params.TickUpper < 0 {
		tickUpperBig.Add(tickUpperBig, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	tickUpperBig.FillBytes(data[offset : offset+32])
	offset += 32
	// amount0Desired
	params.Amount0Desired.FillBytes(data[offset : offset+32])
	offset += 32
	// amount1Desired
	params.Amount1Desired.FillBytes(data[offset : offset+32])
	offset += 32
	// amount0Min
	params.Amount0Min.FillBytes(data[offset : offset+32])
	offset += 32
	// amount1Min
	params.Amount1Min.FillBytes(data[offset : offset+32])
	offset += 32
	// recipient
	copy(data[offset+12:offset+32], params.Recipient.Bytes())
	offset += 32
	// deadline
	params.Deadline.FillBytes(data[offset : offset+32])

	return data
}

// SortTokens returns tokens in the correct order (lower address first).
func SortTokens(tokenA, tokenB common.Address) (common.Address, common.Address) {
	if tokenA.Hex() < tokenB.Hex() {
		return tokenA, tokenB
	}
	return tokenB, tokenA
}
