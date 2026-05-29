package loadgen

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/contract"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/storage"
)

func makeTestAccount(t *testing.T) *account.Account {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return account.NewAccount(key)
}

// mockRPCClient implements rpc.Client with configurable function fields.
// Only set the functions your test needs; unset methods return zero values.
type mockRPCClient struct {
	CallFn                       func(ctx context.Context, method string, params []interface{}) (json.RawMessage, error)
	BatchCallFn                  func(ctx context.Context, calls []rpc.BatchRequest) ([]rpc.BatchResponse, error)
	SendRawTransactionFn         func(ctx context.Context, txRLP []byte) error
	SendRawTransactionBatchFn    func(ctx context.Context, txRLPs [][]byte) []error
	GetNonceFn                   func(ctx context.Context, address string) (uint64, error)
	GetConfirmedNonceFn          func(ctx context.Context, address string) (uint64, error)
	GetBlockNumberFn             func(ctx context.Context) (uint64, error)
	GetBlockByNumberFn           func(ctx context.Context, blockNum uint64) (*rpc.Block, error)
	GetBlockByNumberFullFn       func(ctx context.Context, blockNum uint64) (*rpc.BlockFull, error)
	GetBlocksByNumberFullBatchFn func(ctx context.Context, blockNums []uint64) ([]*rpc.BlockFull, error)
	GetCodeFn                    func(ctx context.Context, address string) (string, error)
	GetGasPriceFn                func(ctx context.Context) (uint64, error)
	GetBaseFeeFn                 func(ctx context.Context) (uint64, error)
	GetBalanceFn                 func(ctx context.Context, address string) (*big.Int, error)
	GetTransactionReceiptFn      func(ctx context.Context, txHash string) (*rpc.TransactionReceipt, error)
	GetTransactionReceiptsBatchFn func(ctx context.Context, txHashes []string) ([]*rpc.TransactionReceipt, error)
}

func (m *mockRPCClient) Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	if m.CallFn != nil {
		return m.CallFn(ctx, method, params)
	}
	return nil, nil
}
func (m *mockRPCClient) BatchCall(ctx context.Context, calls []rpc.BatchRequest) ([]rpc.BatchResponse, error) {
	if m.BatchCallFn != nil {
		return m.BatchCallFn(ctx, calls)
	}
	return nil, nil
}
func (m *mockRPCClient) SendRawTransaction(ctx context.Context, txRLP []byte) error {
	if m.SendRawTransactionFn != nil {
		return m.SendRawTransactionFn(ctx, txRLP)
	}
	return nil
}
func (m *mockRPCClient) SendRawTransactionBatch(ctx context.Context, txRLPs [][]byte) []error {
	if m.SendRawTransactionBatchFn != nil {
		return m.SendRawTransactionBatchFn(ctx, txRLPs)
	}
	return make([]error, len(txRLPs))
}
func (m *mockRPCClient) GetNonce(ctx context.Context, address string) (uint64, error) {
	if m.GetNonceFn != nil {
		return m.GetNonceFn(ctx, address)
	}
	return 0, nil
}
func (m *mockRPCClient) GetConfirmedNonce(ctx context.Context, address string) (uint64, error) {
	if m.GetConfirmedNonceFn != nil {
		return m.GetConfirmedNonceFn(ctx, address)
	}
	return 0, nil
}
func (m *mockRPCClient) GetBlockNumber(ctx context.Context) (uint64, error) {
	if m.GetBlockNumberFn != nil {
		return m.GetBlockNumberFn(ctx)
	}
	return 0, nil
}
func (m *mockRPCClient) GetBlockByNumber(ctx context.Context, blockNum uint64) (*rpc.Block, error) {
	if m.GetBlockByNumberFn != nil {
		return m.GetBlockByNumberFn(ctx, blockNum)
	}
	return nil, nil
}
func (m *mockRPCClient) GetBlockByNumberFull(ctx context.Context, blockNum uint64) (*rpc.BlockFull, error) {
	if m.GetBlockByNumberFullFn != nil {
		return m.GetBlockByNumberFullFn(ctx, blockNum)
	}
	return nil, nil
}
func (m *mockRPCClient) GetBlocksByNumberFullBatch(ctx context.Context, blockNums []uint64) ([]*rpc.BlockFull, error) {
	if m.GetBlocksByNumberFullBatchFn != nil {
		return m.GetBlocksByNumberFullBatchFn(ctx, blockNums)
	}
	return nil, nil
}
func (m *mockRPCClient) GetCode(ctx context.Context, address string) (string, error) {
	if m.GetCodeFn != nil {
		return m.GetCodeFn(ctx, address)
	}
	return "", nil
}
func (m *mockRPCClient) GetGasPrice(ctx context.Context) (uint64, error) {
	if m.GetGasPriceFn != nil {
		return m.GetGasPriceFn(ctx)
	}
	return 0, nil
}
func (m *mockRPCClient) GetBaseFee(ctx context.Context) (uint64, error) {
	if m.GetBaseFeeFn != nil {
		return m.GetBaseFeeFn(ctx)
	}
	return 0, nil
}
func (m *mockRPCClient) GetBalance(ctx context.Context, address string) (*big.Int, error) {
	if m.GetBalanceFn != nil {
		return m.GetBalanceFn(ctx, address)
	}
	return big.NewInt(0), nil
}
func (m *mockRPCClient) GetTransactionReceipt(ctx context.Context, txHash string) (*rpc.TransactionReceipt, error) {
	if m.GetTransactionReceiptFn != nil {
		return m.GetTransactionReceiptFn(ctx, txHash)
	}
	return nil, nil
}
func (m *mockRPCClient) GetTransactionReceiptsBatch(ctx context.Context, txHashes []string) ([]*rpc.TransactionReceipt, error) {
	if m.GetTransactionReceiptsBatchFn != nil {
		return m.GetTransactionReceiptsBatchFn(ctx, txHashes)
	}
	return nil, nil
}
func (m *mockRPCClient) GetTransactionByHash(ctx context.Context, txHash string) (*rpc.TransactionInfo, error) {
	return nil, nil
}

// mockStorage implements storage.Storage with sensible defaults.
type mockStorage struct {
	CreateTestRunFn          func(ctx context.Context, run *storage.TestRun) error
	UpdateTestRunFn          func(ctx context.Context, run *storage.TestRun) error
	CompleteTestRunFn        func(ctx context.Context, id string, run *storage.TestRun) error
	GetTestRunFn             func(ctx context.Context, id string) (*storage.TestRun, error)
	ListTestRunsFn           func(ctx context.Context, limit, offset int) (*storage.PaginatedTestRuns, error)
	DeleteTestRunFn          func(ctx context.Context, id string) error
	UpdateTestRunMetadataFn  func(ctx context.Context, id string, update *storage.TestRunMetadataUpdate) error
	BulkInsertTimeSeriesFn   func(ctx context.Context, testID string, points []storage.TimeSeriesPoint) error
	GetTimeSeriesFn          func(ctx context.Context, testID string) ([]storage.TimeSeriesPoint, error)
	BulkInsertTxLogsFn       func(ctx context.Context, testID string, logs []storage.TxLogEntry) error
	GetTxLogsFn              func(ctx context.Context, testID string, limit, offset int) (*storage.PaginatedTxLogs, error)
	GetTxLogByHashFn         func(ctx context.Context, txHash string) (*storage.TxLogEntry, error)
}

func (m *mockStorage) CreateTestRun(ctx context.Context, run *storage.TestRun) error {
	if m.CreateTestRunFn != nil {
		return m.CreateTestRunFn(ctx, run)
	}
	return nil
}
func (m *mockStorage) UpdateTestRun(ctx context.Context, run *storage.TestRun) error {
	if m.UpdateTestRunFn != nil {
		return m.UpdateTestRunFn(ctx, run)
	}
	return nil
}
func (m *mockStorage) CompleteTestRun(ctx context.Context, id string, run *storage.TestRun) error {
	if m.CompleteTestRunFn != nil {
		return m.CompleteTestRunFn(ctx, id, run)
	}
	return nil
}
func (m *mockStorage) GetTestRun(ctx context.Context, id string) (*storage.TestRun, error) {
	if m.GetTestRunFn != nil {
		return m.GetTestRunFn(ctx, id)
	}
	return nil, nil
}
func (m *mockStorage) ListTestRuns(ctx context.Context, limit, offset int) (*storage.PaginatedTestRuns, error) {
	if m.ListTestRunsFn != nil {
		return m.ListTestRunsFn(ctx, limit, offset)
	}
	return &storage.PaginatedTestRuns{Runs: []storage.TestRun{}, Total: 0, Limit: limit, Offset: offset}, nil
}
func (m *mockStorage) DeleteTestRun(ctx context.Context, id string) error {
	if m.DeleteTestRunFn != nil {
		return m.DeleteTestRunFn(ctx, id)
	}
	return nil
}
func (m *mockStorage) UpdateTestRunMetadata(ctx context.Context, id string, update *storage.TestRunMetadataUpdate) error {
	if m.UpdateTestRunMetadataFn != nil {
		return m.UpdateTestRunMetadataFn(ctx, id, update)
	}
	return nil
}
func (m *mockStorage) BulkInsertTimeSeries(ctx context.Context, testID string, points []storage.TimeSeriesPoint) error {
	if m.BulkInsertTimeSeriesFn != nil {
		return m.BulkInsertTimeSeriesFn(ctx, testID, points)
	}
	return nil
}
func (m *mockStorage) GetTimeSeries(ctx context.Context, testID string) ([]storage.TimeSeriesPoint, error) {
	if m.GetTimeSeriesFn != nil {
		return m.GetTimeSeriesFn(ctx, testID)
	}
	return nil, nil
}
func (m *mockStorage) BulkInsertTxLogs(ctx context.Context, testID string, logs []storage.TxLogEntry) error {
	if m.BulkInsertTxLogsFn != nil {
		return m.BulkInsertTxLogsFn(ctx, testID, logs)
	}
	return nil
}
func (m *mockStorage) GetTxLogs(ctx context.Context, testID string, limit, offset int) (*storage.PaginatedTxLogs, error) {
	if m.GetTxLogsFn != nil {
		return m.GetTxLogsFn(ctx, testID, limit, offset)
	}
	return &storage.PaginatedTxLogs{Transactions: []storage.TxLogEntry{}, Total: 0, Limit: limit, Offset: offset}, nil
}
func (m *mockStorage) GetTxLogByHash(ctx context.Context, txHash string) (*storage.TxLogEntry, error) {
	if m.GetTxLogByHashFn != nil {
		return m.GetTxLogByHashFn(ctx, txHash)
	}
	return nil, nil
}
func (m *mockStorage) Close() error { return nil }

// mockSender implements TxSender.
type mockSender struct {
	SendBatchAsyncFn func(ctx context.Context, txDataSlice [][]byte, callbacks []func(error)) bool
}

func (m *mockSender) SendBatchAsync(ctx context.Context, txDataSlice [][]byte, callbacks []func(error)) bool {
	if m.SendBatchAsyncFn != nil {
		return m.SendBatchAsyncFn(ctx, txDataSlice, callbacks)
	}
	return true
}

// mockDeployer implements ContractDeployer.
type mockDeployer struct {
	DeployAllWithProgressFn    func(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error)
	ValidateCachedContractsFn  func(ctx context.Context, cached map[string]string) (valid map[string]common.Address, invalid []string)
	SetUseLegacyFn             func(useLegacy bool)
	PreMintNFTsFn              func(ctx context.Context, minter *account.Account, nftAddr common.Address, count int, onProgress contract.PreMintProgress) error
}

func (m *mockDeployer) DeployAllWithProgress(ctx context.Context, deployer *account.Account, onProgress contract.ProgressCallback) (map[string]common.Address, error) {
	if m.DeployAllWithProgressFn != nil {
		return m.DeployAllWithProgressFn(ctx, deployer, onProgress)
	}
	return map[string]common.Address{}, nil
}
func (m *mockDeployer) ValidateCachedContracts(ctx context.Context, cached map[string]string) (valid map[string]common.Address, invalid []string) {
	if m.ValidateCachedContractsFn != nil {
		return m.ValidateCachedContractsFn(ctx, cached)
	}
	return map[string]common.Address{}, nil
}
func (m *mockDeployer) SetUseLegacy(useLegacy bool) {
	if m.SetUseLegacyFn != nil {
		m.SetUseLegacyFn(useLegacy)
	}
}
func (m *mockDeployer) PreMintNFTs(ctx context.Context, minter *account.Account, nftAddr common.Address, count int, onProgress contract.PreMintProgress) error {
	if m.PreMintNFTsFn != nil {
		return m.PreMintNFTsFn(ctx, minter, nftAddr, count, onProgress)
	}
	return nil
}

// mockAccountManager implements AccountManager.
type mockAccountManager struct {
	accounts        []*account.Account
	dynamicAccounts []*account.Account
	funded          int

	GenerateDynamicAccountsFn  func(count int) error
	FundAccountsFn             func(ctx context.Context, sendClient, syncClient rpc.Client, accounts []*account.Account) error
	FundDynamicAccountsFn      func(ctx context.Context, sendClient, syncClient rpc.Client) error
	ValidateBalancesFn         func(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account)
	InitializeNoncesFn         func(ctx context.Context, client rpc.Client, numAccounts int) error
	InitializeDynamicNoncesFn  func(ctx context.Context, client rpc.Client) error
	RecycleFundsFn             func(ctx context.Context, client rpc.Client) (int, error)
	ExportDynamicAccountKeysFn func() []account.AccountKeyPair
}

func (m *mockAccountManager) GetAccounts() []*account.Account        { return m.accounts }
func (m *mockAccountManager) GetDynamicAccounts() []*account.Account  { return m.dynamicAccounts }
func (m *mockAccountManager) SetDynamicAccounts(accs []*account.Account) { m.dynamicAccounts = accs }
func (m *mockAccountManager) GetAccountsFunded() int                  { return m.funded }

func (m *mockAccountManager) GenerateDynamicAccounts(count int) error {
	if m.GenerateDynamicAccountsFn != nil {
		return m.GenerateDynamicAccountsFn(count)
	}
	return nil
}
func (m *mockAccountManager) FundAccounts(ctx context.Context, sendClient, syncClient rpc.Client, accounts []*account.Account) error {
	if m.FundAccountsFn != nil {
		return m.FundAccountsFn(ctx, sendClient, syncClient, accounts)
	}
	return nil
}
func (m *mockAccountManager) FundDynamicAccounts(ctx context.Context, sendClient, syncClient rpc.Client) error {
	if m.FundDynamicAccountsFn != nil {
		return m.FundDynamicAccountsFn(ctx, sendClient, syncClient)
	}
	return nil
}
func (m *mockAccountManager) ValidateBalances(ctx context.Context, client rpc.Client, accounts []*account.Account, minBalance *big.Int) (funded, unfunded []*account.Account) {
	if m.ValidateBalancesFn != nil {
		return m.ValidateBalancesFn(ctx, client, accounts, minBalance)
	}
	return accounts, nil
}
func (m *mockAccountManager) InitializeNonces(ctx context.Context, client rpc.Client, numAccounts int) error {
	if m.InitializeNoncesFn != nil {
		return m.InitializeNoncesFn(ctx, client, numAccounts)
	}
	return nil
}
func (m *mockAccountManager) InitializeDynamicNonces(ctx context.Context, client rpc.Client) error {
	if m.InitializeDynamicNoncesFn != nil {
		return m.InitializeDynamicNoncesFn(ctx, client)
	}
	return nil
}
func (m *mockAccountManager) RecycleFunds(ctx context.Context, client rpc.Client) (int, error) {
	if m.RecycleFundsFn != nil {
		return m.RecycleFundsFn(ctx, client)
	}
	return 0, nil
}
func (m *mockAccountManager) ExportDynamicAccountKeys() []account.AccountKeyPair {
	if m.ExportDynamicAccountKeysFn != nil {
		return m.ExportDynamicAccountKeysFn()
	}
	return nil
}
