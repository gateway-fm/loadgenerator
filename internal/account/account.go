// Package account manages test accounts for load generation.
package account

import (
	"context"
	"crypto/ecdsa"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// Account holds a test account's keys and state.
type Account struct {
	PrivateKey *ecdsa.PrivateKey
	Address    common.Address
	nonce      uint64
	freeNonces []uint64 // rolled-back nonces available for reuse (sorted ascending)
	mu         sync.Mutex
	// Unix nanos of the last in-test nonce resync, for rate-limiting
	// MaybeResyncFromChain. Separate from mu so the common "too soon, skip"
	// path costs one atomic load and never contends with nonce reservation.
	lastResyncNano atomic.Int64
}

// NewAccount creates an account from a private key.
func NewAccount(privateKey *ecdsa.PrivateKey) *Account {
	return &Account{
		PrivateKey: privateKey,
		Address:    crypto.PubkeyToAddress(privateKey.PublicKey),
	}
}

// NewAccountFromHex creates an account from a hex-encoded private key.
func NewAccountFromHex(hexKey string) (*Account, error) {
	privateKey, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, err
	}
	return NewAccount(privateKey), nil
}

// Nonce represents a reserved nonce that must be committed or rolled back.
// Use defer n.Rollback() immediately after reserving to ensure cleanup.
type Nonce struct {
	value     uint64
	account   *Account
	committed atomic.Bool
}

// Value returns the nonce value.
func (n *Nonce) Value() uint64 {
	return n.value
}

// Commit marks the nonce as successfully used.
// Safe to call multiple times (idempotent).
func (n *Nonce) Commit() {
	n.committed.Store(true)
}

// Rollback returns the nonce to the pool if not committed.
// Safe to call multiple times (idempotent).
// Typically called via defer.
func (n *Nonce) Rollback() {
	if n.committed.Swap(true) {
		return // Already committed or rolled back
	}
	n.account.rollback(n.value)
}

// ReserveNonce reserves the next nonce for use.
// The returned Nonce MUST be either Committed or Rolled back.
// Use defer n.Rollback() for automatic cleanup on error paths.
//
// Example:
//
//	n := acc.ReserveNonce()
//	defer n.Rollback() // Auto-rollback on any error
//	if err := doSomething(n.Value()); err != nil {
//	    return err // Rollback happens via defer
//	}
//	n.Commit() // Success - prevent rollback
func (a *Account) ReserveNonce() *Nonce {
	a.mu.Lock()
	var nonce uint64
	if len(a.freeNonces) > 0 {
		// Reuse the lowest rolled-back nonce to fill gaps
		nonce = a.freeNonces[0]
		a.freeNonces = a.freeNonces[1:]
	} else {
		nonce = a.nonce
		a.nonce++
	}
	a.mu.Unlock()

	return &Nonce{
		value:   nonce,
		account: a,
	}
}

// rollback returns a nonce to the free pool for reuse.
// If it's the most recent nonce, simply decrements the counter.
// Otherwise, inserts into the sorted free list so it gets reused
// by the next ReserveNonce call, filling the gap.
func (a *Account) rollback(nonce uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Fast path: if this was the most recent nonce, just decrement
	if a.nonce == nonce+1 {
		a.nonce = nonce
		return
	}

	// Out-of-order rollback: insert into free list (sorted ascending)
	// so the lowest nonce gets reused first, filling gaps immediately.
	pos := sort.Search(len(a.freeNonces), func(i int) bool {
		return a.freeNonces[i] >= nonce
	})
	// Avoid duplicates
	if pos < len(a.freeNonces) && a.freeNonces[pos] == nonce {
		return
	}
	a.freeNonces = append(a.freeNonces, 0)
	copy(a.freeNonces[pos+1:], a.freeNonces[pos:])
	a.freeNonces[pos] = nonce
}

// Resync fetches the current nonce from the chain and updates local state.
// Use this to recover from nonce drift after network issues.
// Uses set-if-higher pattern to avoid race conditions with concurrent nonce reservations.
func (a *Account) Resync(ctx context.Context, client rpc.Client) error {
	nonce, err := client.GetNonce(ctx, a.Address.Hex())
	if err != nil {
		return err
	}
	a.mu.Lock()
	// Only update if the fetched nonce is higher to avoid going backwards.
	// This prevents race conditions where another goroutine reserved nonces
	// between the RPC call and this lock acquisition.
	if nonce > a.nonce {
		a.nonce = nonce
	}
	// Clear free list — chain state is authoritative after resync
	a.freeNonces = a.freeNonces[:0]
	a.mu.Unlock()
	return nil
}

// ResyncFromChain fetches the confirmed nonce directly from the chain, bypassing any cache.
// Use this when starting a fresh test to ensure nonces match the true chain state.
// Uses set-if-higher pattern to avoid race conditions with concurrent nonce reservations.
func (a *Account) ResyncFromChain(ctx context.Context, client rpc.Client) error {
	nonce, err := client.GetConfirmedNonce(ctx, a.Address.Hex())
	if err != nil {
		return err
	}
	a.mu.Lock()
	// Only update if the fetched nonce is higher to avoid going backwards.
	// This prevents race conditions where another goroutine reserved nonces
	// between the RPC call and this lock acquisition.
	if nonce > a.nonce {
		a.nonce = nonce
	}
	// Clear free list — chain state is authoritative after resync
	a.freeNonces = a.freeNonces[:0]
	a.mu.Unlock()
	return nil
}

// ForceResync fetches the current nonce from the builder and overwrites local
// state unconditionally. Unlike Resync, this can downgrade the local nonce.
//
// Use ONLY at test-start when no goroutines are reserving nonces — the in-test
// "set if higher" guard exists to defend against concurrent reservers, but
// between runs we MUST trust the builder's view, otherwise a previous failed
// test's inflated local counter sticks forever (RD-892).
func (a *Account) ForceResync(ctx context.Context, client rpc.Client) error {
	nonce, err := client.GetNonce(ctx, a.Address.Hex())
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.nonce = nonce
	a.freeNonces = a.freeNonces[:0]
	a.mu.Unlock()
	return nil
}

// ForceResyncFromChain is like ForceResync but reads the chain-confirmed nonce
// directly, bypassing the builder cache. Use after a builder /reset-nonces.
func (a *Account) ForceResyncFromChain(ctx context.Context, client rpc.Client) error {
	nonce, err := client.GetConfirmedNonce(ctx, a.Address.Hex())
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.nonce = nonce
	a.freeNonces = a.freeNonces[:0]
	a.mu.Unlock()
	return nil
}

// MaybeResyncFromChain force-resyncs this account's nonce from confirmed chain
// state, but at most once per minInterval. Returns true if a resync ran.
//
// This is the in-test recovery path for nonce drift, and on a chain with no
// mempool it is what keeps a run alive (PRST-4262).
//
// Rollback() already recycles the nonce of a rejected send, but nothing bounds
// how far the reserved counter runs ahead of chain state: the generator reserves
// at the target rate while inclusion proceeds at whatever rate the chain manages,
// so any sustained shortfall becomes an ever-growing gap. On Arbitrum Nitro there
// is no mempool to hold the out-of-order remainder — a transaction whose
// predecessor is missing is rejected outright — so once an account's counter is
// more than a little ahead, EVERY subsequent transaction from it is refused with
// "nonce too high" and that account is dead for the rest of the run. Observed
// live: all 500 sender accounts dead within ~70s at a 1000 tx/s target, gaps of
// ~79 nonces (tx: 148 vs state: 69), confirmations frozen while the chain sat
// idle with 100% of blocks closing "tx exhausted".
//
// Resyncing on the rejection turns a permanently dead account back into a
// working one. Rate-limited because at high load thousands of rejections can
// arrive per second per account, and one eth_getTransactionCount each would
// simply move the overload to the RPC node.
//
// Uses the SET-IF-HIGHER ResyncFromChain, never the unconditional
// ForceResyncFromChain. Downgrading the counter here is actively harmful when
// reads are load-balanced across several RPC replicas: each replica follows the
// sequencer feed independently, so a read that lands on a lagging replica returns
// a nonce BELOW what the chain has already consumed. Overwriting with it sends
// the account backwards, the next transaction is refused "nonce too low", that
// triggers another resync, and the error feeds itself. Measured live on a 4h
// soak with 2 RPC replicas: throughput decayed 662 -> 570 tx/s over 30 minutes
// with rejections becoming exclusively "nonce too low".
//
// Set-if-higher still fixes the drift that matters. A counter BEHIND the chain
// (nonce too low) is corrected upward, which is the case this recovers. A counter
// AHEAD of the chain (nonce too high) needs no resync: Rollback already returns
// the rejected nonce to the free list for reuse, and with a large sender pool and
// a large sequencer reorder cache that case stopped occurring at all.
func (a *Account) MaybeResyncFromChain(ctx context.Context, client rpc.Client, minInterval time.Duration) (bool, error) {
	now := time.Now().UnixNano()
	last := a.lastResyncNano.Load()
	if now-last < int64(minInterval) {
		return false, nil
	}
	// CAS so that concurrent rejections for the same account collapse into a
	// single resync rather than a stampede.
	if !a.lastResyncNano.CompareAndSwap(last, now) {
		return false, nil
	}
	nonce, err := client.GetConfirmedNonce(ctx, a.Address.Hex())
	if err != nil {
		return true, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Never go backwards (see the comment above): a lagging replica's read would
	// otherwise send the account into a nonce-too-low feedback loop.
	if nonce > a.nonce {
		a.nonce = nonce
	}
	// Drop only the free nonces the chain has already consumed. The plain
	// ResyncFromChain clears the WHOLE free list, which is correct at test start
	// but wrong here: a nonce rolled back from a failed send sits in that list
	// BELOW the head, and discarding it leaves a hole the chain can never pass,
	// permanently stalling the account -- the exact failure this resync exists to
	// prevent. Anything still >= the confirmed nonce is genuinely reusable.
	kept := a.freeNonces[:0]
	for _, n := range a.freeNonces {
		if n >= nonce {
			kept = append(kept, n)
		}
	}
	a.freeNonces = kept
	return true, nil
}

// SetNonce sets the nonce value directly and clears the free list.
// Prefer Resync for fetching from chain, or ReserveNonce for normal use.
func (a *Account) SetNonce(nonce uint64) {
	a.mu.Lock()
	a.nonce = nonce
	a.freeNonces = a.freeNonces[:0]
	a.mu.Unlock()
}

// PeekNonce returns the current nonce without incrementing.
func (a *Account) PeekNonce() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nonce
}

// GetNonce returns the current nonce and increments it atomically.
// Deprecated: Use ReserveNonce for proper rollback handling.
func (a *Account) GetNonce() uint64 {
	a.mu.Lock()
	nonce := a.nonce
	a.nonce++
	a.mu.Unlock()
	return nonce
}

// DecrementNonce decrements the nonce by 1, used to rollback after failed sends.
// Returns false if nonce was already 0 (cannot decrement).
// Deprecated: Use ReserveNonce/Rollback for proper rollback handling.
func (a *Account) DecrementNonce() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.nonce == 0 {
		return false
	}
	a.nonce--
	return true
}

// Well-known test private keys (from Anvil/Hardhat default accounts).
var TestPrivateKeys = []string{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80", // Account 0
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d", // Account 1
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a", // Account 2
	"7c852118294e51e653712a81e05800f419141751be58f605c371e15141b007a6", // Account 3
	"47e179ec197488593b187f80a00eb0da91f1b9d0b13f8733639f19c30a34926a", // Account 4
	"8b3a350cf5c34c9194ca85829a2df0ec3153be0318b5e2d3348e872092edffba", // Account 5
	"92db14e403b83dfe3df233f83dfa3a0d7096f21ca9b0d6d6b8d88b2b4ec1564e", // Account 6
	"4bbbf85ce3377467afe5d46f804f221813b2bb87f24d81f60f1fcdbf7cbf4356", // Account 7
	"dbda1821b80551c9d65939329250298aa3472ba22feea921c0cf5d620ea67b97", // Account 8
	"2a871d0798f97d79848a013d4936a73bf4cc922c825d33c1cf7073dff6d409c6", // Account 9
}

// LoadTestAccounts loads the standard test accounts.
func LoadTestAccounts() ([]*Account, error) {
	accounts := make([]*Account, 0, len(TestPrivateKeys))
	for _, hexKey := range TestPrivateKeys {
		account, err := NewAccountFromHex(hexKey)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}
