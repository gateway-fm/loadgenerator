package txbuilder

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// Heavy compute function selector: consumeGas(uint256) = 0xa329e8de
var heavyComputeSelector = common.FromHex("0xa329e8de")

// GasConsumer contract bytecode - supports store(uint256), consumeGas(uint256), counter(), values(uint256)
// Compiled from Solidity with correct function signatures
var GasConsumerBytecode = common.FromHex("0x608060405234801561000f575f80fd5b506101db8061001d5f395ff3fe608060405234801561000f575f80fd5b506004361061004a575f3560e01c80635e383d211461004e5780636057361d1461007f57806361bc221a14610094578063a329e8de1461009c575b5f80fd5b61006d61005c36600461016a565b60016020525f908152604090205481565b60405190815260200160405180910390f35b61009261008d36600461016a565b6100af565b005b61006d5f5481565b6100926100aa36600461016a565b6100d5565b5f80548152600160205260408120829055805490806100cd83610181565b919050555050565b5f816040516020016100e991815260200190565b6040516020818303038152906040528051906020012090505f5b8281101561014257604080516020810184905201604051602081830303815290604052805190602001209150808061013a90610181565b915050610103565b505f805481526001602052604081208290558054908061016183610181565b91905055505050565b5f6020828403121561017a575f80fd5b5035919050565b5f6001820161019e57634e487b7160e01b5f52601160045260245ffd5b506001019056fea26469706673582212206182d890991e9bbd7a6af9c355812723ee8e626b7af95fbebe78a89baa5632e464736f6c63430008140033")

// encodeHeavyCompute encodes a consumeGas(uint256) call.
func encodeHeavyCompute(iterations *big.Int) []byte {
	data := make([]byte, 4+32)
	copy(data[0:4], heavyComputeSelector)
	iterations.FillBytes(data[4:36])
	return data
}

// HeavyComputeBuilder builds heavy compute transactions.
type HeavyComputeBuilder struct {
	contractAddress common.Address
	iterations      int64
}

// NewHeavyComputeBuilder creates a new heavy compute builder.
func NewHeavyComputeBuilder() *HeavyComputeBuilder {
	return &HeavyComputeBuilder{
		iterations: 1000, // Default: 1000 iterations of keccak256
	}
}

// Type returns the transaction type identifier.
func (b *HeavyComputeBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeHeavyCompute
}

// GasLimit returns the gas limit for heavy compute (~500000).
func (b *HeavyComputeBuilder) GasLimit() uint64 {
	return 500000
}

// Build creates a heavy compute transaction.
func (b *HeavyComputeBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}
	data := encodeHeavyCompute(big.NewInt(b.iterations))

	return NewTransferTx(params.ChainID, params.Nonce, b.contractAddress, big.NewInt(0), b.GasLimit(), params.GasTipCap, params.GasFeeCap, data, params.UseLegacy), nil
}

// RequiresContract returns true - heavy compute needs a contract.
func (b *HeavyComputeBuilder) RequiresContract() bool {
	return true
}

// ContractBytecode returns the GasConsumer contract bytecode.
func (b *HeavyComputeBuilder) ContractBytecode() []byte {
	return GasConsumerBytecode
}

// SetContractAddress sets the deployed contract address.
func (b *HeavyComputeBuilder) SetContractAddress(addr common.Address) {
	b.contractAddress = addr
}

// SetIterations sets the number of compute iterations.
func (b *HeavyComputeBuilder) SetIterations(iterations int64) {
	if iterations < 0 {
		panic("iterations must be non-negative")
	}
	b.iterations = iterations
}
