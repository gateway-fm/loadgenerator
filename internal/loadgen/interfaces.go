package loadgen

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/contract"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// TxSender sends transactions asynchronously with backpressure.
type TxSender interface {
	SendBatchAsync(ctx context.Context, txDataSlice [][]byte, callbacks []func(error)) bool
}

// ContractDeployer deploys and validates smart contracts.
type ContractDeployer interface {
	DeployAllWithProgress(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error)
	ValidateCachedContracts(ctx context.Context, cached map[string]string) (valid map[string]common.Address, invalid []string)
	SetUseLegacy(useLegacy bool)
	PreMintNFTs(ctx context.Context, minter *account.Account, nftAddr common.Address, count int, onProgress contract.PreMintProgress) error
}

// AccountManager manages test accounts, funding, and nonces.
type AccountManager interface {
	GetAccounts() []*account.Account
	GetDynamicAccounts() []*account.Account
	SetDynamicAccounts(accounts []*account.Account)
	GenerateDynamicAccounts(count int) error
	GetAccountsFunded() int
	FundAccounts(ctx context.Context, sendClient, syncClient rpc.Client, accounts []*account.Account) error
	FundDynamicAccounts(ctx context.Context, sendClient, syncClient rpc.Client) error
	ValidateBalances(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account)
	InitializeNonces(ctx context.Context, client rpc.Client, numAccounts int) error
	InitializeDynamicNonces(ctx context.Context, client rpc.Client) error
	// InitializeNoncesFromChain resyncs built-in AND dynamic accounts from
	// confirmed on-chain state. Needed after Uniswap account setup, which sends
	// TXs without advancing the Account nonce counters.
	InitializeNoncesFromChain(ctx context.Context, client rpc.Client, numAccounts int) error
	RecycleFunds(ctx context.Context, client rpc.Client) (int, error)
	ExportDynamicAccountKeys() []account.AccountKeyPair
}