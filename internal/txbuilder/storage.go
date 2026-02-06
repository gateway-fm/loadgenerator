package txbuilder

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// Storage write function selector: store(uint256)
var storageWriteSelector = common.FromHex("0x6057361d")

// encodeStorageWrite encodes a store(uint256) call.
func encodeStorageWrite(value *big.Int) []byte {
	if value.Sign() < 0 {
		panic("value must be non-negative")
	}
	data := make([]byte, 4+32)
	copy(data[0:4], storageWriteSelector)
	value.FillBytes(data[4:36])
	return data
}

// StorageWriteBuilder builds storage write transactions.
type StorageWriteBuilder struct {
	contractAddress common.Address
}

// NewStorageWriteBuilder creates a new storage write builder.
func NewStorageWriteBuilder() *StorageWriteBuilder {
	return &StorageWriteBuilder{}
}

// Type returns the transaction type identifier.
func (b *StorageWriteBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeStorageWrite
}

// GasLimit returns the gas limit for storage write (~70000).
func (b *StorageWriteBuilder) GasLimit() uint64 {
	return 70000
}

// Build creates a storage write transaction.
func (b *StorageWriteBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}
	// Use nonce as the value to store for variety
	data := encodeStorageWrite(new(big.Int).SetUint64(params.Nonce))

	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   params.ChainID,
		Nonce:     params.Nonce,
		GasTipCap: params.GasTipCap,
		GasFeeCap: params.GasFeeCap,
		Gas:       b.GasLimit(),
		To:        &b.contractAddress,
		Value:     big.NewInt(0),
		Data:      data,
	}), nil
}

// RequiresContract returns true - storage write needs a contract.
func (b *StorageWriteBuilder) RequiresContract() bool {
	return true
}

// ContractBytecode returns nil - uses GasConsumer contract.
func (b *StorageWriteBuilder) ContractBytecode() []byte {
	return nil // Share GasConsumer contract
}

// SetContractAddress sets the deployed contract address.
func (b *StorageWriteBuilder) SetContractAddress(addr common.Address) {
	b.contractAddress = addr
}
