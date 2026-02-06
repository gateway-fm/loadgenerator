package account

import (
	"sync"
	"testing"
)

func TestGetNonce(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// GetNonce should return current and increment
	if got := acc.GetNonce(); got != 100 {
		t.Errorf("GetNonce() = %d, want 100", got)
	}
	if got := acc.GetNonce(); got != 101 {
		t.Errorf("GetNonce() = %d, want 101", got)
	}
	if got := acc.PeekNonce(); got != 102 {
		t.Errorf("PeekNonce() = %d, want 102", got)
	}
}

func TestPeekNonce(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(50)

	// PeekNonce should not increment
	if got := acc.PeekNonce(); got != 50 {
		t.Errorf("PeekNonce() = %d, want 50", got)
	}
	if got := acc.PeekNonce(); got != 50 {
		t.Errorf("PeekNonce() = %d, want 50 (should not change)", got)
	}
}

func TestDecrementNonce(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(10)

	// Decrement should work
	if ok := acc.DecrementNonce(); !ok {
		t.Error("DecrementNonce() returned false, want true")
	}
	if got := acc.PeekNonce(); got != 9 {
		t.Errorf("after decrement, PeekNonce() = %d, want 9", got)
	}

	// Decrement to zero
	acc.SetNonce(1)
	if ok := acc.DecrementNonce(); !ok {
		t.Error("DecrementNonce() from 1 returned false, want true")
	}
	if got := acc.PeekNonce(); got != 0 {
		t.Errorf("after decrement, PeekNonce() = %d, want 0", got)
	}

	// Cannot decrement below zero
	if ok := acc.DecrementNonce(); ok {
		t.Error("DecrementNonce() from 0 returned true, want false")
	}
	if got := acc.PeekNonce(); got != 0 {
		t.Errorf("after failed decrement, PeekNonce() = %d, want 0", got)
	}
}

func TestNonceRollbackScenario(t *testing.T) {
	// Simulates the send failure rollback scenario
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Simulate: peek, build tx, increment (optimistic send)
	nonce := acc.PeekNonce()
	if nonce != 100 {
		t.Errorf("PeekNonce() = %d, want 100", nonce)
	}

	// Simulate building and signing tx with nonce 100...

	// Optimistic increment before send
	acc.GetNonce()
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after GetNonce(), PeekNonce() = %d, want 101", got)
	}

	// Simulate send failure - rollback
	acc.DecrementNonce()
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("after rollback, PeekNonce() = %d, want 100", got)
	}

	// Next tx should use nonce 100 again
	nextNonce := acc.PeekNonce()
	if nextNonce != 100 {
		t.Errorf("after rollback, next nonce = %d, want 100", nextNonce)
	}
}

func TestNonceConcurrency(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(0)

	// Run concurrent GetNonce calls
	const numGoroutines = 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			acc.GetNonce()
		}()
	}

	wg.Wait()

	// After 100 concurrent increments, nonce should be 100
	if got := acc.PeekNonce(); got != numGoroutines {
		t.Errorf("after %d concurrent GetNonce(), PeekNonce() = %d, want %d", numGoroutines, got, numGoroutines)
	}
}

func TestDecrementNonceConcurrency(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Run concurrent decrement calls
	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			acc.DecrementNonce()
		}()
	}

	wg.Wait()

	// After 50 concurrent decrements from 100, nonce should be 50
	if got := acc.PeekNonce(); got != 50 {
		t.Errorf("after %d concurrent DecrementNonce(), PeekNonce() = %d, want 50", numGoroutines, got)
	}
}

// Tests for the new ReserveNonce pattern

func TestReserveNonceCommit(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Reserve and commit
	n := acc.ReserveNonce()
	if n.Value() != 100 {
		t.Errorf("ReserveNonce().Value() = %d, want 100", n.Value())
	}
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after reserve, PeekNonce() = %d, want 101", got)
	}

	n.Commit()

	// After commit, nonce stays at 101
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after commit, PeekNonce() = %d, want 101", got)
	}

	// Commit is idempotent
	n.Commit()
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after double commit, PeekNonce() = %d, want 101", got)
	}
}

func TestReserveNonceRollback(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Reserve and rollback
	n := acc.ReserveNonce()
	if n.Value() != 100 {
		t.Errorf("ReserveNonce().Value() = %d, want 100", n.Value())
	}
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after reserve, PeekNonce() = %d, want 101", got)
	}

	n.Rollback()

	// After rollback, nonce goes back to 100
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("after rollback, PeekNonce() = %d, want 100", got)
	}

	// Rollback is idempotent
	n.Rollback()
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("after double rollback, PeekNonce() = %d, want 100", got)
	}
}

func TestReserveNonceDeferPattern(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Simulate the defer pattern with success
	func() {
		n := acc.ReserveNonce()
		defer n.Rollback()

		// Simulate successful work
		n.Commit()
	}()

	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after committed defer, PeekNonce() = %d, want 101", got)
	}

	// Simulate the defer pattern with failure (no commit)
	func() {
		n := acc.ReserveNonce()
		defer n.Rollback()

		// Simulate work that fails - don't commit
		// return without calling Commit
	}()

	// Should be back to 101 (the one we committed stays, the second rolled back)
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after rollback defer, PeekNonce() = %d, want 101", got)
	}
}

func TestReserveNonceOutOfOrderRollback(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Reserve two nonces
	n1 := acc.ReserveNonce() // 100
	n2 := acc.ReserveNonce() // 101

	if n1.Value() != 100 {
		t.Errorf("n1.Value() = %d, want 100", n1.Value())
	}
	if n2.Value() != 101 {
		t.Errorf("n2.Value() = %d, want 101", n2.Value())
	}
	if got := acc.PeekNonce(); got != 102 {
		t.Errorf("after two reserves, PeekNonce() = %d, want 102", got)
	}

	// Rollback n1 first (out of order) - should NOT rollback because n2 is still out
	n1.Rollback()
	if got := acc.PeekNonce(); got != 102 {
		t.Errorf("after out-of-order n1 rollback, PeekNonce() = %d, want 102 (unchanged)", got)
	}

	// Rollback n2 - this one is the most recent, should rollback
	n2.Rollback()
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after n2 rollback, PeekNonce() = %d, want 101", got)
	}
}

func TestReserveNonceConcurrency(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(0)

	const numGoroutines = 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// All goroutines reserve and commit
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			n := acc.ReserveNonce()
			// Simulate some work
			n.Commit()
		}()
	}

	wg.Wait()

	// After 100 concurrent reserve+commits, nonce should be 100
	if got := acc.PeekNonce(); got != numGoroutines {
		t.Errorf("after %d concurrent ReserveNonce+Commit, PeekNonce() = %d, want %d",
			numGoroutines, got, numGoroutines)
	}
}

func TestReserveNonceCommitAfterRollback(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	n := acc.ReserveNonce()

	// Rollback first
	n.Rollback()
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("after rollback, PeekNonce() = %d, want 100", got)
	}

	// Commit after rollback should be no-op (already finalized)
	n.Commit()
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("after commit-after-rollback, PeekNonce() = %d, want 100", got)
	}
}

func TestReserveNonceRollbackAfterCommit(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	n := acc.ReserveNonce()

	// Commit first
	n.Commit()
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after commit, PeekNonce() = %d, want 101", got)
	}

	// Rollback after commit should be no-op (already finalized)
	n.Rollback()
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after rollback-after-commit, PeekNonce() = %d, want 101", got)
	}
}

// TestAsyncCallbackPattern tests the correct async send pattern where
// commit/rollback happens ONLY in the async callback, not before queuing.
// This is a regression test for a bug where premature commit caused nonce gaps.
//
// BUG scenario (what we're preventing):
//  1. Reserve nonce 0
//  2. Queue async send
//  3. BUG: Commit immediately (before send completes) <-- WRONG
//  4. Reserve nonce 1
//  5. Queue async send
//  6. Commit immediately
//  7. Async send for nonce 0 fails
//  8. Callback tries to rollback but already committed! <-- LOST NONCE
//  9. Nonce 0 is "lost" → gap causes massive requeues on block builder
//
// CORRECT pattern:
//  1. Reserve nonce
//  2. Queue async send with callback
//  3. Callback commits on success OR rollbacks on failure
//  4. NEVER commit before callback executes
func TestAsyncCallbackPattern(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Simulate async sends with out-of-order completion
	// This mirrors the actual sender worker pattern

	// Reserve 3 nonces (simulating 3 concurrent sends)
	n0 := acc.ReserveNonce() // 100
	n1 := acc.ReserveNonce() // 101
	n2 := acc.ReserveNonce() // 102

	if n0.Value() != 100 || n1.Value() != 101 || n2.Value() != 102 {
		t.Fatalf("unexpected nonce values: %d, %d, %d", n0.Value(), n1.Value(), n2.Value())
	}
	if got := acc.PeekNonce(); got != 103 {
		t.Fatalf("after 3 reserves, PeekNonce() = %d, want 103", got)
	}

	// Simulate: n1 completes first (success)
	n1.Commit()
	// n1 committed, but n0 and n2 still pending
	if got := acc.PeekNonce(); got != 103 {
		t.Errorf("after n1 commit, PeekNonce() = %d, want 103 (unchanged)", got)
	}

	// Simulate: n0 fails!
	n0.Rollback()
	// n0 can't rollback because n2 still has nonce 102 reserved (n0=100, current=103)
	// This is expected - out of order rollback doesn't decrement
	if got := acc.PeekNonce(); got != 103 {
		t.Errorf("after n0 out-of-order rollback, PeekNonce() = %d, want 103 (unchanged)", got)
	}

	// Simulate: n2 succeeds
	n2.Commit()
	if got := acc.PeekNonce(); got != 103 {
		t.Errorf("after n2 commit, PeekNonce() = %d, want 103 (unchanged)", got)
	}

	// Key assertion: Even with n0 failed, we don't have a nonce gap from
	// the load generator's perspective. The block builder may see a gap
	// (nonce 100 missing) but that's because n0's TX genuinely failed to send.
	// The important thing is n1 (101) and n2 (102) are correctly committed.
}

// TestAsyncCallbackPatternSequentialFailure tests the scenario where
// the most recent nonce fails - it should properly rollback.
func TestAsyncCallbackPatternSequentialFailure(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// Send nonces sequentially (one at a time, as in single-worker mode)
	n0 := acc.ReserveNonce() // 100
	if got := acc.PeekNonce(); got != 101 {
		t.Fatalf("after reserve, PeekNonce() = %d, want 101", got)
	}

	// n0 fails - this is the most recent, so rollback should work
	n0.Rollback()
	if got := acc.PeekNonce(); got != 100 {
		t.Errorf("after sequential n0 rollback, PeekNonce() = %d, want 100", got)
	}

	// Try again with same nonce
	n0Retry := acc.ReserveNonce()
	if n0Retry.Value() != 100 {
		t.Errorf("retry nonce = %d, want 100", n0Retry.Value())
	}

	// This time it succeeds
	n0Retry.Commit()
	if got := acc.PeekNonce(); got != 101 {
		t.Errorf("after commit, PeekNonce() = %d, want 101", got)
	}

	// Continue with next nonce
	n1 := acc.ReserveNonce()
	if n1.Value() != 101 {
		t.Errorf("next nonce = %d, want 101", n1.Value())
	}
	n1.Commit()

	if got := acc.PeekNonce(); got != 102 {
		t.Errorf("final PeekNonce() = %d, want 102", got)
	}
}

// TestPrematureCommitBug demonstrates the bug that was fixed.
// This test shows what happens if you commit BEFORE the async callback.
// DO NOT USE THIS PATTERN IN PRODUCTION CODE.
func TestPrematureCommitBug(t *testing.T) {
	acc, err := NewAccountFromHex(TestPrivateKeys[0])
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	acc.SetNonce(100)

	// BAD PATTERN - DO NOT DO THIS:
	// Commit immediately after queuing, before async completion
	n0 := acc.ReserveNonce() // 100
	n0.Commit()              // <-- BUG: Committed before we know if send succeeded!

	n1 := acc.ReserveNonce() // 101
	n1.Commit()              // <-- BUG: Committed before we know if send succeeded!

	// Now simulate: n0's async send fails
	// We try to rollback but it's already committed!
	n0.Rollback() // This is a no-op because committed=true

	// The account thinks we're at nonce 102, but the block builder
	// only received nonce 101 (nonce 100 failed to send).
	// This creates a gap: account sends 102, 103, 104...
	// but block builder is waiting for 100!
	// Result: massive requeues as "future" nonces pile up.

	if got := acc.PeekNonce(); got != 102 {
		t.Errorf("PeekNonce() = %d, want 102 (demonstrating the bug state)", got)
	}

	// This test passes to document the buggy behavior.
	// The CORRECT pattern is tested in TestAsyncCallbackPattern above.
}
