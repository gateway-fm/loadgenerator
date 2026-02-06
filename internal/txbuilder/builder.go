// Package txbuilder provides transaction building for different transaction types.
package txbuilder

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// TxParams holds parameters for building a transaction.
type TxParams struct {
	ChainID   *big.Int
	Nonce     uint64
	GasTipCap *big.Int
	GasFeeCap *big.Int
	From      common.Address // Sender address (needed for Uniswap swaps)
}

// Builder builds transactions for a specific type.
type Builder interface {
	// Type returns the transaction type identifier.
	Type() ptypes.TransactionType

	// GasLimit returns the gas limit for this tx type.
	GasLimit() uint64

	// Build creates a transaction.
	Build(params TxParams) (*types.Transaction, error)

	// RequiresContract returns true if a contract must be deployed.
	RequiresContract() bool

	// ContractBytecode returns the bytecode to deploy, if any.
	ContractBytecode() []byte

	// SetContractAddress sets the deployed contract address.
	SetContractAddress(addr common.Address)
}

// ComplexBuilder extends Builder with additional setup methods for complex
// transaction types like Uniswap V3 swaps that require multiple contracts.
type ComplexBuilder interface {
	Builder

	// IsComplex returns true if this builder requires complex setup.
	IsComplex() bool

	// DeployContracts deploys all required contracts.
	DeployContracts(ctx context.Context, client interface{}, deployer interface{}, chainID, gasPrice *big.Int, logger interface{}) error

	// SetupAccounts prepares accounts for transactions (approvals, minting, etc).
	SetupAccounts(ctx context.Context, accounts []interface{}, client interface{}, chainID, gasPrice *big.Int) error
}

// Registry manages builder lookup by type.
type Registry struct {
	builders map[ptypes.TransactionType]Builder
}

// NewRegistry creates a new builder registry.
func NewRegistry() *Registry {
	return &Registry{
		builders: make(map[ptypes.TransactionType]Builder),
	}
}

// Register adds a builder to the registry.
func (r *Registry) Register(builder Builder) {
	r.builders[builder.Type()] = builder
}

// Get returns a builder for the given type.
func (r *Registry) Get(txType ptypes.TransactionType) (Builder, error) {
	builder, ok := r.builders[txType]
	if !ok {
		return nil, fmt.Errorf("unknown transaction type: %s", txType)
	}
	return builder, nil
}

// GetAll returns all registered builders.
func (r *Registry) GetAll() []Builder {
	builders := make([]Builder, 0, len(r.builders))
	for _, b := range r.builders {
		builders = append(builders, b)
	}
	return builders
}

// GetComplexBuilder returns a ComplexBuilder if the given type implements it.
func (r *Registry) GetComplexBuilder(txType ptypes.TransactionType) (ComplexBuilder, bool) {
	builder, ok := r.builders[txType]
	if !ok {
		return nil, false
	}
	cb, ok := builder.(ComplexBuilder)
	return cb, ok
}

// NewDefaultRegistry creates a registry with all standard builders.
// Uses real Uniswap V3 for swap transactions.
func NewDefaultRegistry(recipient common.Address) *Registry {
	r := NewRegistry()

	r.Register(NewETHTransferBuilder(recipient))
	r.Register(NewERC20TransferBuilder(recipient))
	r.Register(NewERC20ApproveBuilder(recipient))
	r.Register(NewUniswapV3SwapBuilder()) // Real Uniswap V3 swaps
	r.Register(NewStorageWriteBuilder())
	r.Register(NewHeavyComputeBuilder())

	return r
}
