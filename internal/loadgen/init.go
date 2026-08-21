package loadgen

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/pattern"
	"github.com/gateway-fm/loadgenerator/internal/ratelimit"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/sender"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/workload"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// privacyURL appends /rpc/{orgID} to the privacy proxy URL when an org id is
// configured (PrivacyOrgID takes precedence over PrivacyOrgIDFile). The privacy
// proxy requires the org in the path for users that belong to multiple orgs;
// without it RBAC can't resolve a single org and denies every request.
func privacyURL(cfg *config.Config, logger *slog.Logger) string {
	url := cfg.PrivacyRPCURL
	orgID := strings.TrimSpace(cfg.PrivacyOrgID)
	if orgID == "" && cfg.PrivacyOrgIDFile != "" {
		if b, err := os.ReadFile(cfg.PrivacyOrgIDFile); err == nil {
			orgID = strings.TrimSpace(string(b))
		} else {
			logger.Warn("could not read privacy org-id file; using base URL", "path", cfg.PrivacyOrgIDFile, "error", err)
		}
	}
	if orgID != "" {
		url = strings.TrimRight(url, "/") + "/rpc/" + orgID
	}
	return url
}

// l2AuthToken reads the bearer credential for the builder and L2 clients from
// cfg.L2AuthTokenFile, trimming the trailing newline a mounted secret file
// carries. Returns "" when no file is configured or it cannot be read — an
// unkeyed run must still start, and the failure then shows up as the edge's
// anonymous rate limit rather than as a crash, so the warning is the signal.
func l2AuthToken(cfg *config.Config, logger *slog.Logger) string {
	if cfg.L2AuthTokenFile == "" {
		return ""
	}
	b, err := os.ReadFile(cfg.L2AuthTokenFile)
	if err != nil {
		logger.Warn("could not read L2 auth token file; sending no Authorization header",
			"path", cfg.L2AuthTokenFile, "error", err)
		return ""
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		logger.Warn("L2 auth token file is empty; sending no Authorization header",
			"path", cfg.L2AuthTokenFile)
	}
	return token
}

// applyL2ClientTuning applies the optional timeout / retry overrides to a client
// config. Both are no-ops when unset, so an unconfigured run keeps
// DefaultClientConfig's 2s and 3 retries exactly.
//
// These matter through a proxy edge and not much anywhere else. A 2s timeout
// against an edge whose p50 is several seconds means the client abandons requests
// the edge is still serving, then retries them -- so the transaction lands and is
// re-sent, and a per-key limiter that counts batch ITEMS charges for both. That
// is load the client manufactures for itself.
func applyL2ClientTuning(cfg *config.Config, ccfg *rpc.ClientConfig) {
	if cfg.L2ClientTimeout > 0 {
		ccfg.Timeout = cfg.L2ClientTimeout
	}
	if cfg.L2ClientMaxRetries > 0 {
		ccfg.MaxRetries = cfg.L2ClientMaxRetries
	}
	if cfg.L2MaxConnsPerHost > 0 {
		ccfg.MaxConnsPerHost = cfg.L2MaxConnsPerHost
	}
}

// buildPrivacyClient builds a privacy-proxy-routed RPC client: routes to
// privacyURL(cfg), attaches the Bearer token from PrivacyAuthTokenFile, and
// wraps it in a NoBatch client (the proxy rejects JSON-RPC batches). Returns the
// client and the resolved URL.
func buildPrivacyClient(cfg *config.Config, logger *slog.Logger) (rpc.Client, string, error) {
	url := privacyURL(cfg, logger)
	pcfg := rpc.DefaultClientConfig(url)
	pcfg.Logger = logger
	if cfg.PrivacyAuthTokenFile != "" {
		b, err := os.ReadFile(cfg.PrivacyAuthTokenFile)
		if err != nil {
			return nil, url, fmt.Errorf("read privacy auth token file %s: %w", cfg.PrivacyAuthTokenFile, err)
		}
		pcfg.AuthToken = strings.TrimSpace(string(b))
	}
	return rpc.NewNoBatchClient(rpc.NewHTTPClient(pcfg)), url, nil
}

func (lg *LoadGenerator) runInitialization(req types.StartTestRequest) {
	// Defer error handling - if we panic or error, set status to error
	defer func() {
		if r := recover(); r != nil {
			lg.setError(fmt.Sprintf("initialization panic: %v", r))
		}
	}()

	// Clear any warnings from previous test
	lg.warningsMu.Lock()
	lg.warnings = nil
	lg.warningsMu.Unlock()

	// Route-all privacy (external/prod): rebuild the privacy-routed clients from
	// the token file at the START of each test, so a freshly-pasted/refreshed
	// token is picked up without a restart. The proxy is the only RPC endpoint in
	// this mode; nonce init, funding, and sends below all use the live
	// lg.builderClient/l2Client, so rebuilding here (before nonce init) routes the
	// whole test through the proxy with the current token.
	if lg.cfg.PrivacyRouteAll && lg.cfg.PrivacyRPCURL != "" {
		client, url, err := buildPrivacyClient(lg.cfg, lg.logger)
		if err != nil {
			lg.setError(fmt.Sprintf("privacy route-all requires auth token: %v", err))
			return
		}
		lg.privacyBuilderClient = client
		lg.builderClient = client
		lg.l2Client = client
		lg.sender = sender.New(sender.Config{
			Client:      client,
			Concurrency: 2000,
			Logger:      lg.logger,
		})
		lg.logger.Info("privacy route-all: all RPC routed through proxy (current token)", "url", url)
	}

	// Auto-detect the chain's actual chainId and adopt it. The load generator
	// signs every transaction with cfg.ChainID; against an external chain
	// (route-all / gasless mode) a stale default (e.g. the local 42069) produces
	// signatures the chain rejects as invalid for the wrong chain — silently
	// failing every send. Querying eth_chainId here makes external runs work
	// without the operator having to know the chain's id; on the bundled chain
	// the reported id matches the default, so this is a no-op there. Best-effort:
	// on query failure we keep the configured id.
	{
		cidCtx, cidCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if raw, err := lg.l2Client.Call(cidCtx, "eth_chainId", nil); err == nil {
			s := strings.TrimPrefix(strings.Trim(string(raw), "\""), "0x")
			if v, ok := new(big.Int).SetString(s, 16); ok && v.Sign() > 0 {
				if detected := v.Int64(); detected != lg.cfg.ChainID {
					lg.logger.Warn("chainId mismatch: adopting the chain's reported chainId",
						"configured", lg.cfg.ChainID, "detected", detected)
					lg.cfg.ChainID = detected
				}
			}
		} else {
			lg.logger.Warn("failed to query eth_chainId; using configured chainId",
				"chainId", lg.cfg.ChainID, "error", err)
		}
		cidCancel()
	}

	// Set defaults
	if req.TransactionType == "" {
		req.TransactionType = types.TxTypeEthTransfer
	}
	lg.currentTxType = req.TransactionType

	// Gasless mode: zero-fee chain that self-authorizes senders by signature, so
	// we skip funding and send 0-value eth-transfers from unfunded random accounts.
	// May be enabled per-test (request) or globally (GASLESS env). Only eth-transfer
	// is supported — contract types need a funded deployer and on-chain token state.
	lg.gasless = req.Gasless || lg.cfg.Gasless
	if lg.gasless {
		if req.TransactionType != types.TxTypeEthTransfer || workload.UsesRealisticMix(req.Pattern) {
			lg.setError("gasless mode supports the eth-transfer transaction type only (contract types require a funded deployer)")
			return
		}
		lg.logger.Info("gasless mode enabled: skipping funding, sending 0-value eth-transfers with zero gas")
	}

	// Auto-calculate required accounts based on target TPS
	// Formula: accounts = targetTPS * blockTimeSec * safetyMargin
	numAccounts := req.NumAccounts
	// For realistic tests, use the numAccounts from the realistic config
	if numAccounts <= 0 && req.RealisticConfig != nil && req.RealisticConfig.NumAccounts > 0 {
		numAccounts = req.RealisticConfig.NumAccounts
	}
	if numAccounts <= 0 {
		// Determine target TPS for account calculation
		targetTPSForAccounts := req.ConstantRate
		if targetTPSForAccounts == 0 {
			targetTPSForAccounts = req.RampEnd
		}
		if targetTPSForAccounts == 0 {
			targetTPSForAccounts = req.SpikeRate
		}
		if targetTPSForAccounts == 0 && req.RealisticConfig != nil {
			targetTPSForAccounts = req.RealisticConfig.TargetTPS
		}

		// For "max" pattern, use a default of 500 accounts or scale from initial rate
		if req.Pattern == types.PatternAdaptive {
			if req.AdaptiveInitialRate > 0 {
				numAccounts = config.CalculateRequiredAccounts(req.AdaptiveInitialRate*2, lg.cfg.BlockTimeMS)
			} else {
				numAccounts = config.MinAccountsForAdaptive
			}
		} else if targetTPSForAccounts > 0 {
			numAccounts = config.CalculateRequiredAccounts(targetTPSForAccounts, lg.cfg.BlockTimeMS)
		} else {
			numAccounts = 10 // Fallback default
		}

		lg.logger.Info("auto-calculated accounts based on TPS",
			"targetTPS", targetTPSForAccounts,
			"blockTimeMS", lg.cfg.BlockTimeMS,
			"accounts", numAccounts)
	} else {
		// User specified numAccounts - check if sufficient for target TPS
		targetTPS := req.ConstantRate
		if targetTPS == 0 {
			targetTPS = req.RampEnd
		}
		if targetTPS == 0 {
			targetTPS = req.SpikeRate
		}
		if targetTPS == 0 && req.RealisticConfig != nil {
			targetTPS = req.RealisticConfig.TargetTPS
		}
		if targetTPS == 0 && req.AdaptiveInitialRate > 0 {
			targetTPS = req.AdaptiveInitialRate * 2 // Adaptive can double
		}

		if warning := config.CheckAccountSufficiency(numAccounts, targetTPS, lg.cfg.BlockTimeMS); warning != "" {
			lg.warningsMu.Lock()
			lg.warnings = append(lg.warnings, warning)
			lg.warningsMu.Unlock()
			lg.logger.Warn("insufficient accounts for target TPS",
				"numAccounts", numAccounts,
				"targetTPS", targetTPS,
				"recommended", config.CalculateRequiredAccounts(targetTPS, lg.cfg.BlockTimeMS),
				"achievableTPS", config.EstimateMaxTPS(numAccounts, lg.cfg.BlockTimeMS))
		}
	}

	// Update init progress
	lg.initAccountsTotal = numAccounts

	accounts := lg.accountMgr.GetAccounts()
	if numAccounts > len(accounts) {
		dynamicCount := numAccounts - len(accounts)
		chainID := lg.cfg.ChainID

		// Try warm start from cached accounts. Skipped in gasless mode: cached
		// accounts always read zero balance on a zero-fee chain, which the warm
		// path treats as a re-genesis and wipes anyway — and there's no funding
		// to preserve, so generating fresh accounts is simpler.
		warmStartOK := false
		if lg.cacheStorage != nil && !lg.gasless {
			warmStartOK = lg.tryWarmStartAccounts(dynamicCount, chainID)
		}

		if !warmStartOK {
			// Cold start: generate + fund from scratch
			lg.initPhase = types.InitPhaseGeneratingAccts
			lg.initProgress = fmt.Sprintf("Generating %d accounts...", dynamicCount)
			lg.logger.Info("generating dynamic accounts", "count", dynamicCount)

			if err := lg.accountMgr.GenerateDynamicAccounts(dynamicCount); err != nil {
				lg.setError(fmt.Sprintf("failed to generate accounts: %v", err))
				return
			}
			lg.initAccountsGen = dynamicCount

			fundCtx, fundCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer fundCancel()

			if lg.gasless {
				// Zero-fee chain: no faucet funding. Random accounts send 0-value,
				// zero-gas transfers immediately, self-authorized by their signature.
				// Also skip the builder-nonce reset (a bundled-builder concern that
				// has no meaning against an external gasless chain).
				lg.logger.Info("gasless: skipping builder-nonce reset and account funding", "accounts", dynamicCount)
			} else {
				if err := lg.resetBuilderNonces(); err != nil {
					lg.setError(fmt.Sprintf("failed to reset builder nonces: %v (cannot start test with stale cache)", err))
					return
				}

				lg.initPhase = types.InitPhaseFundingAccts
				lg.initFundingTotal = dynamicCount
				lg.initProgress = fmt.Sprintf("Funding %d accounts from faucet...", dynamicCount)
				lg.logger.Info("funding dynamic accounts from faucet")

				if err := lg.accountMgr.FundDynamicAccounts(fundCtx, lg.builderClient, lg.l2Client); err != nil {
					lg.logger.Warn("failed to fund some dynamic accounts", "error", err)
				}
				lg.initFundingSent = lg.accountMgr.GetAccountsFunded()
				if lg.initFundingSent == 0 {
					lg.setError(fmt.Sprintf("failed to fund any accounts (0/%d funded) - cannot start test", dynamicCount))
					return
				}

				lg.initPhase = types.InitPhaseWaitingForFunding
				fundedCount := lg.accountMgr.GetAccountsFunded()
				blocksNeeded := (fundedCount / 4000) + 3
				waitTime := time.Duration(blocksNeeded) * time.Second
				if waitTime < 5*time.Second {
					waitTime = 5 * time.Second
				}
				lg.initProgress = fmt.Sprintf("Waiting for %d funding TXs to be included (~%ds)...", fundedCount, int(waitTime.Seconds()))
				lg.logger.Info("waiting for funding transactions to be included",
					"fundedAccounts", fundedCount,
					"blocksNeeded", blocksNeeded,
					"waitTime", waitTime)
				time.Sleep(waitTime)
			}

			lg.initPhase = types.InitPhaseInitNonces
			lg.initProgress = "Initializing nonces for dynamic accounts..."
			if err := lg.accountMgr.InitializeDynamicNonces(fundCtx, lg.builderClient); err != nil {
				lg.logger.Warn("failed to initialize dynamic nonces", "error", err)
			}

			// Persist newly generated accounts for future reuse
			lg.saveDynamicAccountsToCache(chainID)
		}
	}

	// Initialize nonces for built-in accounts
	lg.initPhase = types.InitPhaseInitNonces
	lg.initProgress = "Initializing nonces for built-in accounts..."
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CRITICAL: Use builderClient to sync through builder (eth_getPendingNonce)
	// This ensures load generator and builder have the same nonce view, preventing
	// "nonce ahead" errors when builder has cached nonces from previous tests
	if err := lg.accountMgr.InitializeNonces(ctx, lg.builderClient, min(numAccounts, len(accounts))); err != nil {
		lg.setError(fmt.Sprintf("failed to initialize nonces: %v", err))
		return
	}

	// Set up EIP-1559 gas pricing
	if lg.gasless {
		// Zero-fee chain: both tip and fee cap are zero, so an unfunded random
		// account can send. Skip the gas-price / baseFee probes and the bumps
		// below — they would otherwise raise the fee cap to a non-zero value and
		// require the sender to hold a balance, defeating gasless mode.
		lg.gasTipCap = big.NewInt(0)
		lg.gasFeeCap = big.NewInt(0)
		lg.logger.Info("gasless: zero gas tip and fee cap")
	} else {
		lg.gasTipCap = big.NewInt(lg.cfg.GasTipCap)
		if lg.cfg.GasFeeCap > 0 {
			// Explicit fee cap configured
			lg.gasFeeCap = big.NewInt(lg.cfg.GasFeeCap)
		} else {
			// Auto-calculate from chain's gas price (query L2 node)
			gasPrice, err := lg.l2Client.GetGasPrice(ctx)
			if err != nil {
				lg.logger.Warn("failed to query gas price, using 2x tip as fee cap", "error", err)
				lg.gasFeeCap = new(big.Int).Mul(lg.gasTipCap, big.NewInt(2))
			} else {
				// Use 2x queried price for headroom against base fee fluctuation
				lg.gasFeeCap = new(big.Int).Mul(big.NewInt(int64(gasPrice)), big.NewInt(2))
			}
		}

		// EIP-1559 invariant: maxFeePerGas (feeCap) must be >= maxPriorityFeePerGas
		// (tipCap), otherwise the node rejects the tx outright. On a quiet chain
		// eth_gasPrice (and thus 2x it) can fall below the configured tip, which
		// silently fails every transaction. Clamp the fee cap up to the tip.
		if lg.gasFeeCap.Cmp(lg.gasTipCap) < 0 {
			lg.logger.Warn("gasFeeCap below gasTipCap; raising to tipCap (EIP-1559 invariant)",
				"oldFeeCap", lg.gasFeeCap, "tipCap", lg.gasTipCap)
			lg.gasFeeCap = new(big.Int).Set(lg.gasTipCap)
		}

		// CRITICAL: Ensure gasFeeCap is above current baseFee to avoid silent rejections
		// Query baseFee directly from latest block and ensure we're at least 2x above it
		if baseFee, err := lg.l2Client.GetBaseFee(ctx); err == nil && baseFee > 0 {
			minFeeCap := new(big.Int).Mul(big.NewInt(int64(baseFee)), big.NewInt(2))
			if lg.gasFeeCap.Cmp(minFeeCap) < 0 {
				lg.logger.Warn("gasFeeCap below 2x baseFee, adjusting to prevent rejections",
					"oldFeeCap", lg.gasFeeCap,
					"newFeeCap", minFeeCap,
					"baseFee", baseFee,
					"baseFeeGwei", float64(baseFee)/1e9,
				)
				lg.gasFeeCap = minFeeCap
			}
			lg.logger.Info("gas pricing configured",
				"gasTipCap", lg.gasTipCap,
				"gasFeeCap", lg.gasFeeCap,
				"baseFee", baseFee,
				"baseFeeGwei", float64(baseFee)/1e9,
			)
		} else {
			lg.logger.Info("gas pricing configured (baseFee query failed, using calculated values)",
				"gasTipCap", lg.gasTipCap,
				"gasFeeCap", lg.gasFeeCap,
				"baseFeeError", err,
			)
		}
	}

	// Which contracts to deploy. Derived by workload.DeployTxTypeFor so this decision
	// and the workers' tx-type selection read the SAME config — including the default
	// config the workers fall back to when none was supplied.
	txTypeForDeploy := workload.DeployTxTypeFor(req)

	// Phase: Deploying contracts
	if txTypeForDeploy != types.TxTypeEthTransfer {
		lg.initPhase = types.InitPhaseDeployingContracts
		lg.initProgress = "Deploying test contracts..."
		if txTypeForDeploy == types.TxTypeUniswapSwap {
			// Uniswap V3: 7 steps (WETH9, USDC, Factory, SwapRouter, NFTManager, Pool, Liquidity) + 3 base
			lg.initContractsTotal = 10
		} else {
			lg.initContractsTotal = 3 // ERC20, GasConsumer, NFT
		}
	}

	if err := lg.ensureContractsDeployed(txTypeForDeploy); err != nil {
		lg.setError(fmt.Sprintf("failed to deploy contracts: %v", err))
		return
	}
	// Note: initContractsDone is now updated incrementally via progress callbacks

	// Pre-mint NFTs for erc721-transfer load tests (setup-only, not load-test traffic).
	if req.TransactionType == types.TxTypeERC721Transfer && req.Erc721PreMint > 0 {
		lg.contractsMu.RLock()
		nftAddr := lg.nftContract
		lg.contractsMu.RUnlock()

		if nftAddr == (common.Address{}) {
			lg.setError("nft contract address unavailable for pre-mint")
			return
		}

		accounts := lg.accountMgr.GetAccounts()
		if len(accounts) == 0 {
			lg.setError("no accounts available for pre-mint")
			return
		}
		minter := accounts[0]

		lg.statusMu.Lock()
		lg.initProgress = fmt.Sprintf("Pre-minting NFTs (0/%d)...", req.Erc721PreMint)
		lg.statusMu.Unlock()

		preMintProgress := func(minted, total int) {
			lg.statusMu.Lock()
			lg.initProgress = fmt.Sprintf("Pre-minting NFTs (%d/%d)...", minted, total)
			lg.statusMu.Unlock()
		}

		preMintCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if err := lg.deployer.PreMintNFTs(preMintCtx, minter, nftAddr, req.Erc721PreMint, preMintProgress); err != nil {
			cancel()
			lg.setError(fmt.Sprintf("failed to pre-mint NFTs: %v", err))
			return
		}
		cancel()
	}

	// Create pattern
	patternCfg := pattern.Config{
		Duration:            time.Duration(req.DurationSec) * time.Second,
		ConstantRate:        req.ConstantRate,
		RampStart:           req.RampStart,
		RampEnd:             req.RampEnd,
		BaselineRate:        req.BaselineRate,
		SpikeRate:           req.SpikeRate,
		SpikeDuration:       time.Duration(req.SpikeDuration) * time.Second,
		SpikeInterval:       time.Duration(req.SpikeInterval) * time.Second,
		AdaptiveInitialRate: req.AdaptiveInitialRate,
	}

	// Set stress rate from stress config if provided
	if req.RealisticConfig != nil && req.RealisticConfig.TargetTPS > 0 {
		patternCfg.RealisticRate = req.RealisticConfig.TargetTPS
	}

	pat, err := lg.patternReg.Get(req.Pattern, patternCfg)
	if err != nil {
		lg.setError(fmt.Sprintf("invalid pattern: %v", err))
		return
	}
	lg.currentPattern = pat

	// Set initial rate
	initialRate := float64(pat.GetRate(0))
	if initialRate <= 0 {
		initialRate = 100 // Fallback minimum
	}
	atomic.StoreInt64(&lg.currentRate, int64(initialRate))
	lg.rateLimiter = ratelimit.New(initialRate)
	atomic.StoreInt64(&lg.peakRate, 0)
	atomic.StoreInt64(&lg.pendingCount, 0)
	atomic.StoreInt32(&lg.stopping, 0)
	atomic.StoreInt32(&lg.forceStop, 0)

	// Reset metrics
	lg.metricsCol.Reset()
	lg.preconfLatencies.Reset()
	atomic.StoreUint64(&lg.lastPreconfSeqNum, 0)
	atomic.StoreUint64(&lg.preconfGaps, 0)

	// Initialize realistic test metrics tracking
	if workload.UsesRealisticMix(req.Pattern) {
		lg.metricsCol.InitRealisticMetrics()
	}

	// Set timing
	lg.startTime = time.Now()
	lg.currentDuration = time.Duration(req.DurationSec) * time.Second
	lg.lastSentCount = 0
	lg.lastCheckTime = lg.startTime
	lg.currentTPS = 0

	// Determine target TPS for buffer allocation
	targetTPS := req.ConstantRate
	if targetTPS == 0 {
		targetTPS = req.RampEnd
	}
	if targetTPS == 0 {
		targetTPS = req.SpikeRate
	}
	if targetTPS == 0 && req.RealisticConfig != nil {
		targetTPS = req.RealisticConfig.TargetTPS
	}
	if targetTPS == 0 {
		targetTPS = 1000 // Default estimate
	}

	// Determine if TX logging should be enabled
	lg.txLoggingEnabled = lg.shouldLogTransactions(lg.currentDuration, targetTPS)
	if !lg.txLoggingEnabled {
		lg.logger.Warn("TX logging disabled due to estimated memory usage",
			"duration", lg.currentDuration,
			"targetTPS", targetTPS,
			"estimatedMemoryMB", (int(lg.currentDuration.Seconds())*targetTPS*txLogEntrySize)/(1024*1024))
	}

	// Initialize memory buffers
	lg.initBuffers(lg.currentDuration, targetTPS)

	// Create test run ID and persist to storage
	lg.currentTestID = fmt.Sprintf("test-%d", time.Now().UnixNano())
	if lg.storage != nil {
		defaultName := fmt.Sprintf("%s test - %s", req.Pattern, lg.startTime.Format("Jan 2 3:04 PM"))
		testRun := &storage.TestRun{
			ID:               lg.currentTestID,
			StartedAt:        lg.startTime,
			Pattern:          req.Pattern,
			TransactionType:  lg.currentTxType,
			DurationMs:       lg.currentDuration.Milliseconds(),
			Config:           &req,
			Status:           "running",
			TxLoggingEnabled: lg.txLoggingEnabled,
			ExecutionLayer:   lg.cfg.ExecutionLayer, // Track which execution layer was used
			PrivacyMode:      req.PrivacyMode,
			CustomName:       &defaultName,
		}
		if err := lg.storage.CreateTestRun(context.Background(), testRun); err != nil {
			lg.logger.Error("failed to create test run in storage", "error", err)
			// Continue anyway - storage is non-critical
		}
	}

	// Create context
	lg.ctx, lg.cancel = context.WithCancel(context.Background())

	// Connect to preconf WebSocket if the execution layer supports it and URL is configured.
	// The chain-poller fallback (for execution layers without a preconf channel) is
	// started later, after `testStartBlockNumber` has been recorded — otherwise the
	// poller would have no anchor and would scan from block 0.
	if lg.cfg.Capabilities.SupportsPreconfirmations && lg.cfg.PreconfWSURL != "" {
		go lg.connectPreconfWS()
	}

	// Connect to builder block metrics WebSocket if supported
	if lg.cfg.Capabilities.SupportsBlockMetricsWS && lg.cfg.PreconfWSURL != "" {
		go lg.connectBuilderMetricsWS()
	}

	// Connect to L2 WebSocket for block metrics (always try, will warn if fails)
	go lg.connectL2WS()

	// Clear block metrics from previous test
	lg.blockMetricsMu.Lock()
	lg.blockMetrics = nil
	lg.cumulativeGasUsed = 0
	lg.cumulativeGasLimit = 0
	lg.rollingGasWindow = nil // Clear rolling window for smooth MGas/s chart
	lg.rollingTxWindow = nil  // Clear rolling window for smooth TX/s chart
	lg.peakMgasPerSec = 0
	lg.lastMgasPerSec = 0 // Reset cached MGas/s value
	lg.peakTxPerSec = 0
	lg.lastTxPerSec = 0 // Reset cached TX/s value
	lg.lastBlockTime = time.Time{}
	lg.lastBlockNumber = 0
	lg.firstBlockNumber = 0
	lg.lastRecordedBlock = 0 // Reset deduplication tracking
	lg.totalBlockCount = 0
	lg.testStartBlockNumber = 0
	lg.testEndBlockNumber = 0
	lg.rpcLastBlockNumber = 0
	lg.blockMetricsMu.Unlock()

	// Clear incremental verification data from previous test
	lg.incrementalSnapshotsMu.Lock()
	lg.incrementalSnapshots = nil
	lg.incrementalSnapshotsMu.Unlock()
	lg.recentBlockNumbersMu.Lock()
	lg.recentBlockNumbers = nil
	lg.recentBlockNumbersMu.Unlock()
	lg.recentConfirmedMu.Lock()
	lg.recentConfirmedHashes = nil
	lg.recentConfirmedMu.Unlock()
	lg.txOrdering = ""

	// Query starting block number via RPC (fallback for when WebSocket fails)
	if lg.l2Client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if startBlock, err := lg.l2Client.GetBlockNumber(ctx); err == nil {
			lg.blockMetricsMu.Lock()
			lg.testStartBlockNumber = startBlock
			lg.blockMetricsMu.Unlock()
			lg.logger.Info("recorded test start block number", "block", startBlock)
		} else {
			lg.logger.Warn("failed to get start block number", "error", err)
		}
		cancel()
	}

	// Spawn the receipt-polling fallback when no live preconfirmation stream is
	// available — either the execution layer has no preconf channel, or it does
	// but no preconf WS URL is configured (e.g. external/prod privacy mode, where
	// the only RPC endpoint is the proxy and there is no builder preconf socket).
	// Without this, such modes would have zero confirmation sources and report 0
	// confirmed even though txs land on-chain. Started here (not earlier) so the
	// poller anchors at `testStartBlockNumber` rather than scanning from block 0.
	if !lg.cfg.Capabilities.SupportsPreconfirmations || lg.cfg.PreconfWSURL == "" {
		go lg.connectChainPoller()
	}

	// Privacy routing. Three cases:
	//   - route-all (external/prod): builder/l2/sender already point at the proxy
	//     from startup (see NewLoadGenerator); nothing to swap per test.
	//   - per-test (dev/bundled): route only sends through the proxy, reads stay
	//     direct; re-read the token each test (it may be refreshed between runs).
	//   - non-privacy: use the default (direct) sender.
	switch {
	case lg.cfg.PrivacyRouteAll && lg.cfg.PrivacyRPCURL != "":
		// no-op: all clients are privacy-routed at startup
	case req.PrivacyMode && lg.cfg.PrivacyRPCURL != "":
		client, url, err := buildPrivacyClient(lg.cfg, lg.logger)
		if err != nil {
			lg.logger.Error("failed to build privacy client", "error", err)
			lg.setError(fmt.Sprintf("privacy mode requires auth token: %v", err))
			return
		}
		lg.privacyBuilderClient = client
		lg.sender = sender.New(sender.Config{
			Client:      lg.privacyBuilderClient,
			Concurrency: 2000,
			Logger:      lg.logger,
		})
		lg.logger.Info("using privacy proxy for this test", "url", url)
	default:
		// Ensure we use the default sender (restore after a previous privacy test)
		lg.sender = lg.defaultSender
	}

	// Phase: Starting workers
	lg.initPhase = types.InitPhaseStartingWorkers
	lg.initProgress = "Starting test workers..."

	// Get all accounts (built-in + dynamic)
	allAccounts := lg.accountMgr.GetAccounts()
	dynamicAccounts := lg.accountMgr.GetDynamicAccounts()
	allAccounts = append(allAccounts, dynamicAccounts...)

	// Configure read load before any worker starts, so an unsuitable target (archive
	// selection against a pruned node, a missing ERC-20 for eth_call) aborts the test
	// here rather than producing a run whose reads measure nothing.
	if err := lg.setupReadLoad(req); err != nil {
		lg.setError(fmt.Sprintf("read load configuration failed: %v", err))
		return
	}

	// Start sender workers
	// More workers = better parallelism, but must not exceed semaphore capacity
	numWorkers := len(allAccounts)
	if numWorkers > maxSenderWorkers {
		numWorkers = maxSenderWorkers // Cap workers (should be <= sender concurrency / 4)
	}
	if numWorkers > numAccounts {
		numWorkers = numAccounts
	}

	for i := 0; i < numWorkers; i++ {
		lg.wg.Add(1)
		go lg.senderWorker(i, allAccounts)
	}

	// Start read load alongside the senders, at its own independent rate
	lg.startReadLoad()

	// Start TPS calculator
	lg.wg.Add(1)
	go lg.tpsCalculator()

	// Start adaptive controller if needed
	if pat.NeedsAdaptiveController() {
		lg.wg.Add(1)
		go lg.adaptiveController()
	}

	// Start backpressure monitor to check block builder status
	lg.wg.Add(1)
	go lg.backpressureMonitor()

	// Start completion watcher (NOT in WaitGroup because it calls StopTest which waits on wg)
	go lg.completionWatcher()

	// Initialization complete - transition to running
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.initPhase = types.InitPhaseNone
	lg.initProgress = ""
	lg.statusMu.Unlock()

	// Fetch builder config to get txOrdering for incremental verification
	if env := lg.fetchBuilderConfig(); env != nil {
		lg.txOrdering = env.BuilderTxOrdering
		lg.logger.Info("fetched builder tx ordering for verification", "ordering", lg.txOrdering)
	}

	// Start incremental verification for long-running tests
	lg.startIncrementalVerification()

	lg.logger.Info("test started",
		"pattern", req.Pattern,
		"txType", lg.currentTxType,
		"duration", lg.currentDuration,
		"accounts", numAccounts,
	)
}
