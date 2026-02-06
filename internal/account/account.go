// Package account manages test accounts for load generation.
package account

import (
	"context"
	"crypto/ecdsa"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// Account holds a test account's keys and state.
type Account struct {
	PrivateKey *ecdsa.PrivateKey
	Address    common.Address
	nonce      uint64
	mu         sync.Mutex
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
	nonce := a.nonce
	a.nonce++
	a.mu.Unlock()

	return &Nonce{
		value:   nonce,
		account: a,
	}
}

// rollback decrements nonce if it was the last one issued.
func (a *Account) rollback(nonce uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Only rollback if this was the most recent nonce
	// (prevents issues with out-of-order rollbacks)
	if a.nonce == nonce+1 {
		a.nonce = nonce
	}
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
	a.mu.Unlock()
	return nil
}

// SetNonce sets the nonce value directly.
// Prefer Resync for fetching from chain, or ReserveNonce for normal use.
func (a *Account) SetNonce(nonce uint64) {
	a.mu.Lock()
	a.nonce = nonce
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
