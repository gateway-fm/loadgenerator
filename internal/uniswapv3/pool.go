package uniswapv3

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// POOL_INIT_CODE_HASH is the keccak256 of the UniswapV3Pool bytecode.
// This is used by the factory to compute pool addresses via CREATE2.
// From: https://github.com/Uniswap/v3-core/blob/main/contracts/UniswapV3Factory.sol
var POOL_INIT_CODE_HASH = common.HexToHash("0xe34f199b19b2b4f47f68442619d555527d244f78a3297ea89325f843f87b8b54")

// ComputePoolAddress computes the CREATE2 address for a Uniswap V3 pool.
// The pool address is deterministic based on factory address, token pair, and fee.
func ComputePoolAddress(factory common.Address, tokenA, tokenB common.Address, fee uint32) common.Address {
	// Sort tokens
	token0, token1 := SortTokens(tokenA, tokenB)

	// Compute the salt: keccak256(abi.encode(token0, token1, fee))
	salt := crypto.Keccak256Hash(
		common.LeftPadBytes(token0.Bytes(), 32),
		common.LeftPadBytes(token1.Bytes(), 32),
		common.LeftPadBytes(big.NewInt(int64(fee)).Bytes(), 32),
	)

	// CREATE2 address: keccak256(0xff ++ factory ++ salt ++ initCodeHash)[12:]
	data := make([]byte, 1+20+32+32)
	data[0] = 0xff
	copy(data[1:21], factory.Bytes())
	copy(data[21:53], salt.Bytes())
	copy(data[53:85], POOL_INIT_CODE_HASH.Bytes())

	hash := crypto.Keccak256Hash(data)
	return common.BytesToAddress(hash[12:])
}

// ComputeContractAddress computes the CREATE address for a contract deployment.
// Address = keccak256(rlp([sender, nonce]))[12:]
func ComputeContractAddress(sender common.Address, nonce uint64) common.Address {
	data, _ := rlp.EncodeToBytes([]interface{}{sender, nonce})
	hash := crypto.Keccak256Hash(data)
	return common.BytesToAddress(hash[12:])
}
