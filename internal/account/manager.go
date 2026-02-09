package account

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// Manager manages test accounts including generation, funding, and nonce tracking.
type Manager struct {
	// Built-in test accounts (10 hardcoded)
	accounts []*Account

	// Dynamic accounts for realistic test (generated on demand)
	dynamicAccounts []*Account
	accountsFunded  int32 // atomic

	chainID   *big.Int
	gasPrice  *big.Int
	useLegacy bool // Use legacy (type 0) transactions instead of EIP-1559
	logger    *slog.Logger
}

// NewManager creates a new account manager.
// useLegacy controls whether funding transactions use legacy (type 0) format.
func NewManager(chainID, gasPrice *big.Int, useLegacy bool, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	accounts, err := LoadTestAccounts()
	if err != nil {
		return nil, fmt.Errorf("failed to load test accounts: %w", err)
	}

	return &Manager{
		accounts:  accounts,
		chainID:   chainID,
		gasPrice:  gasPrice,
		useLegacy: useLegacy,
		logger:    logger,
	}, nil
}

// GetAccounts returns the built-in test accounts.
func (m *Manager) GetAccounts() []*Account {
	return m.accounts
}

// GetDynamicAccounts returns the dynamically generated accounts.
func (m *Manager) GetDynamicAccounts() []*Account {
	return m.dynamicAccounts
}

// GetAccountsFunded returns the number of funded dynamic accounts.
func (m *Manager) GetAccountsFunded() int {
	return int(atomic.LoadInt32(&m.accountsFunded))
}

// InitializeNonces fetches initial nonces for the built-in accounts in parallel.
// CRITICAL: Uses Resync to sync through builder (eth_getPendingNonce), ensuring
// load generator and builder have the same nonce view. This prevents "nonce ahead"
// errors when builder has cached nonces from previous tests.
func (m *Manager) InitializeNonces(ctx context.Context, client rpc.Client, numAccounts int) error {
	m.logger.Info("Initializing account nonces (parallel, through builder)...", slog.Int("count", numAccounts))

	count := numAccounts
	if count > len(m.accounts) {
		count = len(m.accounts)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, count)
	sem := make(chan struct{}, 16) // Limit concurrent RPC calls

	for i := range count {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			account := m.accounts[idx]
			if err := account.Resync(ctx, client); err != nil {
				select {
				case errChan <- fmt.Errorf("account %d: %w", idx, err):
				default:
				}
				return
			}
			m.logger.Debug("Account nonce initialized",
				slog.Int("account_idx", idx),
				slog.String("address", account.Address.Hex()[:10]),
				slog.Uint64("nonce", account.PeekNonce()),
			)
		}(i)
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		return err
	}

	m.logger.Info("Account nonces initialized", slog.Int("count", count))
	return nil
}

// InitializeDynamicNonces fetches initial nonces for dynamic accounts in parallel.
// CRITICAL: Uses Resync to sync through builder (eth_getPendingNonce), which also
// populates the builder's nonce cache for these new accounts. This prevents cache
// misses and RPC delays when the test starts.
func (m *Manager) InitializeDynamicNonces(ctx context.Context, client rpc.Client) error {
	count := len(m.dynamicAccounts)
	m.logger.Info("Initializing nonces for dynamic accounts (parallel, through builder)",
		slog.Int("count", count),
	)

	if count == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errChan := make(chan error, count)
	sem := make(chan struct{}, 32) // Higher concurrency for dynamic accounts
	var initialized int32

	for i, acc := range m.dynamicAccounts {
		wg.Add(1)
		go func(idx int, acc *Account) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			if err := acc.Resync(ctx, client); err != nil {
				select {
				case errChan <- fmt.Errorf("dynamic account %d: %w", idx, err):
				default:
				}
				return
			}
			n := atomic.AddInt32(&initialized, 1)
			if n <= 5 || n%100 == 0 {
				m.logger.Debug("Dynamic account nonce initialized",
					slog.Int("account_idx", idx),
					slog.String("address", acc.Address.Hex()[:10]),
					slog.Uint64("nonce", acc.PeekNonce()),
				)
			}
		}(i, acc)
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		return err
	}

	m.logger.Info("Dynamic account nonces initialized", slog.Int("count", count))
	return nil
}

// GenerateDynamicAccounts creates N random accounts for realistic testing.
// Uses parallel key generation for better performance.
func (m *Manager) GenerateDynamicAccounts(count int) error {
	m.dynamicAccounts = make([]*Account, count)
	atomic.StoreInt32(&m.accountsFunded, 0)

	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > 16 {
		numWorkers = 16 // Diminishing returns beyond this
	}

	m.logger.Info("Generating dynamic accounts",
		slog.Int("count", count),
		slog.Int("workers", numWorkers),
	)

	var wg sync.WaitGroup
	errChan := make(chan error, numWorkers)
	workSize := (count + numWorkers - 1) / numWorkers

	for w := 0; w < numWorkers; w++ {
		start := w * workSize
		end := start + workSize
		if end > count {
			end = count
		}
		if start >= count {
			break
		}

		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				privateKey, err := crypto.GenerateKey()
				if err != nil {
					select {
					case errChan <- fmt.Errorf("key %d: %w", i, err):
					default:
					}
					return
				}
				m.dynamicAccounts[i] = NewAccount(privateKey)
			}
		}(start, end)
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		return err
	}

	m.logger.Info("Generated dynamic accounts", slog.Int("count", count))
	return nil
}

// FundDynamicAccounts funds all dynamic accounts using faucets in parallel.
// Reserves accounts[0] for contract deployment (not used as faucet).
// Each faucet sends transactions rapidly (no delays). The builder handles nonce ordering.
// sendClient is used for sending transactions (should be builder client).
// syncClient is used for nonce sync (should be L2 client for confirmed chain state).
// IMPORTANT: This function now waits for all funding transactions to be confirmed before returning.
func (m *Manager) FundDynamicAccounts(ctx context.Context, sendClient, syncClient rpc.Client) error {
	if len(m.dynamicAccounts) == 0 {
		return nil
	}

	fundAmount, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1,000 ETH per account (reduced from 10k for efficiency)
	signer := types.LatestSignerForChainID(m.chainID)

	// Use accounts[1..9] as faucets (9 faucets), reserve accounts[0] for deployment
	numFaucets := len(m.accounts) - 1 // 9 faucets
	if numFaucets < 1 {
		numFaucets = 1
	}

	m.logger.Info("Funding dynamic accounts (faucets, with confirmation wait)",
		slog.Int("count", len(m.dynamicAccounts)),
		slog.Int("faucets", numFaucets),
		slog.String("note", "accounts[0] reserved for deployment"),
	)

	// CRITICAL: Sync faucet nonces THROUGH the builder (not directly from chain!)
	// This ensures load generator and builder have the same nonce view, preventing
	// "nonce ahead" errors when builder has cached nonces from previous tests.
	// Using sendClient (builder) queries eth_getPendingNonce which either:
	// - Returns cached nonce (ensuring we match builder's expectation)
	// - Or queries chain, caches result, and returns (ensuring we're in sync)
	startingNonces := make([]uint64, numFaucets)
	for i := 0; i < numFaucets; i++ {
		faucet := m.accounts[i+1]
		if err := faucet.Resync(ctx, sendClient); err != nil {
			return fmt.Errorf("resync faucet %d from builder: %w", i+1, err)
		}
		startingNonces[i] = faucet.PeekNonce()
	}

	// Calculate how many txs each faucet will send
	txsPerFaucet := make([]int, numFaucets)
	for i := 0; i < len(m.dynamicAccounts); i++ {
		faucetIdx := i % numFaucets
		txsPerFaucet[faucetIdx]++
	}

	// Distribute accounts among faucets and fund in parallel - NO DELAYS
	// Faucets are accounts[1..numFaucets] (indices 1-9)
	var wg sync.WaitGroup
	for faucetIdx := 0; faucetIdx < numFaucets; faucetIdx++ {
		wg.Add(1)
		go func(fIdx int) {
			defer wg.Done()
			faucet := m.accounts[fIdx+1] // +1 to skip accounts[0]

			// Fund every numFaucets-th account (round robin)
			for i := fIdx; i < len(m.dynamicAccounts); i += numFaucets {
				acc := m.dynamicAccounts[i]

				// Simple send - no retry, no delay
				if err := m.fundAccountFast(ctx, sendClient, faucet, acc, signer, fundAmount); err != nil {
					m.logger.Warn("Failed to fund account",
						slog.Int("idx", i),
						slog.Int("faucet", fIdx+1),
						slog.String("err", err.Error()))
					continue
				}

				funded := atomic.AddInt32(&m.accountsFunded, 1)
				if funded%500 == 0 {
					m.logger.Info("Funding progress (sent)",
						slog.Int("sent", int(funded)),
						slog.Int("total", len(m.dynamicAccounts)),
					)
				}
			}
		}(faucetIdx)
	}

	wg.Wait()

	m.logger.Info("All funding transactions sent, waiting for confirmation...",
		slog.Int("sent", int(atomic.LoadInt32(&m.accountsFunded))),
	)

	// CRITICAL: Wait for all funding transactions to be confirmed
	// Check each faucet's chain nonce until it matches what we sent
	confirmTimeout := 2 * time.Minute // Allow up to 2 minutes for confirmation
	var confirmWg sync.WaitGroup
	confirmErrors := make(chan error, numFaucets)

	for faucetIdx := 0; faucetIdx < numFaucets; faucetIdx++ {
		confirmWg.Add(1)
		go func(fIdx int) {
			defer confirmWg.Done()
			faucet := m.accounts[fIdx+1]
			expectedNonce := startingNonces[fIdx] + uint64(txsPerFaucet[fIdx])

			if err := m.waitForNonceConfirmation(ctx, syncClient, faucet, expectedNonce, confirmTimeout); err != nil {
				m.logger.Warn("Faucet confirmation timeout",
					slog.Int("faucet", fIdx+1),
					slog.Uint64("expected", expectedNonce),
					slog.String("err", err.Error()))
				select {
				case confirmErrors <- fmt.Errorf("faucet %d: %w", fIdx+1, err):
				default:
				}
			} else {
				m.logger.Debug("Faucet funding confirmed",
					slog.Int("faucet", fIdx+1),
					slog.Uint64("nonce", expectedNonce))
			}
		}(faucetIdx)
	}

	confirmWg.Wait()
	close(confirmErrors)

	// Check if any confirmations failed
	if err := <-confirmErrors; err != nil {
		m.logger.Warn("Some faucet confirmations failed", slog.String("firstErr", err.Error()))
		// Continue anyway - some accounts were funded
	}

	m.logger.Info("Account funding complete and confirmed",
		slog.Int("funded", int(atomic.LoadInt32(&m.accountsFunded))),
	)
	return nil
}

// waitForNonceConfirmation waits until the faucet's nonce reaches the expected value.
// Uses GetConfirmedNonce (eth_getTransactionCount with "latest") to check ACTUAL on-chain confirmation.
// IMPORTANT: Do NOT use GetNonce here - it goes through eth_getPendingNonce which returns
// the builder's view, not the actual confirmed chain state.
func (m *Manager) waitForNonceConfirmation(ctx context.Context, client rpc.Client, faucet *Account, expectedNonce uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for time.Now().Before(deadline) {
		// CRITICAL: Use GetConfirmedNonce to check ACTUAL on-chain state
		// GetNonce uses eth_getPendingNonce which returns builder's cached view,
		// not the confirmed chain state. This caused false positives where we thought
		// TXs were confirmed but they were only queued in the builder.
		onChainNonce, err := client.GetConfirmedNonce(ctx, faucet.Address.Hex())
		if err != nil {
			return fmt.Errorf("get confirmed nonce: %w", err)
		}

		if onChainNonce >= expectedNonce {
			return nil // All transactions confirmed
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return fmt.Errorf("timeout waiting for nonce confirmation")
}

// Legacy parallel funding code - no longer used but kept for reference
func (m *Manager) fundDynamicAccountsParallel(ctx context.Context, client rpc.Client) error {
	if len(m.dynamicAccounts) == 0 {
		return nil
	}

	fundAmount, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1,000 ETH
	signer := types.LatestSignerForChainID(m.chainID)
	numFaucets := 1
	delayPerBatch := 50 * time.Millisecond
	batchSize := 1

	for i := range numFaucets {
		if err := m.accounts[i].Resync(ctx, client); err != nil {
			return fmt.Errorf("resync faucet %d: %w", i, err)
		}
	}

	var wg sync.WaitGroup
	errChan := make(chan error, numFaucets)

	for faucetIdx := range numFaucets {
		wg.Add(1)
		go func(fIdx int) {
			defer wg.Done()
			faucet := m.accounts[fIdx]
			txInBatch := 0

			for i := fIdx; i < len(m.dynamicAccounts); i += numFaucets {
				acc := m.dynamicAccounts[i]
				if err := m.fundAccount(ctx, client, faucet, acc, signer, fundAmount); err != nil {
					m.logger.Warn("Failed to fund account",
						slog.Int("idx", i),
						slog.Int("faucet", fIdx),
						slog.String("err", err.Error()))
					continue
				}
				funded := atomic.AddInt32(&m.accountsFunded, 1)
				txInBatch++

				if funded%100 == 0 {
					m.logger.Info("Funding progress",
						slog.Int("funded", int(funded)),
						slog.Int("total", len(m.dynamicAccounts)),
					)
				}

				if txInBatch >= batchSize {
					txInBatch = 0
					select {
					case <-ctx.Done():
						errChan <- ctx.Err()
						return
					case <-time.After(delayPerBatch):
					}
				}
			}
		}(faucetIdx)
	}

	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	m.logger.Info("Account funding complete",
		slog.Int("funded", int(atomic.LoadInt32(&m.accountsFunded))),
	)
	return nil
}

// fundAccountFast funds a single account - no retries, no delays.
// Just reserve nonce, sign, send, commit. Trust the builder.
func (m *Manager) fundAccountFast(
	ctx context.Context,
	client rpc.Client,
	faucet, recipient *Account,
	signer types.Signer,
	amount *big.Int,
) error {
	// Use high tip (1000 gwei) to ensure funding txs are included before test txs
	fundingTip := big.NewInt(1000 * 1e9) // 1000 gwei

	n := faucet.ReserveNonce()

	var tx *types.Transaction
	if m.useLegacy {
		tx = types.NewTx(&types.LegacyTx{
			Nonce:    n.Value(),
			GasPrice: new(big.Int).Add(m.gasPrice, fundingTip),
			Gas:      21000,
			To:       &recipient.Address,
			Value:    amount,
		})
	} else {
		tx = types.NewTx(&types.DynamicFeeTx{
			ChainID:   m.chainID,
			Nonce:     n.Value(),
			GasTipCap: fundingTip,
			GasFeeCap: new(big.Int).Add(m.gasPrice, fundingTip),
			Gas:       21000,
			To:        &recipient.Address,
			Value:     amount,
		})
	}

	signed, err := types.SignTx(tx, signer, faucet.PrivateKey)
	if err != nil {
		n.Rollback()
		return fmt.Errorf("sign: %w", err)
	}

	data, err := signed.MarshalBinary()
	if err != nil {
		n.Rollback()
		return fmt.Errorf("encode: %w", err)
	}

	err = client.SendRawTransaction(ctx, data)
	if err != nil {
		n.Rollback()
		return fmt.Errorf("send: %w", err)
	}

	n.Commit()
	return nil
}

// fundAccount funds a single account with retry logic and proper nonce rollback.
// On recovery mode errors, it resyncs the faucet's nonce from chain before retrying.
func (m *Manager) fundAccount(
	ctx context.Context,
	client rpc.Client,
	faucet, recipient *Account,
	signer types.Signer,
	amount *big.Int,
) error {
	// Use high tip (1000 gwei) to ensure funding txs are included before test txs
	// This prevents race condition where test txs arrive before accounts are funded
	fundingTip := big.NewInt(1000 * 1e9) // 1000 gwei

	// Retry loop with exponential backoff for recovery mode errors
	maxRetries := 5
	baseDelay := 2 * time.Second // Start with longer delay to let builder recover

	for attempt := range maxRetries {
		n := faucet.ReserveNonce()

		var tx *types.Transaction
		if m.useLegacy {
			tx = types.NewTx(&types.LegacyTx{
				Nonce:    n.Value(),
				GasPrice: new(big.Int).Add(m.gasPrice, fundingTip),
				Gas:      21000,
				To:       &recipient.Address,
				Value:    amount,
			})
		} else {
			tx = types.NewTx(&types.DynamicFeeTx{
				ChainID:   m.chainID,
				Nonce:     n.Value(),
				GasTipCap: fundingTip,
				GasFeeCap: new(big.Int).Add(m.gasPrice, fundingTip),
				Gas:       21000,
				To:        &recipient.Address,
				Value:     amount,
			})
		}

		signed, err := types.SignTx(tx, signer, faucet.PrivateKey)
		if err != nil {
			n.Rollback()
			return fmt.Errorf("sign: %w", err)
		}

		data, err := signed.MarshalBinary()
		if err != nil {
			n.Rollback()
			return fmt.Errorf("encode: %w", err)
		}

		err = client.SendRawTransaction(ctx, data)
		if err == nil {
			n.Commit() // Success
			return nil
		}

		n.Rollback()

		// Check if it's a recoverable error (server in recovery mode)
		errStr := err.Error()
		if strings.Contains(errStr, "recovery mode") || strings.Contains(errStr, "under stress") {
			// Exponential backoff: 2s, 4s, 8s, 16s, 32s
			delay := baseDelay * time.Duration(1<<attempt)
			delay = min(delay, 30*time.Second)

			m.logger.Debug("builder in recovery mode, waiting before retry",
				slog.Int("attempt", attempt+1),
				slog.Duration("delay", delay),
			)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}

			// Resync faucet nonce from chain to ensure consistency
			if syncErr := faucet.Resync(ctx, client); syncErr != nil {
				m.logger.Warn("failed to resync faucet nonce", slog.String("err", syncErr.Error()))
			}
			continue
		}

		// Non-recoverable error
		return fmt.Errorf("send: %w", err)
	}

	return fmt.Errorf("max retries exceeded for funding")
}

// AccountKeyPair holds an address and its hex-encoded private key for cache persistence.
type AccountKeyPair struct {
	Address       string
	PrivateKeyHex string
}

// SetDynamicAccounts sets pre-loaded dynamic accounts from cache.
func (m *Manager) SetDynamicAccounts(accounts []*Account) {
	m.dynamicAccounts = accounts
	atomic.StoreInt32(&m.accountsFunded, int32(len(accounts)))
}

// ValidateBalances checks balances of accounts in parallel and splits them into
// funded (>= minBalance) and unfunded groups.
func (m *Manager) ValidateBalances(ctx context.Context, client rpc.Client, accounts []*Account, minBalance *big.Int) (funded, unfunded []*Account) {
	if len(accounts) == 0 {
		return nil, nil
	}

	type result struct {
		idx    int
		funded bool
	}

	results := make([]result, len(accounts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32) // Limit concurrent RPC calls

	for i, acc := range accounts {
		wg.Add(1)
		go func(idx int, acc *Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			balance, err := client.GetBalance(ctx, acc.Address.Hex())
			if err != nil {
				m.logger.Debug("balance check failed",
					slog.Int("idx", idx),
					slog.String("err", err.Error()))
				results[idx] = result{idx: idx, funded: false}
				return
			}
			results[idx] = result{idx: idx, funded: balance.Cmp(minBalance) >= 0}
		}(i, acc)
	}
	wg.Wait()

	for i, r := range results {
		if r.funded {
			funded = append(funded, accounts[i])
		} else {
			unfunded = append(unfunded, accounts[i])
		}
	}
	return funded, unfunded
}

// FundAccounts funds an arbitrary slice of accounts using faucets[1..9].
// sendClient is used for sending transactions, syncClient for nonce confirmation.
func (m *Manager) FundAccounts(ctx context.Context, sendClient, syncClient rpc.Client, accounts []*Account) error {
	if len(accounts) == 0 {
		return nil
	}

	fundAmount, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1,000 ETH
	signer := types.LatestSignerForChainID(m.chainID)

	numFaucets := len(m.accounts) - 1 // accounts[1..9]
	if numFaucets < 1 {
		numFaucets = 1
	}

	m.logger.Info("Funding accounts subset",
		slog.Int("count", len(accounts)),
		slog.Int("faucets", numFaucets),
	)

	// Sync faucet nonces through builder
	startingNonces := make([]uint64, numFaucets)
	for i := 0; i < numFaucets; i++ {
		faucet := m.accounts[i+1]
		if err := faucet.Resync(ctx, sendClient); err != nil {
			return fmt.Errorf("resync faucet %d from builder: %w", i+1, err)
		}
		startingNonces[i] = faucet.PeekNonce()
	}

	txsPerFaucet := make([]int, numFaucets)
	for i := range accounts {
		txsPerFaucet[i%numFaucets]++
	}

	var wg sync.WaitGroup
	var fundedCount atomic.Int32
	for faucetIdx := 0; faucetIdx < numFaucets; faucetIdx++ {
		wg.Add(1)
		go func(fIdx int) {
			defer wg.Done()
			faucet := m.accounts[fIdx+1]

			for i := fIdx; i < len(accounts); i += numFaucets {
				acc := accounts[i]
				if err := m.fundAccountFast(ctx, sendClient, faucet, acc, signer, fundAmount); err != nil {
					m.logger.Warn("Failed to fund account",
						slog.Int("idx", i),
						slog.Int("faucet", fIdx+1),
						slog.String("err", err.Error()))
					continue
				}
				fundedCount.Add(1)
			}
		}(faucetIdx)
	}
	wg.Wait()

	m.logger.Info("Funding TXs sent, waiting for confirmation...",
		slog.Int("sent", int(fundedCount.Load())))

	// Wait for confirmations
	confirmTimeout := 2 * time.Minute
	var confirmWg sync.WaitGroup
	confirmErrors := make(chan error, numFaucets)

	for faucetIdx := 0; faucetIdx < numFaucets; faucetIdx++ {
		confirmWg.Add(1)
		go func(fIdx int) {
			defer confirmWg.Done()
			faucet := m.accounts[fIdx+1]
			expectedNonce := startingNonces[fIdx] + uint64(txsPerFaucet[fIdx])

			if err := m.waitForNonceConfirmation(ctx, syncClient, faucet, expectedNonce, confirmTimeout); err != nil {
				select {
				case confirmErrors <- fmt.Errorf("faucet %d: %w", fIdx+1, err):
				default:
				}
			}
		}(faucetIdx)
	}
	confirmWg.Wait()
	close(confirmErrors)

	if err := <-confirmErrors; err != nil {
		m.logger.Warn("Some faucet confirmations failed", slog.String("firstErr", err.Error()))
	}

	m.logger.Info("Subset funding complete", slog.Int("funded", int(fundedCount.Load())))
	return nil
}

// ExportDynamicAccountKeys returns address + hex private key pairs for all dynamic accounts.
func (m *Manager) ExportDynamicAccountKeys() []AccountKeyPair {
	pairs := make([]AccountKeyPair, len(m.dynamicAccounts))
	for i, acc := range m.dynamicAccounts {
		pairs[i] = AccountKeyPair{
			Address:       acc.Address.Hex(),
			PrivateKeyHex: hex.EncodeToString(crypto.FromECDSA(acc.PrivateKey)),
		}
	}
	return pairs
}

// Reset clears dynamic accounts.
func (m *Manager) Reset() {
	m.dynamicAccounts = nil
	atomic.StoreInt32(&m.accountsFunded, 0)
}

// RecycleFunds sends funds from dynamic accounts back to faucets.
// This allows running multiple tests without restarting the system.
// Returns the number of accounts successfully recycled.
func (m *Manager) RecycleFunds(ctx context.Context, client rpc.Client) (int, error) {
	if len(m.dynamicAccounts) == 0 {
		m.logger.Info("no dynamic accounts to recycle")
		return 0, nil
	}

	signer := types.LatestSignerForChainID(m.chainID)
	numFaucets := len(m.accounts) - 1 // accounts[1..9] are faucets
	if numFaucets < 1 {
		numFaucets = 1
	}

	m.logger.Info("recycling funds from dynamic accounts",
		slog.Int("count", len(m.dynamicAccounts)),
		slog.Int("faucets", numFaucets),
	)

	// Recycle in parallel using goroutines per faucet
	var wg sync.WaitGroup
	var recycledCount atomic.Int32
	var mu sync.Mutex
	var errors []error

	for fIdx := range numFaucets {
		wg.Add(1)
		go func(faucetIdx int) {
			defer wg.Done()

			faucet := m.accounts[faucetIdx+1] // accounts[1..9]

			// Process accounts assigned to this faucet
			for i := faucetIdx; i < len(m.dynamicAccounts); i += numFaucets {
				acc := m.dynamicAccounts[i]

				if err := m.recycleAccountFunds(ctx, client, acc, faucet, signer); err != nil {
					m.logger.Debug("failed to recycle from account",
						slog.Int("idx", i),
						slog.String("err", err.Error()),
					)
					mu.Lock()
					errors = append(errors, err)
					mu.Unlock()
					continue
				}
				recycledCount.Add(1)
			}
		}(fIdx)
	}

	wg.Wait()

	count := int(recycledCount.Load())
	m.logger.Info("fund recycling complete",
		slog.Int("recycled", count),
		slog.Int("failed", len(errors)),
	)

	if len(errors) > 0 {
		return count, fmt.Errorf("recycling had %d errors (first: %w)", len(errors), errors[0])
	}
	return count, nil
}

// recycleAccountFunds sends remaining balance from a dynamic account back to a faucet.
func (m *Manager) recycleAccountFunds(
	ctx context.Context,
	client rpc.Client,
	from, to *Account,
	signer types.Signer,
) error {
	// Get current balance
	balance, err := client.GetBalance(ctx, from.Address.Hex())
	if err != nil {
		return fmt.Errorf("get balance: %w", err)
	}

	// Calculate amount to send (balance - gas cost)
	gasLimit := uint64(21000)
	gasTip := big.NewInt(1 * 1e9)   // 1 gwei tip
	maxFee := big.NewInt(10 * 1e9)  // 10 gwei max fee
	gasCost := new(big.Int).Mul(maxFee, big.NewInt(int64(gasLimit)))

	// Skip if balance is too low to cover gas
	if balance.Cmp(gasCost) <= 0 {
		return nil // Nothing to recycle
	}

	sendAmount := new(big.Int).Sub(balance, gasCost)

	// Skip very small amounts
	minAmount := big.NewInt(1e16) // 0.01 ETH minimum
	if sendAmount.Cmp(minAmount) < 0 {
		return nil
	}

	// Sync local nonce state with chain before sending
	if err := from.Resync(ctx, client); err != nil {
		return fmt.Errorf("resync nonce: %w", err)
	}

	n := from.ReserveNonce()
	defer n.Rollback() // Auto-rollback on any error

	var tx *types.Transaction
	if m.useLegacy {
		tx = types.NewTx(&types.LegacyTx{
			Nonce:    n.Value(),
			GasPrice: maxFee,
			Gas:      gasLimit,
			To:       &to.Address,
			Value:    sendAmount,
		})
	} else {
		tx = types.NewTx(&types.DynamicFeeTx{
			ChainID:   m.chainID,
			Nonce:     n.Value(),
			GasTipCap: gasTip,
			GasFeeCap: maxFee,
			Gas:       gasLimit,
			To:        &to.Address,
			Value:     sendAmount,
		})
	}

	signed, err := types.SignTx(tx, signer, from.PrivateKey)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	data, err := signed.MarshalBinary()
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	if err := client.SendRawTransaction(ctx, data); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	n.Commit() // Success - prevent rollback
	return nil
}

// Rand provides thread-safe random number generation using math/rand/v2.
// This is used for tip generation and transaction type selection.
// Go 1.22's math/rand/v2 is automatically seeded and goroutine-safe.
type Rand struct{}

// Float64 returns a random float64 in [0, 1).
func (r *Rand) Float64() float64 {
	return rand.Float64()
}

// IntN returns a random int in [0, n).
func (r *Rand) IntN(n int) int {
	return rand.IntN(n)
}

// Intn is an alias for IntN (for compatibility).
func (r *Rand) Intn(n int) int {
	return rand.IntN(n)
}

// NewRand returns a thread-safe random number generator.
// Uses Go 1.22's math/rand/v2 which is automatically seeded and concurrent-safe.
func NewRand() *Rand {
	return &Rand{}
}
