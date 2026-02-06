// Package contract handles contract deployment for load testing.
package contract

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
)

// DeploymentResult holds the result of a contract deployment.
type DeploymentResult struct {
	Name    string
	Address common.Address
	Err     error
}

// ProgressCallback is called after each contract deployment or skip.
type ProgressCallback func(contractName string, deployed, total int)

// Deployer handles contract deployment.
type Deployer struct {
	client   rpc.Client
	chainID  *big.Int
	gasPrice *big.Int
	logger   *slog.Logger
}

// NewDeployer creates a new contract deployer.
func NewDeployer(client rpc.Client, chainID, gasPrice *big.Int, logger *slog.Logger) *Deployer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deployer{
		client:   client,
		chainID:  chainID,
		gasPrice: gasPrice,
		logger:   logger,
	}
}

// DeployAll deploys all required contracts sequentially.
// Returns a map of contract name to address.
//
// NOTE: Contracts are deployed sequentially (not in parallel) to avoid nonce race conditions.
// When deploying in parallel, the second TX (nonce+1) can arrive at the builder before
// the first TX (nonce) is processed, causing nonce+1 to be rejected as a "future" transaction.
// Sequential deployment is only ~1-2s slower but is much more reliable.
func (d *Deployer) DeployAll(ctx context.Context, deployer *account.Account) (map[string]common.Address, error) {
	return d.DeployAllWithProgress(ctx, deployer, nil)
}

// DeployAllWithProgress deploys all required contracts sequentially with progress reporting.
// If onProgress is non-nil, it's called after each contract is deployed or skipped.
func (d *Deployer) DeployAllWithProgress(ctx context.Context, deployer *account.Account, onProgress ProgressCallback) (map[string]common.Address, error) {
	d.logger.Info("Deploying contracts sequentially...")

	addresses := make(map[string]common.Address)

	// Define contracts to deploy in order
	contracts := []struct {
		name     string
		bytecode []byte
	}{
		{"ERC20", txbuilder.ERC20Bytecode},
		{"GasConsumer", txbuilder.GasConsumerBytecode},
	}

	total := len(contracts)

	// Get the starting nonce to calculate expected addresses
	startNonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
	if err != nil {
		return addresses, fmt.Errorf("failed to fetch initial nonce: %w", err)
	}

	for i, contract := range contracts {
		// Calculate expected address for this contract
		expectedAddr := crypto.CreateAddress(deployer.Address, startNonce+uint64(i))

		// Check if contract already exists at expected address
		exists, err := d.checkContractExists(ctx, expectedAddr)
		if err != nil {
			d.logger.Warn("Failed to check contract existence, will deploy",
				slog.String("name", contract.name),
				slog.String("error", err.Error()),
			)
		} else if exists {
			d.logger.Info("Contract already deployed, skipping",
				slog.String("name", contract.name),
				slog.String("address", expectedAddr.Hex()),
			)
			addresses[contract.name] = expectedAddr
			if onProgress != nil {
				onProgress(contract.name, i+1, total)
			}
			continue
		}

		// Fetch fresh nonce for each deployment to avoid race conditions
		nonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
		if err != nil {
			return addresses, fmt.Errorf("failed to fetch nonce for %s: %w", contract.name, err)
		}

		addr, err := d.deployContract(ctx, deployer, contract.name, contract.bytecode, nonce)
		if err != nil {
			return addresses, fmt.Errorf("failed to deploy %s: %w", contract.name, err)
		}

		addresses[contract.name] = addr
		d.logger.Info("Contract deployed",
			slog.String("name", contract.name),
			slog.String("address", addr.Hex()),
		)

		if onProgress != nil {
			onProgress(contract.name, i+1, total)
		}
	}

	d.logger.Info("All contracts deployed successfully")
	return addresses, nil
}

// checkContractExists checks if a contract is deployed at the given address.
func (d *Deployer) checkContractExists(ctx context.Context, addr common.Address) (bool, error) {
	code, err := d.client.GetCode(ctx, addr.Hex())
	if err != nil {
		return false, err
	}
	return code != "" && code != "0x", nil
}

// deployContract deploys a single contract with retry logic and waits for confirmation.
func (d *Deployer) deployContract(ctx context.Context, deployer *account.Account, name string, bytecode []byte, nonce uint64) (common.Address, error) {
	// Calculate expected contract address
	contractAddr := crypto.CreateAddress(deployer.Address, nonce)

	// Build deployment transaction
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   d.chainID,
		Nonce:     nonce,
		GasTipCap: big.NewInt(0),
		GasFeeCap: d.gasPrice,
		Gas:       3000000, // High gas limit for deployment
		To:        nil,     // Contract creation
		Value:     big.NewInt(0),
		Data:      bytecode,
	})

	// Sign transaction
	signer := types.LatestSignerForChainID(d.chainID)
	signedTx, err := types.SignTx(tx, signer, deployer.PrivateKey)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to sign tx: %w", err)
	}

	// Send transaction
	rlp, err := signedTx.MarshalBinary()
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to marshal tx: %w", err)
	}

	// Retry loop for sending - handles recovery mode
	maxRetries := 10
	baseDelay := 2 * time.Second

	for attempt := range maxRetries {
		err = d.client.SendRawTransaction(ctx, rlp)
		if err == nil {
			break // Success
		}

		errStr := err.Error()
		if strings.Contains(errStr, "recovery mode") || strings.Contains(errStr, "under stress") {
			delay := baseDelay * time.Duration(1<<min(attempt, 4)) // Cap at 16x base delay
			delay = min(delay, 30*time.Second)

			d.logger.Info("Builder in recovery mode, waiting before retry",
				slog.String("contract", name),
				slog.Int("attempt", attempt+1),
				slog.Duration("delay", delay),
			)

			select {
			case <-ctx.Done():
				return common.Address{}, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}

		// Non-recoverable error
		return common.Address{}, fmt.Errorf("failed to send tx: %w", err)
	}

	if err != nil {
		return common.Address{}, fmt.Errorf("failed to send tx after retries: %w", err)
	}

	d.logger.Info("Deploying contract",
		slog.String("name", name),
		slog.String("expected_address", contractAddr.Hex()),
	)

	// Wait for deployment with exponential backoff
	return d.waitForDeployment(ctx, name, contractAddr)
}

// waitForDeployment waits for a contract to be deployed with exponential backoff.
func (d *Deployer) waitForDeployment(ctx context.Context, name string, contractAddr common.Address) (common.Address, error) {
	backoff := 200 * time.Millisecond
	maxBackoff := 2 * time.Second
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return common.Address{}, ctx.Err()
		case <-time.After(backoff):
		}

		code, err := d.client.GetCode(ctx, contractAddr.Hex())
		if err == nil && code != "" && code != "0x" {
			return contractAddr, nil
		}

		backoff = min(backoff*2, maxBackoff)
	}

	return common.Address{}, fmt.Errorf("timeout waiting for %s deployment", name)
}

// ValidateCachedContracts checks which cached contracts still have code on-chain.
// Returns valid contracts (name→address) and a list of invalid names that need redeployment.
func (d *Deployer) ValidateCachedContracts(ctx context.Context, cached map[string]string) (valid map[string]common.Address, invalid []string) {
	valid = make(map[string]common.Address)
	for name, addrHex := range cached {
		addr := common.HexToAddress(addrHex)
		exists, err := d.checkContractExists(ctx, addr)
		if err != nil {
			d.logger.Warn("Failed to validate cached contract",
				slog.String("name", name),
				slog.String("address", addrHex),
				slog.String("error", err.Error()),
			)
			invalid = append(invalid, name)
			continue
		}
		if !exists {
			d.logger.Info("Cached contract no longer exists",
				slog.String("name", name),
				slog.String("address", addrHex),
			)
			invalid = append(invalid, name)
			continue
		}
		valid[name] = addr
	}
	return valid, invalid
}

// Deploy deploys a single contract.
func (d *Deployer) Deploy(ctx context.Context, deployer *account.Account, name string, bytecode []byte) (common.Address, error) {
	nonce, err := d.client.GetNonce(ctx, deployer.Address.Hex())
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to fetch nonce: %w", err)
	}

	return d.deployContract(ctx, deployer, name, bytecode, nonce)
}
