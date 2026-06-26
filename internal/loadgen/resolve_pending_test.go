package loadgen

import (
	"context"
	"log/slog"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/storage"
)

func newReceiptTestLG(receiptFn func(ctx context.Context, txHash string) (*rpc.TransactionReceipt, error), pending int) *LoadGenerator {
	lg := &LoadGenerator{
		logger:           slog.Default(),
		metricsCol:       metrics.NewInMemoryCollector(true),
		l2Client:         &mockRPCClient{GetTransactionReceiptFn: receiptFn},
		txLoggingEnabled: true,
	}
	for i := 0; i < pending; i++ {
		h := common.BigToHash(big.NewInt(int64(i + 1)))
		lg.pendingTxs.Store(h, &storage.TxLogEntry{TxHash: h.Hex(), Status: "pending"})
	}
	return lg
}

// A force stop arriving while we are inside the receipt-fetch loop must abort it
// promptly (cancel in-flight lookups + skip the rest), not run to completion.
func TestResolvePendingViaReceipts_ForceStopAbortsMidLoop(t *testing.T) {
	// Receipt lookup blocks until its context is canceled — simulates a slow
	// remote fetch that would otherwise hang the loop.
	lg := newReceiptTestLG(func(ctx context.Context, _ string) (*rpc.TransactionReceipt, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, 50)

	done := make(chan struct{})
	go func() {
		lg.resolvePendingViaReceipts()
		close(done)
	}()

	// Simulate the user clicking Stop shortly after resolution begins.
	time.Sleep(50 * time.Millisecond)
	atomic.StoreInt32(&lg.forceStop, 1)

	select {
	case <-done:
		// aborted promptly — good
	case <-time.After(3 * time.Second):
		t.Fatal("resolvePendingViaReceipts did not abort within 3s of force stop")
	}
}

// If a force stop is already set when resolution starts, it should skip all
// lookups and return immediately.
func TestResolvePendingViaReceipts_ForceStopPresetSkipsAll(t *testing.T) {
	var calls int32
	lg := newReceiptTestLG(func(ctx context.Context, _ string) (*rpc.TransactionReceipt, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	}, 50)
	atomic.StoreInt32(&lg.forceStop, 1)

	done := make(chan struct{})
	go func() {
		lg.resolvePendingViaReceipts()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resolvePendingViaReceipts did not return promptly with force stop preset")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("expected 0 receipt lookups with force stop preset, got %d", n)
	}
}
