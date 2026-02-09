package pipeline

import (
	"context"
	"encoding/json"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/sender"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// mockClient implements rpc.Client for testing.
type mockClient struct {
	sendCount int32 // atomic
	shouldErr bool
}

var _ rpc.Client = (*mockClient)(nil)

func (m *mockClient) SendRawTransaction(ctx context.Context, txRLP []byte) error {
	atomic.AddInt32(&m.sendCount, 1)
	if m.shouldErr {
		return ErrMockSendFailed
	}
	return nil
}

var ErrMockSendFailed = &mockError{}

type mockError struct{}

func (e *mockError) Error() string { return "mock send failed" }

func (m *mockClient) Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	return nil, nil
}

func (m *mockClient) GetNonce(ctx context.Context, address string) (uint64, error) {
	return 0, nil
}

func (m *mockClient) GetBlockNumber(ctx context.Context) (uint64, error) {
	return 0, nil
}

func (m *mockClient) GetBlockByNumber(ctx context.Context, blockNum uint64) (*rpc.Block, error) {
	return nil, nil
}

func (m *mockClient) GetBlockByNumberFull(ctx context.Context, blockNum uint64) (*rpc.BlockFull, error) {
	return nil, nil
}

func (m *mockClient) GetCode(ctx context.Context, address string) (string, error) {
	return "", nil
}

func (m *mockClient) GetGasPrice(ctx context.Context) (uint64, error) {
	return 1_000_000_000, nil // 1 Gwei
}

func (m *mockClient) GetBaseFee(ctx context.Context) (uint64, error) {
	return 1000, nil // 0.000001 Gwei (Base-like)
}

func (m *mockClient) GetConfirmedNonce(ctx context.Context, address string) (uint64, error) {
	return 0, nil
}

func (m *mockClient) GetBalance(ctx context.Context, address string) (*big.Int, error) {
	return big.NewInt(1e18), nil // 1 ETH
}

func (m *mockClient) GetTransactionReceipt(ctx context.Context, txHash string) (*rpc.TransactionReceipt, error) {
	return nil, nil
}

func (m *mockClient) BatchCall(ctx context.Context, calls []rpc.BatchRequest) ([]rpc.BatchResponse, error) {
	return nil, nil
}

func (m *mockClient) GetBlocksByNumberFullBatch(ctx context.Context, blockNums []uint64) ([]*rpc.BlockFull, error) {
	return nil, nil
}

func (m *mockClient) GetTransactionReceiptsBatch(ctx context.Context, txHashes []string) ([]*rpc.TransactionReceipt, error) {
	return nil, nil
}

func (m *mockClient) SendRawTransactionBatch(ctx context.Context, txRLPs [][]byte) []error {
	errs := make([]error, len(txRLPs))
	for i, rlp := range txRLPs {
		errs[i] = m.SendRawTransaction(ctx, rlp)
	}
	return errs
}

// mockMetrics implements metrics.Collector for testing.
type mockMetrics struct {
	txSent      int32 // atomic
	txFailed    int32 // atomic
	txConfirmed int32 // atomic
}

var _ metrics.Collector = (*mockMetrics)(nil)

func (m *mockMetrics) RecordTxSent(common.Hash, time.Time)                 {}
func (m *mockMetrics) RecordTxConfirmed(common.Hash, time.Time)            {}
func (m *mockMetrics) RecordTxFailed(string)                               { atomic.AddInt32(&m.txFailed, 1) }
func (m *mockMetrics) RecordPending(common.Hash, time.Time)                {}
func (m *mockMetrics) RecordPreconfirmed(common.Hash, time.Time)           {}
func (m *mockMetrics) RecordRevoked(common.Hash, time.Time)                {}
func (m *mockMetrics) RecordDropped(common.Hash, time.Time)                {}
func (m *mockMetrics) RecordRequeued(common.Hash, time.Time)               {}
func (m *mockMetrics) RecordTip(*big.Int)                                  {}
func (m *mockMetrics) RecordTipWithConfig(*big.Int, float64, float64)      {}
func (m *mockMetrics) RecordTxType(ptypes.TransactionType, *big.Int, bool) {}
func (m *mockMetrics) InitRealisticMetrics()                               {}
func (m *mockMetrics) SetPendingCount(int64)                               {}
func (m *mockMetrics) AddPendingCount(int64)                               {}
func (m *mockMetrics) SubPendingCount(int64)                               {}
func (m *mockMetrics) SetPeakRate(int)                                     {}
func (m *mockMetrics) IncTxSent() uint64                                   { return uint64(atomic.AddInt32(&m.txSent, 1)) }
func (m *mockMetrics) IncTxFailed() uint64                                 { return 0 }
func (m *mockMetrics) GetTxSent() uint64                                   { return uint64(atomic.LoadInt32(&m.txSent)) }
func (m *mockMetrics) GetTxConfirmed() uint64                              { return 0 }
func (m *mockMetrics) GetTxFailed() uint64                                 { return uint64(atomic.LoadInt32(&m.txFailed)) }
func (m *mockMetrics) GetPendingCount() int64                              { return 0 }
func (m *mockMetrics) GetTxPending() uint64                                { return 0 }
func (m *mockMetrics) GetTxPreconfirmed() uint64                           { return 0 }
func (m *mockMetrics) GetTxRevoked() uint64                                { return 0 }
func (m *mockMetrics) GetTxDropped() uint64                                { return 0 }
func (m *mockMetrics) GetTxRequeued() uint64                               { return 0 }
func (m *mockMetrics) GetSnapshot() metrics.Snapshot                       { return metrics.Snapshot{} }
func (m *mockMetrics) GetTxSentTime(common.Hash) (time.Time, bool)         { return time.Time{}, false }
func (m *mockMetrics) GetLatencyStats() *ptypes.LatencyStats               { return nil }
func (m *mockMetrics) GetPreconfLatencyStats() *ptypes.LatencyStats        { return nil }
func (m *mockMetrics) GetPendingLatencyStats() *ptypes.LatencyStats        { return nil }
func (m *mockMetrics) GetTipHistogram(*ptypes.RealisticTestConfig) []ptypes.TipHistogramBucket {
	return nil
}
func (m *mockMetrics) GetTxTypeMetrics() []ptypes.TxTypeMetrics { return nil }
func (m *mockMetrics) GetFlowStats() *metrics.FlowStats         { return nil }
func (m *mockMetrics) Reset()                                   {}

func TestPipelineExecute(t *testing.T) {
	client := &mockClient{}
	snd := sender.New(sender.Config{
		Client:      client,
		Concurrency: 10,
	})

	chainID := big.NewInt(1)
	gasPrice := big.NewInt(1e9)
	recipient := common.HexToAddress("0x1234567890123456789012345678901234567890")

	pipe := New(Config{
		Builder:  txbuilder.NewETHTransferBuilder(recipient),
		Signer:   types.NewLondonSigner(chainID),
		Sender:   snd,
		ChainID:  chainID,
		GasPrice: gasPrice,
	})

	acc, err := account.NewAccountFromHex(account.TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}
	acc.SetNonce(100)

	result := pipe.Execute(acc)

	if result.Error != nil {
		t.Errorf("Execute returned error: %v", result.Error)
	}

	if !result.Queued {
		t.Error("Execute returned Queued=false, expected true")
	}

	if result.Nonce != 100 {
		t.Errorf("Execute returned Nonce=%d, expected 100", result.Nonce)
	}

	// Wait for async send to complete
	time.Sleep(10 * time.Millisecond)

	if got := atomic.LoadInt32(&client.sendCount); got != 1 {
		t.Errorf("sendCount = %d, want 1", got)
	}

	// Nonce should have advanced (committed)
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("account nonce = %d, want 101", got)
	}
}

func TestPipelineNonceRollbackOnSendFailure(t *testing.T) {
	client := &mockClient{shouldErr: true}
	snd := sender.New(sender.Config{
		Client:      client,
		Concurrency: 10,
	})

	chainID := big.NewInt(1)
	gasPrice := big.NewInt(1e9)
	recipient := common.HexToAddress("0x1234567890123456789012345678901234567890")

	metr := &mockMetrics{}
	pipe := New(Config{
		Builder:  txbuilder.NewETHTransferBuilder(recipient),
		Signer:   types.NewLondonSigner(chainID),
		Sender:   snd,
		Metrics:  metr,
		ChainID:  chainID,
		GasPrice: gasPrice,
	})

	acc, err := account.NewAccountFromHex(account.TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}
	acc.SetNonce(100)

	result := pipe.Execute(acc)

	if result.Error != nil {
		t.Errorf("Execute returned error: %v (should be nil, error happens async)", result.Error)
	}

	if !result.Queued {
		t.Error("Execute returned Queued=false, expected true")
	}

	// Wait for async send to complete and rollback
	time.Sleep(50 * time.Millisecond)

	// Check that metrics recorded the failure
	if got := atomic.LoadInt32(&metr.txFailed); got != 1 {
		t.Errorf("txFailed = %d, want 1", got)
	}

	// CRITICAL: Verify the nonce was actually rolled back
	// The test name claims "NonceRollbackOnSendFailure" - we must verify this!
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("nonce after rollback = %d, want 100 (rollback failed!)", got)
	}
}

func TestPipelineAtCapacity(t *testing.T) {
	client := &mockClient{}
	snd := sender.New(sender.Config{
		Client:      client,
		Concurrency: 10,
	})

	chainID := big.NewInt(1)
	gasPrice := big.NewInt(1e9)
	recipient := common.HexToAddress("0x1234567890123456789012345678901234567890")

	pipe := New(Config{
		Builder:  txbuilder.NewETHTransferBuilder(recipient),
		Signer:   types.NewLondonSigner(chainID),
		Sender:   snd,
		ChainID:  chainID,
		GasPrice: gasPrice,
	})

	acc, err := account.NewAccountFromHex(account.TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	// Execute should succeed
	result := pipe.Execute(acc)

	if !result.Queued {
		t.Error("Expected Execute to be queued")
	}

	if result.Error != nil {
		t.Errorf("Unexpected error: %v", result.Error)
	}
}
