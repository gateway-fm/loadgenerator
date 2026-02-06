// Package integration provides integration tests using Anvil as a local test chain.
//
// These tests require Anvil to be installed and available in PATH.
// Run with: go test -tags=integration ./internal/integration/...
//
//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/contract"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
	"github.com/gateway-fm/loadgenerator/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// Anvil default chain ID
	anvilChainID = 31337

	// Anvil default funded account (first deterministic account)
	// Private key: 0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
	anvilFundedPrivKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

	// Test configuration
	testBlockTime = 1 // 1 second blocks for faster tests
)

// anvilInstance manages an Anvil process for testing.
type anvilInstance struct {
	cmd  *exec.Cmd
	port int
	url  string
}

// startAnvil starts an Anvil instance on a random port.
func startAnvil(t *testing.T) *anvilInstance {
	t.Helper()

	// Find a free port
	port := 8545 + (time.Now().UnixNano() % 1000)

	cmd := exec.Command("anvil",
		"--port", fmt.Sprintf("%d", port),
		"--block-time", fmt.Sprintf("%d", testBlockTime),
		"--silent",
	)

	// Capture stderr for debugging
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skip("Anvil not installed, skipping integration test")
		}
		t.Fatalf("Failed to start Anvil: %v", err)
	}

	instance := &anvilInstance{
		cmd:  cmd,
		port: int(port),
		url:  fmt.Sprintf("http://localhost:%d", port),
	}

	// Wait for Anvil to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			t.Fatalf("Anvil failed to start: %s", stderr.String())
		default:
			resp, err := http.Post(instance.url, "application/json",
				strings.NewReader(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`))
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return instance
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// stop stops the Anvil instance.
func (a *anvilInstance) stop() {
	if a.cmd != nil && a.cmd.Process != nil {
		a.cmd.Process.Kill()
		a.cmd.Wait()
	}
}

// TestRPCClient tests the RPC client against Anvil.
func TestRPCClient(t *testing.T) {
	anvil := startAnvil(t)
	defer anvil.stop()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := rpc.DefaultClientConfig(anvil.url)
	cfg.Logger = logger

	client := rpc.NewHTTPClient(cfg)
	ctx := context.Background()

	// Test GetBlockNumber
	t.Run("GetBlockNumber", func(t *testing.T) {
		blockNum, err := client.GetBlockNumber(ctx)
		if err != nil {
			t.Fatalf("GetBlockNumber failed: %v", err)
		}
		// Block number should be 0 or small on fresh Anvil
		if blockNum > 100 {
			t.Errorf("Unexpected block number: %d", blockNum)
		}
	})

	// Create test account from Anvil's prefunded account
	testAccount, err := account.NewAccountFromHex(anvilFundedPrivKey)
	if err != nil {
		t.Fatalf("Failed to create test account: %v", err)
	}

	// Test GetNonce
	t.Run("GetNonce", func(t *testing.T) {
		nonce, err := client.GetNonce(ctx, testAccount.Address.Hex())
		if err != nil {
			t.Fatalf("GetNonce failed: %v", err)
		}
		// Fresh account should have nonce 0
		if nonce != 0 {
			t.Errorf("Expected nonce 0, got %d", nonce)
		}
	})
}

// TestAccountGeneration tests account generation.
func TestAccountGeneration(t *testing.T) {
	chainID := big.NewInt(anvilChainID)
	gasPrice := big.NewInt(1e9) // 1 gwei
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Create account manager
	manager, err := account.NewManager(chainID, gasPrice, logger)
	if err != nil {
		t.Fatalf("Failed to create account manager: %v", err)
	}

	// Generate dynamic accounts
	numAccounts := 5
	if err := manager.GenerateDynamicAccounts(numAccounts); err != nil {
		t.Fatalf("Failed to generate accounts: %v", err)
	}

	accounts := manager.GetDynamicAccounts()
	if len(accounts) != numAccounts {
		t.Fatalf("Expected %d accounts, got %d", numAccounts, len(accounts))
	}

	// Verify each account has unique address
	seen := make(map[common.Address]bool)
	for i, acc := range accounts {
		if seen[acc.Address] {
			t.Errorf("Duplicate address for account %d", i)
		}
		seen[acc.Address] = true
	}
}

// TestContractDeployment tests contract deployment.
func TestContractDeployment(t *testing.T) {
	anvil := startAnvil(t)
	defer anvil.stop()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := rpc.DefaultClientConfig(anvil.url)
	cfg.Logger = logger

	client := rpc.NewHTTPClient(cfg)
	ctx := context.Background()

	chainID := big.NewInt(anvilChainID)
	gasPrice := big.NewInt(1e9)

	// Create deployer account from Anvil's prefunded account
	deployerAccount, err := account.NewAccountFromHex(anvilFundedPrivKey)
	if err != nil {
		t.Fatalf("Failed to create deployer account: %v", err)
	}

	// Get current nonce
	nonce, err := client.GetNonce(ctx, deployerAccount.Address.Hex())
	if err != nil {
		t.Fatalf("Failed to get nonce: %v", err)
	}
	deployerAccount.SetNonce(nonce)

	// Create contract deployer
	deployer := contract.NewDeployer(client, chainID, gasPrice, logger)

	// Deploy contracts
	addresses, err := deployer.DeployAll(ctx, deployerAccount)
	if err != nil {
		t.Fatalf("Failed to deploy contracts: %v", err)
	}

	// Verify all contracts were deployed
	expectedContracts := []string{"ERC20", "GasConsumer"}
	for _, name := range expectedContracts {
		addr, ok := addresses[name]
		if !ok {
			t.Errorf("Contract %s not found in deployed addresses", name)
			continue
		}
		if addr == (common.Address{}) {
			t.Errorf("Contract %s has zero address", name)
		}
	}
}

// TestTransactionBuilding tests transaction builders.
func TestTransactionBuilding(t *testing.T) {
	anvil := startAnvil(t)
	defer anvil.stop()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := rpc.DefaultClientConfig(anvil.url)
	cfg.Logger = logger

	client := rpc.NewHTTPClient(cfg)
	ctx := context.Background()

	chainID := big.NewInt(anvilChainID)

	// Create test account from Anvil's prefunded account
	senderAccount, err := account.NewAccountFromHex(anvilFundedPrivKey)
	if err != nil {
		t.Fatalf("Failed to create sender account: %v", err)
	}

	// Get current nonce
	nonce, err := client.GetNonce(ctx, senderAccount.Address.Hex())
	if err != nil {
		t.Fatalf("Failed to get nonce: %v", err)
	}
	senderAccount.SetNonce(nonce)

	// Create signer
	signer := ethtypes.LatestSignerForChainID(chainID)

	// Test ETH transfer builder
	t.Run("ETHTransfer", func(t *testing.T) {
		// Generate a random recipient
		recipientKey, _ := crypto.GenerateKey()
		recipient := crypto.PubkeyToAddress(recipientKey.PublicKey)

		builder := txbuilder.NewETHTransferBuilder(recipient)

		params := txbuilder.TxParams{
			ChainID:   chainID,
			Nonce:     senderAccount.GetNonce(),
			GasTipCap: big.NewInt(1e9),
			GasFeeCap: big.NewInt(2e9),
		}

		tx, err := builder.Build(params)
		if err != nil {
			t.Fatalf("Failed to build ETH transfer: %v", err)
		}

		signedTx, err := ethtypes.SignTx(tx, signer, senderAccount.PrivateKey)
		if err != nil {
			t.Fatalf("Failed to sign tx: %v", err)
		}

		txBytes, err := signedTx.MarshalBinary()
		if err != nil {
			t.Fatalf("Failed to marshal tx: %v", err)
		}

		if err := client.SendRawTransaction(ctx, txBytes); err != nil {
			t.Fatalf("Failed to send ETH transfer: %v", err)
		}

		// Wait for confirmation
		time.Sleep(2 * time.Second)

		t.Log("ETH transfer sent successfully")
	})
}

// TestMetricsCollection tests the metrics collector.
func TestMetricsCollection(t *testing.T) {
	collector := metrics.NewMemoryCollector()

	// Record some transactions
	now := time.Now()
	for i := 0; i < 100; i++ {
		hash := common.BigToHash(big.NewInt(int64(i)))
		sendTime := now.Add(time.Duration(i) * time.Millisecond)
		collector.RecordTxSent(hash, sendTime)
		collector.IncTxSent()

		// Confirm with varying latencies
		confirmTime := sendTime.Add(time.Duration(50+i) * time.Millisecond)
		collector.RecordTxConfirmed(hash, confirmTime)
	}

	// Check counters
	if collector.GetTxSent() != 100 {
		t.Errorf("Expected 100 sent, got %d", collector.GetTxSent())
	}
	if collector.GetTxConfirmed() != 100 {
		t.Errorf("Expected 100 confirmed, got %d", collector.GetTxConfirmed())
	}

	// Check latency stats
	stats := collector.GetLatencyStats()
	if stats == nil {
		t.Fatal("Expected latency stats, got nil")
	}
	if stats.Count != 100 {
		t.Errorf("Expected 100 samples, got %d", stats.Count)
	}
	if stats.Min <= 0 || stats.Max <= 0 {
		t.Error("Expected positive latency values")
	}
	if stats.P50 <= 0 || stats.P99 <= 0 {
		t.Error("Expected positive percentile values")
	}
}

// TestFullLoadTestSetup tests all components work together.
func TestFullLoadTestSetup(t *testing.T) {
	anvil := startAnvil(t)
	defer anvil.stop()

	// Create config
	cfg := &config.Config{
		BuilderRPCURL: anvil.url,
		L2RPCURL:      anvil.url,
		ChainID:       anvilChainID,
		GasPrice:      1e9,
		GasLimit:      21000,
		ListenAddr:    "127.0.0.1:0", // Random port
	}

	numAccounts := 3

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rpcCfg := rpc.DefaultClientConfig(cfg.BuilderRPCURL)
	rpcCfg.Logger = logger

	client := rpc.NewHTTPClient(rpcCfg)
	ctx := context.Background()

	chainID := big.NewInt(cfg.ChainID)
	gasPrice := big.NewInt(cfg.GasPrice)

	// Create account manager and generate accounts
	manager, err := account.NewManager(chainID, gasPrice, logger)
	if err != nil {
		t.Fatalf("Failed to create account manager: %v", err)
	}

	if err := manager.GenerateDynamicAccounts(numAccounts); err != nil {
		t.Fatalf("Failed to generate accounts: %v", err)
	}

	accounts := manager.GetDynamicAccounts()
	if len(accounts) != numAccounts {
		t.Fatalf("Expected %d accounts, got %d", numAccounts, len(accounts))
	}

	// Verify RPC connectivity
	_, err = client.GetBlockNumber(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to Anvil: %v", err)
	}

	t.Log("Full load test setup verified successfully")
}

// TestTransactionTypes verifies all transaction builders.
func TestTransactionTypes(t *testing.T) {
	chainID := big.NewInt(anvilChainID)

	// Generate test addresses
	recipientKey, _ := crypto.GenerateKey()
	recipient := crypto.PubkeyToAddress(recipientKey.PublicKey)

	testCases := []struct {
		name     string
		builder  txbuilder.Builder
		txType   types.TransactionType
		gasLimit uint64
	}{
		{
			name:     "ETHTransfer",
			builder:  txbuilder.NewETHTransferBuilder(recipient),
			txType:   types.TxTypeEthTransfer,
			gasLimit: 21000,
		},
		{
			name:     "ERC20Transfer",
			builder:  txbuilder.NewERC20TransferBuilder(recipient),
			txType:   types.TxTypeERC20Transfer,
			gasLimit: 65000,
		},
		{
			name:     "ERC20Approve",
			builder:  txbuilder.NewERC20ApproveBuilder(recipient),
			txType:   types.TxTypeERC20Approve,
			gasLimit: 46000,
		},
		{
			name:     "StorageWrite",
			builder:  txbuilder.NewStorageWriteBuilder(),
			txType:   types.TxTypeStorageWrite,
			gasLimit: 43000,
		},
		{
			name:     "HeavyCompute",
			builder:  txbuilder.NewHeavyComputeBuilder(),
			txType:   types.TxTypeHeavyCompute,
			gasLimit: 500000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.builder.Type() != tc.txType {
				t.Errorf("Expected type %s, got %s", tc.txType, tc.builder.Type())
			}

			if tc.builder.GasLimit() != tc.gasLimit {
				t.Errorf("Expected gas limit %d, got %d", tc.gasLimit, tc.builder.GasLimit())
			}

			params := txbuilder.TxParams{
				ChainID:   chainID,
				Nonce:     0,
				GasTipCap: big.NewInt(1e9),
				GasFeeCap: big.NewInt(2e9),
			}

			tx, err := tc.builder.Build(params)
			if err != nil {
				t.Fatalf("Failed to build transaction: %v", err)
			}

			if tx.Gas() != tc.gasLimit {
				t.Errorf("Transaction gas mismatch: expected %d, got %d", tc.gasLimit, tx.Gas())
			}
		})
	}
}

// BenchmarkTransactionSending benchmarks transaction sending throughput.
func BenchmarkTransactionSending(b *testing.B) {
	// Skip if Anvil is not available
	cmd := exec.Command("which", "anvil")
	if err := cmd.Run(); err != nil {
		b.Skip("Anvil not installed, skipping benchmark")
	}

	// Start Anvil
	anvilCmd := exec.Command("anvil", "--port", "18545", "--silent")
	if err := anvilCmd.Start(); err != nil {
		b.Fatalf("Failed to start Anvil: %v", err)
	}
	defer anvilCmd.Process.Kill()

	time.Sleep(2 * time.Second) // Wait for Anvil

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := rpc.DefaultClientConfig("http://localhost:18545")
	cfg.Logger = logger

	client := rpc.NewHTTPClient(cfg)
	ctx := context.Background()

	chainID := big.NewInt(anvilChainID)

	// Create sender from Anvil's prefunded account
	senderAccount, _ := account.NewAccountFromHex(anvilFundedPrivKey)

	nonce, _ := client.GetNonce(ctx, senderAccount.Address.Hex())
	senderAccount.SetNonce(nonce)

	recipientKey, _ := crypto.GenerateKey()
	recipient := crypto.PubkeyToAddress(recipientKey.PublicKey)

	builder := txbuilder.NewETHTransferBuilder(recipient)
	signer := ethtypes.LatestSignerForChainID(chainID)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		params := txbuilder.TxParams{
			ChainID:   chainID,
			Nonce:     senderAccount.GetNonce(),
			GasTipCap: big.NewInt(1e9),
			GasFeeCap: big.NewInt(2e9),
		}

		tx, _ := builder.Build(params)
		signedTx, _ := ethtypes.SignTx(tx, signer, senderAccount.PrivateKey)
		txBytes, _ := signedTx.MarshalBinary()
		client.SendRawTransaction(ctx, txBytes)
	}
}
