package txbuilder

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// NewTransferTx creates either a DynamicFeeTx or LegacyTx depending on useLegacy.
// For legacy transactions, gasFeeCap is used as the gas price.
func NewTransferTx(chainID *big.Int, nonce uint64, to common.Address, value *big.Int, gasLimit uint64, gasTipCap *big.Int, gasFeeCap *big.Int, data []byte, useLegacy bool) *types.Transaction {
	if useLegacy {
		return types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: gasFeeCap,
			Gas:      gasLimit,
			To:       &to,
			Value:    value,
			Data:     data,
		})
	}
	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        &to,
		Value:     value,
		Data:      data,
	})
}

// NewContractTx creates either a DynamicFeeTx or LegacyTx for contract deployment (nil To).
// For legacy transactions, gasFeeCap is used as the gas price.
func NewContractTx(chainID *big.Int, nonce uint64, value *big.Int, gasLimit uint64, gasTipCap *big.Int, gasFeeCap *big.Int, data []byte, useLegacy bool) *types.Transaction {
	if useLegacy {
		return types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: gasFeeCap,
			Gas:      gasLimit,
			To:       nil,
			Value:    value,
			Data:     data,
		})
	}
	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        nil,
		Value:     value,
		Data:      data,
	})
}
