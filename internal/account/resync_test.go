package account

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// resyncClient implements rpc.Client by embedding the interface: only
// GetConfirmedNonce is defined, so any other use panics loudly instead of
// silently succeeding.
type resyncClient struct {
	rpc.Client

	nonce uint64
	err   error
	calls int64 // atomic
}

func (c *resyncClient) GetConfirmedNonce(_ context.Context, _ string) (uint64, error) {
	atomic.AddInt64(&c.calls, 1)
	if c.err != nil {
		return 0, c.err
	}
	return c.nonce, nil
}

func (c *resyncClient) callCount() int64 { return atomic.LoadInt64(&c.calls) }

func testAccount(t *testing.T) *Account {
	t.Helper()
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}
	return acc
}

// The counter must never move BACKWARDS. Reads are load-balanced across RPC replicas
// that each follow the sequencer feed independently, so a read landing on a lagging
// replica returns a nonce below what the chain has already consumed. Adopting it yields
// "nonce too low", which triggers another resync, which feeds itself — 4,646 such
// rejections in 90 seconds before this was set-if-higher (PRST-4262).
func TestMaybeResyncFromChain_NeverMovesBackwards(t *testing.T) {
	acc := testAccount(t)
	acc.SetNonce(100)

	client := &resyncClient{nonce: 90} // lagging replica
	did, err := acc.MaybeResyncFromChain(context.Background(), client, 0)
	if err != nil {
		t.Fatalf("MaybeResyncFromChain: %v", err)
	}
	if !did {
		t.Fatal("expected the resync to be attempted")
	}
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("nonce moved backwards to %d; must stay at 100", got)
	}
}

func TestMaybeResyncFromChain_AdoptsHigherNonce(t *testing.T) {
	acc := testAccount(t)
	acc.SetNonce(100)

	client := &resyncClient{nonce: 150}
	if _, err := acc.MaybeResyncFromChain(context.Background(), client, 0); err != nil {
		t.Fatalf("MaybeResyncFromChain: %v", err)
	}
	if got := acc.PeekNonce(); got != 150 {
		t.Errorf("nonce = %d, want 150", got)
	}
}

// Only free nonces the chain has ALREADY CONSUMED may be dropped. Clearing the whole
// free list (as ResyncFromChain does, correctly, at test start) discards a rolled-back
// nonce sitting BELOW the head, leaving a hole the chain can never pass and stalling the
// account permanently — the exact failure the resync exists to prevent. This is also the
// property a future "simplify to match ResyncFromChain" change would silently undo.
func TestMaybeResyncFromChain_PrunesOnlyConsumedFreeNonces(t *testing.T) {
	acc := testAccount(t)
	acc.SetNonce(100)

	// Reserve and roll back a spread of nonces so the free list holds 100..104.
	var reserved []*Nonce
	for i := 0; i < 5; i++ {
		reserved = append(reserved, acc.ReserveNonce())
	}
	for _, n := range reserved {
		n.Rollback()
	}

	// The chain has consumed through 102, so 100-102 are spent and 103-104 reusable.
	client := &resyncClient{nonce: 103}
	if _, err := acc.MaybeResyncFromChain(context.Background(), client, 0); err != nil {
		t.Fatalf("MaybeResyncFromChain: %v", err)
	}

	// The next reservations must come from the surviving free entries, in order, and
	// must never hand back a consumed nonce.
	for _, want := range []uint64{103, 104} {
		got := acc.ReserveNonce().Value()
		if got != want {
			t.Fatalf("ReserveNonce() = %d, want %d (consumed nonce handed back, or a "+
				"reusable one was discarded)", got, want)
		}
	}
	// Free list exhausted: the next one comes from the counter.
	if got := acc.ReserveNonce().Value(); got != 105 {
		t.Errorf("ReserveNonce() = %d, want 105 after the free list drained", got)
	}
}

// A flood of rejections for one account must collapse into a single
// eth_getTransactionCount, or the recovery itself moves the bottleneck onto the RPC
// node — which is what it is meant to relieve.
func TestMaybeResyncFromChain_RateLimited(t *testing.T) {
	acc := testAccount(t)
	acc.SetNonce(10)
	client := &resyncClient{nonce: 20}

	const interval = 50 * time.Millisecond
	did, err := acc.MaybeResyncFromChain(context.Background(), client, interval)
	if err != nil || !did {
		t.Fatalf("first resync should run: did=%v err=%v", did, err)
	}

	// Immediately after, it must be suppressed.
	did, err = acc.MaybeResyncFromChain(context.Background(), client, interval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if did {
		t.Error("second resync ran inside the rate-limit window")
	}
	if got := client.callCount(); got != 1 {
		t.Errorf("client called %d times, want 1", got)
	}

	// After the window, it runs again.
	time.Sleep(interval + 20*time.Millisecond)
	did, err = acc.MaybeResyncFromChain(context.Background(), client, interval)
	if err != nil || !did {
		t.Fatalf("resync should run after the interval elapsed: did=%v err=%v", did, err)
	}
	if got := client.callCount(); got != 2 {
		t.Errorf("client called %d times, want 2", got)
	}
}

// The CAS is what makes concurrent rejections collapse rather than stampede.
func TestMaybeResyncFromChain_ConcurrentCallersCollapse(t *testing.T) {
	acc := testAccount(t)
	acc.SetNonce(10)
	client := &resyncClient{nonce: 20}

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = acc.MaybeResyncFromChain(context.Background(), client, time.Hour)
		}()
	}
	wg.Wait()

	if got := client.callCount(); got != 1 {
		t.Errorf("client called %d times under %d concurrent callers, want 1",
			got, goroutines)
	}
}

// A failed lookup still consumes the rate-limit slot (it reports did=true), so a broken
// RPC endpoint cannot be retried in a tight loop.
func TestMaybeResyncFromChain_ErrorIsReportedAndRateLimited(t *testing.T) {
	acc := testAccount(t)
	acc.SetNonce(10)
	client := &resyncClient{err: errors.New("connection refused")}

	did, err := acc.MaybeResyncFromChain(context.Background(), client, time.Hour)
	if err == nil {
		t.Fatal("expected the client error to be surfaced")
	}
	if !did {
		t.Error("a resync that reached the client should report that it ran")
	}
	if got := acc.PeekNonce(); got != 10 {
		t.Errorf("nonce changed to %d on a failed lookup", got)
	}

	did, _ = acc.MaybeResyncFromChain(context.Background(), client, time.Hour)
	if did {
		t.Error("a failed resync must still hold the rate-limit slot")
	}
}
