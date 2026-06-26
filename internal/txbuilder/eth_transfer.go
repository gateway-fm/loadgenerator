package txbuilder

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// ETHTransferBuilder builds simple ETH transfer transactions.
type ETHTransferBuilder struct {
	recipient common.Address
}

// NewETHTransferBuilder creates a new ETH transfer builder.
func NewETHTransferBuilder(recipient common.Address) *ETHTransferBuilder {
	return &ETHTransferBuilder{
		recipient: recipient,
	}
}

// Type returns the transaction type identifier.
func (b *ETHTransferBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeEthTransfer
}

// GasLimit returns the gas limit for ETH transfer (21000).
func (b *ETHTransferBuilder) GasLimit() uint64 {
	return 21000
}

// Build creates an ETH transfer transaction.
func (b *ETHTransferBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}
	// 1 wei normally (a real value transfer); 0 in gasless mode, where the sender
	// is an unfunded random account with no balance to move.
	value := big.NewInt(1)
	if params.Gasless {
		value = big.NewInt(0)
	}
	return NewTransferTx(params.ChainID, params.Nonce, b.recipient, value, b.GasLimit(), params.GasTipCap, params.GasFeeCap, nil, params.UseLegacy), nil
}

// RequiresContract returns false - ETH transfer doesn't need a contract.
func (b *ETHTransferBuilder) RequiresContract() bool {
	return false
}

// ContractBytecode returns nil - no contract needed.
func (b *ETHTransferBuilder) ContractBytecode() []byte {
	return nil
}

// SetContractAddress is a no-op for ETH transfer.
func (b *ETHTransferBuilder) SetContractAddress(addr common.Address) {}
