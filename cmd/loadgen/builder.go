package main

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/storage"
)

func (lg *LoadGenerator) resyncAllNonces() {
	lg.logger.Info("resyncing all account nonces from builder...")

	accounts := lg.accountMgr.GetAccounts()
	resyncedCount := 0
	failedCount := 0
	fallbackCount := 0

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use builderClient to get pending nonces (via eth_getPendingNonce) - ensures sync with builder
	// Falls back to l2Client (chain confirmed) if builder is unavailable
	for _, acc := range accounts {
		// First try builder's view (preferred - stays in sync)
		if err := acc.Resync(ctx, lg.builderClient); err != nil {
			// Builder failed - fall back to chain confirmed nonce
			lg.logger.Debug("builder nonce failed, falling back to chain",
				"address", acc.Address.Hex()[:10],
				"error", err)
			if err := acc.ResyncFromChain(ctx, lg.l2Client); err != nil {
				lg.logger.Warn("failed to resync nonce for account",
					"address", acc.Address.Hex()[:10],
					"error", err)
				failedCount++
			} else {
				fallbackCount++
			}
		} else {
			resyncedCount++
		}
	}

	atomic.StoreInt32(&lg.nonceResyncNeeded, 0)
	lg.logger.Info("nonce resync complete",
		"resynced", resyncedCount,
		"fallback", fallbackCount,
		"failed", failedCount)
}

// resetBuilderNonces calls the block builder's /reset-nonces endpoint to clear
// its nonce cache and pending pool. This is critical before funding new accounts
// to prevent stale cached nonces from causing "nonce ahead" rejections.
// Skipped when no external block builder is used (e.g., cdk-erigon mode).
func (lg *LoadGenerator) resetBuilderNonces() error {
	if !lg.cfg.Capabilities.HasExternalBlockBuilder {
		lg.logger.Info("skipping builder nonce reset (no external block builder)")
		return nil
	}
	lg.logger.Info("resetting block builder nonce cache...")

	// Extract base URL from builder RPC URL (remove any path)
	baseURL := lg.cfg.BuilderRPCURL
	resetURL := baseURL + "/reset-nonces"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", resetURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to POST /reset-nonces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reset-nonces returned status %d", resp.StatusCode)
	}

	lg.logger.Info("block builder nonce cache reset successfully")
	return nil
}

// backpressureMonitor periodically fetches block builder status and updates pressure.
func (lg *LoadGenerator) backpressureMonitor() {
	defer lg.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-lg.ctx.Done():
			return
		case <-ticker.C:
			lg.fetchBuilderPressure()
		}
	}
}

// fetchBuilderPressure fetches block builder status via HTTP GET and updates pressure and gas metrics.
func (lg *LoadGenerator) fetchBuilderPressure() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Make HTTP GET request to /status endpoint (not JSON-RPC)
	statusURL := lg.cfg.BuilderRPCURL + "/status"
	req, err := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		lg.builderPressureMu.Lock()
		lg.builderPressure = 0
		lg.builderPressureMu.Unlock()
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		lg.builderPressureMu.Lock()
		lg.builderPressure = 0
		lg.builderPressureMu.Unlock()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		lg.builderPressureMu.Lock()
		lg.builderPressure = 0
		lg.builderPressureMu.Unlock()
		return
	}

	// Parse response to get pending count, max txs, and gas metrics
	var status struct {
		PendingTxCount    int64   `json:"pendingTxCount"`
		MaxTxsPerBlock    int64   `json:"maxTxsPerBlock"`
		PendingPoolSize   int64   `json:"pendingPoolSize"`
		LatestBaseFeeGwei float64 `json:"latestBaseFeeGwei"`
		LatestGasUsed     uint64  `json:"latestGasUsed"`
		// HSM / block attestation metadata (optional)
		BlockAttestationEnabled bool   `json:"blockAttestationEnabled"`
		HSMProvider             string `json:"hsmProvider"`
		HSMKeyIDActive          string `json:"hsmKeyIdActive"`
		HSMFailoverEnabled      bool   `json:"hsmFailoverEnabled"`
	}

	if err := stdjson.NewDecoder(resp.Body).Decode(&status); err != nil {
		return
	}

	// Calculate pressure as ratio of pending to capacity (2 * maxTxsPerBlock)
	capacity := status.MaxTxsPerBlock * 2
	if capacity <= 0 {
		capacity = 50000 // default
	}

	pressure := float64(status.PendingTxCount) / float64(capacity)
	if pressure > 1.0 {
		pressure = 1.0
	}

	// Also fetch gas metrics from L2 node (non-blocking, best-effort)
	// This is more reliable than block builder status for baseFee
	var gasPriceGwei, baseFeeGwei float64
	if lg.l2Client != nil {
		// Fetch eth_gasPrice (includes baseFee + suggested tip)
		if gasPrice, err := lg.l2Client.GetGasPrice(ctx); err == nil {
			gasPriceGwei = float64(gasPrice) / 1e9 // Convert wei to gwei
		}
		// Fetch baseFee directly from latest block
		if baseFee, err := lg.l2Client.GetBaseFee(ctx); err == nil {
			baseFeeGwei = float64(baseFee) / 1e9 // Convert wei to gwei
		}
	}

	// Use L2 node baseFee if available, otherwise fall back to block builder status
	if baseFeeGwei == 0 {
		baseFeeGwei = status.LatestBaseFeeGwei
	}

	lg.builderPressureMu.Lock()
	lg.builderPressure = pressure
	lg.latestBaseFeeGwei = baseFeeGwei
	lg.latestGasPriceGwei = gasPriceGwei
	lg.latestGasUsed = status.LatestGasUsed
	lg.blockAttestationEnabled = status.BlockAttestationEnabled
	lg.hsmProvider = status.HSMProvider
	lg.hsmKeyIDActive = status.HSMKeyIDActive
	lg.hsmFailoverEnabled = status.HSMFailoverEnabled
	lg.builderPressureMu.Unlock()
}

// fetchBuilderConfig fetches the complete builder configuration for environment snapshot.
func (lg *LoadGenerator) fetchBuilderConfig() *storage.EnvironmentSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Initialize environment snapshot with load-gen config
	env := &storage.EnvironmentSnapshot{
		LoadGenGasTipCapGwei:  float64(lg.cfg.GasTipCap) / 1e9, // wei to gwei
		LoadGenGasFeeCapGwei:  float64(lg.cfg.GasFeeCap) / 1e9, // wei to gwei
		LoadGenExecutionLayer: lg.cfg.ExecutionLayer,
		// Node identification - set defaults based on execution layer
		NodeName:        lg.cfg.Capabilities.Name,
		UseBlockBuilder: lg.cfg.Capabilities.HasExternalBlockBuilder,
	}

	// Fetch node info from L2 RPC (works for all execution layers)
	env.NodeVersion, env.ChainID = lg.fetchNodeInfo(ctx)

	// Derive NodeName from version string if more specific info available
	if env.NodeVersion != "" {
		env.NodeName = lg.parseNodeName(env.NodeVersion, lg.cfg.ExecutionLayer)
	}

	// Try to fetch builder status (only available when using external block-builder)
	if !lg.cfg.Capabilities.SupportsBuilderStatusAPI {
		lg.logger.Debug("skipping builder status fetch (not supported by execution layer)")
		return env
	}

	statusURL := lg.cfg.BuilderRPCURL + "/status"
	req, err := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		lg.logger.Debug("failed to create builder status request", "error", err)
		return env
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		lg.logger.Debug("failed to fetch builder config", "error", err)
		return env
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		lg.logger.Debug("builder status returned non-OK", "status", resp.StatusCode)
		return env
	}

	// Parse full response for environment snapshot
	var builderStatus struct {
		BlockTimeMs            int    `json:"blockTimeMs"`
		GasLimit               uint64 `json:"gasLimit"`
		MaxTxsPerBlock         int    `json:"maxTxsPerBlock"`
		TxOrdering             string `json:"txOrdering"`
		EnablePreconfirmations bool   `json:"enablePreconfirmations"`
		SkipEmptyBlocks        bool   `json:"skipEmptyBlocks"`
		IncludeDepositTx       bool   `json:"includeDepositTx"`
		// HSM / block attestation metadata (optional)
		BlockAttestationEnabled bool   `json:"blockAttestationEnabled"`
		HSMProvider             string `json:"hsmProvider"`
		HSMKeyIDActive          string `json:"hsmKeyIdActive"`
		HSMFailoverEnabled      bool   `json:"hsmFailoverEnabled"`
	}

	if err := stdjson.NewDecoder(resp.Body).Decode(&builderStatus); err != nil {
		lg.logger.Debug("failed to decode builder config", "error", err)
		return env
	}

	// Store builder config for incremental verification
	lg.txOrdering = builderStatus.TxOrdering
	lg.includeDepositTx = builderStatus.IncludeDepositTx

	// Update environment with builder config
	env.BuilderBlockTimeMs = builderStatus.BlockTimeMs
	env.BuilderGasLimit = builderStatus.GasLimit
	env.BuilderMaxTxsPerBlock = builderStatus.MaxTxsPerBlock
	env.BuilderTxOrdering = builderStatus.TxOrdering
	env.BuilderEnablePreconfs = builderStatus.EnablePreconfirmations
	env.BuilderSkipEmptyBlocks = builderStatus.SkipEmptyBlocks
	env.BuilderIncludeDepositTx = builderStatus.IncludeDepositTx
	env.BuilderBlockAttestationEnabled = builderStatus.BlockAttestationEnabled
	env.BuilderHSMProvider = builderStatus.HSMProvider
	env.BuilderHSMKeyIDActive = builderStatus.HSMKeyIDActive
	env.BuilderHSMFailoverEnabled = builderStatus.HSMFailoverEnabled

	return env
}

// fetchHeaderAttestations retrieves signed header attestations for the test block range.
func (lg *LoadGenerator) fetchHeaderAttestations(ctx context.Context, firstBlock, lastBlock uint64) []storage.HeaderAttestation {
	if firstBlock == 0 || lastBlock == 0 || lastBlock < firstBlock {
		return nil
	}
	if !lg.cfg.Capabilities.SupportsBuilderStatusAPI {
		return nil
	}

	result := make([]storage.HeaderAttestation, 0, int(lastBlock-firstBlock+1))
	client := &http.Client{Timeout: 1200 * time.Millisecond}

	for blockNum := firstBlock; blockNum <= lastBlock; blockNum++ {
		select {
		case <-ctx.Done():
			return result
		default:
		}

		reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		url := lg.cfg.BuilderRPCURL + "/block-attestations/" + strconv.FormatUint(blockNum, 10)
		req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		if err != nil {
			cancel()
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			cancel()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			cancel()
			continue
		}

		var payload builderHeaderAttestationResponse
		if err := stdjson.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			cancel()
			continue
		}
		resp.Body.Close()
		cancel()

		result = append(result, storage.HeaderAttestation{
			SchemaVersion: payload.SchemaVersion,
			Status:        payload.Status,
			BlockNumber:   payload.Commitment.BlockNumber,
			BlockHash:     payload.Commitment.BlockHash,
			ParentHash:    payload.Commitment.ParentHash,
			StateRoot:     payload.Commitment.StateRoot,
			ReceiptsRoot:  payload.Commitment.ReceiptsRoot,
			Timestamp:     payload.Commitment.Timestamp,
			GasUsed:       payload.Commitment.GasUsed,
			BaseFeeWei:    fmt.Sprint(payload.Commitment.BaseFeePerGasWei),
			Sequencer:     payload.Commitment.SequencerAddress,
			DigestHex:     payload.DigestHex,
			SignatureHex:  payload.SignatureHex,
			RHex:          payload.RHex,
			SHex:          payload.SHex,
			V:             payload.V,
			KeyID:         payload.KeyID,
			Provider:      payload.Provider,
			Failover:      payload.Failover,
			Error:         payload.Error,
			SignedAt:       payload.SignedAt,
		})
	}

	return result
}

// fetchNodeInfo fetches node version and chain ID from L2 RPC.
func (lg *LoadGenerator) fetchNodeInfo(ctx context.Context) (version string, chainID uint64) {
	if lg.l2Client == nil {
		return "", 0
	}

	// Fetch web3_clientVersion
	versionResp, err := lg.l2Client.Call(ctx, "web3_clientVersion", nil)
	if err == nil && versionResp != nil {
		var v string
		if err := stdjson.Unmarshal(versionResp, &v); err == nil {
			version = v
		}
	} else {
		lg.logger.Debug("failed to fetch web3_clientVersion", "error", err)
	}

	// Fetch eth_chainId
	chainIDResp, err := lg.l2Client.Call(ctx, "eth_chainId", nil)
	if err == nil && chainIDResp != nil {
		var hexChainID string
		if err := stdjson.Unmarshal(chainIDResp, &hexChainID); err == nil {
			// Parse hex string (e.g., "0xa455") to uint64
			if len(hexChainID) > 2 && hexChainID[:2] == "0x" {
				if parsed, err := parseHexUint64(hexChainID); err == nil {
					chainID = parsed
				}
			}
		}
	} else {
		lg.logger.Debug("failed to fetch eth_chainId", "error", err)
	}

	return version, chainID
}

// parseNodeName derives a user-friendly node name from the version string.
func (lg *LoadGenerator) parseNodeName(version, executionLayer string) string {
	// Common version string formats:
	// op-reth: "reth/v1.9.3-op/..."
	// gravity-reth: "reth/v0.4.1-gravity/..." or "gravity-reth/v0.4.1/..."
	// cdk-erigon: "erigon/..."
	// standard reth: "reth/v1.x.x/..."

	lowerVersion := strings.ToLower(version)

	// Check for gravity-reth signatures
	if strings.Contains(lowerVersion, "gravity") {
		return "gravity-reth"
	}

	// Check for op-reth signatures
	if strings.Contains(lowerVersion, "-op") || strings.Contains(lowerVersion, "optimism") {
		return "op-reth"
	}

	// Check for cdk-erigon
	if strings.Contains(lowerVersion, "erigon") || strings.Contains(lowerVersion, "cdk") {
		return "cdk-erigon"
	}

	// Check for standard reth
	if strings.Contains(lowerVersion, "reth") {
		// Could be gravity-reth or op-reth without clear markers - use execution layer hint
		if executionLayer == "gravity-reth" {
			return "gravity-reth"
		}
		return "op-reth" // Default to op-reth for reth-based nodes
	}

	// Fall back to execution layer config
	return executionLayer
}