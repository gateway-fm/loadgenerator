package sender

import (
	"context"
	"encoding/json"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// mockClient implements rpc.Client for testing.
type mockClient struct {
	delay     time.Duration
	failCount int32 // atomic
	sendCount int32 // atomic
	shouldErr bool
}

// Ensure mockClient implements rpc.Client
var _ rpc.Client = (*mockClient)(nil)

func (m *mockClient) SendRawTransaction(ctx context.Context, txRLP []byte) error {
	atomic.AddInt32(&m.sendCount, 1)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.shouldErr {
		atomic.AddInt32(&m.failCount, 1)
		return context.DeadlineExceeded
	}
	return nil
}

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

func TestSenderBasic(t *testing.T) {
	client := &mockClient{}
	s := New(Config{
		Client:      client,
		Concurrency: 10,
	})

	var wg sync.WaitGroup
	var callbackCalled atomic.Bool

	wg.Add(1)
	ok := s.SendAsync(context.Background(), []byte("tx"), func(err error) {
		callbackCalled.Store(true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		wg.Done()
	})

	if !ok {
		t.Error("SendAsync returned false, expected true")
	}

	wg.Wait()

	if !callbackCalled.Load() {
		t.Error("callback was not called")
	}

	if got := atomic.LoadInt32(&client.sendCount); got != 1 {
		t.Errorf("sendCount = %d, want 1", got)
	}
}

func TestSenderAtCapacity(t *testing.T) {
	client := &mockClient{delay: 100 * time.Millisecond}
	s := New(Config{
		Client:      client,
		Concurrency: 2,
	})

	// Fill up the sender
	for i := 0; i < 2; i++ {
		ok := s.SendAsync(context.Background(), []byte("tx"), nil)
		if !ok {
			t.Errorf("SendAsync %d returned false, expected true", i)
		}
	}

	// This one should fail (at capacity)
	ok := s.SendAsync(context.Background(), []byte("tx"), nil)
	if ok {
		t.Error("SendAsync returned true when at capacity, expected false")
	}

	// TrySend should return ErrAtCapacity
	err := s.TrySend(context.Background(), []byte("tx"), nil)
	if err != ErrAtCapacity {
		t.Errorf("TrySend error = %v, want ErrAtCapacity", err)
	}
}

func TestSenderCapacityMetrics(t *testing.T) {
	s := New(Config{
		Client:      &mockClient{delay: 50 * time.Millisecond},
		Concurrency: 5,
	})

	if got := s.Capacity(); got != 5 {
		t.Errorf("Capacity() = %d, want 5", got)
	}

	if got := s.Available(); got != 5 {
		t.Errorf("Available() = %d, want 5", got)
	}

	if got := s.InFlight(); got != 0 {
		t.Errorf("InFlight() = %d, want 0", got)
	}

	// Start 3 sends
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		s.SendAsync(context.Background(), []byte("tx"), func(error) { wg.Done() })
	}

	// Wait a bit for goroutines to start
	time.Sleep(10 * time.Millisecond)

	if got := s.InFlight(); got != 3 {
		t.Errorf("InFlight() = %d, want 3", got)
	}

	if got := s.Available(); got != 2 {
		t.Errorf("Available() = %d, want 2", got)
	}

	wg.Wait()
}

func TestSenderConcurrency(t *testing.T) {
	client := &mockClient{}
	s := New(Config{
		Client:      client,
		Concurrency: 100,
	})

	const numSends = 500
	var wg sync.WaitGroup
	wg.Add(numSends)

	for i := 0; i < numSends; i++ {
		go func() {
			ok := s.SendAsync(context.Background(), []byte("tx"), func(error) {
				wg.Done()
			})
			if !ok {
				// Retry if at capacity with bounded retries to prevent infinite loop
				const maxRetries = 1000
				for retries := 0; !ok && retries < maxRetries; retries++ {
					time.Sleep(time.Millisecond)
					ok = s.SendAsync(context.Background(), []byte("tx"), func(error) {
						wg.Done()
					})
				}
				if !ok {
					t.Errorf("failed to send after %d retries", maxRetries)
					wg.Done() // Prevent deadlock
				}
			}
		}()
	}

	wg.Wait()

	if got := atomic.LoadInt32(&client.sendCount); got != numSends {
		t.Errorf("sendCount = %d, want %d", got, numSends)
	}
}

func TestSenderErrorCallback(t *testing.T) {
	client := &mockClient{shouldErr: true}
	s := New(Config{
		Client:      client,
		Concurrency: 10,
	})

	var wg sync.WaitGroup
	var gotErr error

	wg.Add(1)
	s.SendAsync(context.Background(), []byte("tx"), func(err error) {
		gotErr = err
		wg.Done()
	})

	wg.Wait()

	if gotErr == nil {
		t.Error("expected error in callback, got nil")
	}
}
