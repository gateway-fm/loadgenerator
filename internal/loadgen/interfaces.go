package loadgen

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/contract"
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
}