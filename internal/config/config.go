// Package config handles configuration loading and validation.
package config

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/execnode"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Config holds load generator configuration.
type Config struct {
	BuilderRPCURL      string
	L2RPCURL           string
	L2WSURL            string // WebSocket URL for L2 newHeads (block metrics)
	PreconfWSURL       string // WebSocket URL for preconfirmation events
	ChainID            int64
	GasPrice           int64  // Deprecated: Use GasTipCap instead. Kept for backwards compatibility.
	GasTipCap          int64  // EIP-1559 priority fee (tip) in wei
	GasFeeCap          int64  // EIP-1559 max fee per gas in wei (0 = auto from chain)
	GasLimit           uint64
	ListenAddr         string
	DatabasePath       string // Path to SQLite database file
	ExecutionLayer     string // Execution layer: "reth", "op-reth", "gravity-reth", or "cdk-erigon"
	BlockTimeMS        int    // Block time in milliseconds (for account scaling)
	CORSAllowedOrigins string // Comma-separated list of allowed origins, or "*" for all (default: "*")

	// Capabilities holds the resolved execution layer capabilities.
	// This is populated automatically based on ExecutionLayer.
	Capabilities *execnode.ExecutionLayerCapabilities
}

// CLIConfig holds CLI-specific settings for running in CLI mode.
type CLIConfig struct {
	Pattern     types.LoadPattern
	TPS         int
	Duration    time.Duration
	NumAccounts int
}

// Defaults
const (
	DefaultBuilderRPCURL   = "http://localhost:13000"
	DefaultL2RPCURL        = "http://localhost:13000"
	DefaultChainID         = 42069
	DefaultGasPrice        = 1000000000 // 1 Gwei (deprecated, use GasTipCap)
	DefaultGasTipCap       = 1000000000 // 1 Gwei - priority fee (tip)
	DefaultGasFeeCap       = 0          // 0 = auto-calculate from chain gas price
	DefaultGasLimit        = 21000
	DefaultListenAddr      = ":3001"
	DefaultDatabasePath    = "./data/loadgen.db"
	DefaultDuration        = 30 * time.Second
	DefaultNumAccounts     = 100
	DefaultExecutionLayer      = "reth"     // "reth" or "cdk-erigon"
	DefaultBlockTimeMS         = 250        // 250ms default block time
	DefaultCORSAllowedOrigins  = "*"        // Allow all origins by default for dev
	AccountSafetyMargin        = 1.5        // 50% extra accounts for safety
	TxsPerAccountPerBlock      = 30         // TXs each account can sustain per block (50 tested sustainable, 30 with safety buffer)
	MinAccountsForAdaptive = 500        // Minimum accounts for "adaptive" pattern
	MaxAccountsLimit       = 5000       // Maximum accounts to prevent resource exhaustion
	MinAccounts            = 100        // Minimum accounts for any load test
)

// CalculateRequiredAccounts calculates the number of accounts needed for a given TPS.
// Formula: accounts = ceil(targetTPS * blockTimeSec / TxsPerAccountPerBlock * safetyMargin)
// The block builder batches entire nonce chains per sender, so each account can handle
// multiple TXs per block (TxsPerAccountPerBlock). This dramatically reduces the number
// of accounts needed compared to a naive 1-TX-per-account-per-block assumption.
func CalculateRequiredAccounts(targetTPS int, blockTimeMS int) int {
	if blockTimeMS <= 0 {
		blockTimeMS = DefaultBlockTimeMS
	}
	blockTimeSec := float64(blockTimeMS) / 1000.0
	required := int(math.Ceil(float64(targetTPS) * blockTimeSec / TxsPerAccountPerBlock * AccountSafetyMargin))
	if required < MinAccounts {
		required = MinAccounts
	}
	if required > MaxAccountsLimit {
		required = MaxAccountsLimit
	}
	return required
}

// EstimateMaxTPS estimates the maximum sustainable TPS for a given number of accounts.
// This is the inverse of CalculateRequiredAccounts (without safety margin for realistic estimate).
// Formula: maxTPS = accounts * TxsPerAccountPerBlock / blockTimeSec
func EstimateMaxTPS(numAccounts int, blockTimeMS int) int {
	if blockTimeMS <= 0 {
		blockTimeMS = DefaultBlockTimeMS
	}
	blockTimeSec := float64(blockTimeMS) / 1000.0
	// Don't apply safety margin here - give realistic estimate
	maxTPS := int(float64(numAccounts) * TxsPerAccountPerBlock / blockTimeSec)
	if maxTPS < 1 {
		maxTPS = 1
	}
	return maxTPS
}

// CheckAccountSufficiency checks if the number of accounts is sufficient for the target TPS.
// Returns a warning message if accounts are insufficient, empty string otherwise.
func CheckAccountSufficiency(numAccounts int, targetTPS int, blockTimeMS int) string {
	if targetTPS <= 0 || numAccounts <= 0 {
		return ""
	}

	recommended := CalculateRequiredAccounts(targetTPS, blockTimeMS)
	if numAccounts >= recommended {
		return ""
	}

	// Calculate what TPS can actually be achieved
	achievableTPS := EstimateMaxTPS(numAccounts, blockTimeMS)
	percentage := float64(numAccounts) / float64(recommended) * 100

	return fmt.Sprintf(
		"Insufficient accounts for target TPS: have %d accounts (can sustain ~%d TPS), but targeting %d TPS. "+
			"Recommended: %d accounts. Current capacity: %.0f%% of target.",
		numAccounts, achievableTPS, targetTPS, recommended, percentage,
	)
}

// Load reads configuration from environment variables and command-line flags.
// Command-line flags take precedence over environment variables.
// Returns the config, CLI config (nil if running in server mode), and any error.
func Load() (*Config, *CLIConfig, error) {
	cfg := &Config{
		BuilderRPCURL:  DefaultBuilderRPCURL,
		L2RPCURL:       DefaultL2RPCURL,
		ChainID:        DefaultChainID,
		GasPrice:       DefaultGasPrice,
		GasTipCap:      DefaultGasTipCap,
		GasFeeCap:      DefaultGasFeeCap,
		GasLimit:       DefaultGasLimit,
		ListenAddr:     DefaultListenAddr,
		DatabasePath:   DefaultDatabasePath,
		ExecutionLayer:     DefaultExecutionLayer,
		BlockTimeMS:        DefaultBlockTimeMS,
		CORSAllowedOrigins: DefaultCORSAllowedOrigins,
	}

	// Load from environment variables first
	if v := os.Getenv("BUILDER_RPC_URL"); v != "" {
		cfg.BuilderRPCURL = v
	}
	if v := os.Getenv("L2_RPC_URL"); v != "" {
		cfg.L2RPCURL = v
	}
	if v := os.Getenv("L2_WS_URL"); v != "" {
		cfg.L2WSURL = v
	}
	if v := os.Getenv("PRECONF_WS_URL"); v != "" {
		cfg.PreconfWSURL = v
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("DATABASE_PATH"); v != "" {
		cfg.DatabasePath = v
	}
	if v := os.Getenv("EXECUTION_LAYER"); v != "" {
		cfg.ExecutionLayer = v
	}
	if v := os.Getenv("BLOCK_TIME_MS"); v != "" {
		if ms, err := parseIntEnv(v); err == nil && ms > 0 {
			cfg.BlockTimeMS = ms
		}
	}
	if v := os.Getenv("CORS_ALLOWED_ORIGINS"); v != "" {
		cfg.CORSAllowedOrigins = v
	}
	if v := os.Getenv("GAS_TIP_CAP"); v != "" {
		if tip, err := parseInt64Env(v); err == nil && tip > 0 {
			cfg.GasTipCap = tip
		}
	}
	if v := os.Getenv("GAS_FEE_CAP"); v != "" {
		if fee, err := parseInt64Env(v); err == nil && fee >= 0 {
			cfg.GasFeeCap = fee
		}
	}

	// Define command-line flags
	var (
		builderURL   = flag.String("builder", cfg.BuilderRPCURL, "Block builder RPC URL")
		l2URL        = flag.String("l2", cfg.L2RPCURL, "L2 RPC URL")
		preconfWS    = flag.String("preconf-ws", cfg.PreconfWSURL, "Preconfirmation WebSocket URL")
		chainID      = flag.Int64("chainid", cfg.ChainID, "Chain ID")
		gasPrice     = flag.Int64("gasprice", cfg.GasPrice, "Gas price in wei (deprecated, use -gastipcap)")
		gasTipCap    = flag.Int64("gastipcap", cfg.GasTipCap, "EIP-1559 priority fee (tip) in wei")
		gasFeeCap    = flag.Int64("gasfeecap", cfg.GasFeeCap, "EIP-1559 max fee per gas in wei (0=auto)")
		gasLimit     = flag.Uint64("gaslimit", cfg.GasLimit, "Gas limit")
		listenAddr   = flag.String("listen", cfg.ListenAddr, "HTTP listen address")
		patternFlag  = flag.String("pattern", "", "Load pattern (constant, ramp, spike, adaptive, realistic)")
		tpsFlag      = flag.Int("tps", 0, "Target TPS (CLI mode)")
		durationFlag = flag.Duration("duration", DefaultDuration, "Test duration")
		accountsFlag = flag.Int("accounts", DefaultNumAccounts, "Number of accounts")
	)

	flag.Parse()

	// Apply flags to config
	cfg.BuilderRPCURL = *builderURL
	cfg.L2RPCURL = *l2URL
	cfg.PreconfWSURL = *preconfWS
	cfg.ChainID = *chainID
	cfg.GasPrice = *gasPrice
	cfg.GasTipCap = *gasTipCap
	cfg.GasFeeCap = *gasFeeCap
	cfg.GasLimit = *gasLimit
	cfg.ListenAddr = *listenAddr

	// Backwards compatibility: if GasPrice was explicitly set and GasTipCap wasn't changed,
	// use GasPrice as GasTipCap (legacy behavior)
	if cfg.GasPrice != DefaultGasPrice && cfg.GasTipCap == DefaultGasTipCap {
		cfg.GasTipCap = cfg.GasPrice
	}

	// Resolve execution layer capabilities
	cfg.Capabilities = execnode.DefaultRegistry().Get(cfg.ExecutionLayer)
	if cfg.Capabilities == nil {
		return nil, nil, fmt.Errorf("unknown execution layer: %s (supported: reth, op-reth, gravity-reth, cdk-erigon)", cfg.ExecutionLayer)
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}

	// Check if running in CLI mode (TPS specified)
	if *tpsFlag > 0 {
		pattern := types.LoadPattern(*patternFlag)
		if pattern == "" {
			pattern = types.PatternConstant
		}

		cliCfg := &CLIConfig{
			Pattern:     pattern,
			TPS:         *tpsFlag,
			Duration:    *durationFlag,
			NumAccounts: *accountsFlag,
		}

		if err := cliCfg.Validate(); err != nil {
			return nil, nil, err
		}

		return cfg, cliCfg, nil
	}

	return cfg, nil, nil
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.BuilderRPCURL == "" {
		return fmt.Errorf("builder RPC URL is required")
	}
	if c.L2RPCURL == "" {
		return fmt.Errorf("L2 RPC URL is required")
	}
	if c.ChainID <= 0 {
		return fmt.Errorf("chain ID must be positive")
	}
	if c.GasTipCap <= 0 {
		return fmt.Errorf("gas tip cap must be positive")
	}
	// GasFeeCap can be 0 (auto-calculate from chain) or positive
	if c.GasFeeCap < 0 {
		return fmt.Errorf("gas fee cap cannot be negative")
	}
	if c.GasLimit == 0 {
		return fmt.Errorf("gas limit must be positive")
	}
	return nil
}

// Validate validates the CLI configuration.
func (c *CLIConfig) Validate() error {
	switch c.Pattern {
	case types.PatternConstant, types.PatternRamp, types.PatternSpike, types.PatternAdaptive, types.PatternRealistic, types.PatternAdaptiveRealistic:
		// valid
	default:
		return fmt.Errorf("invalid pattern: %s", c.Pattern)
	}

	if c.TPS <= 0 {
		return fmt.Errorf("TPS must be positive")
	}
	if c.Duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	if c.NumAccounts <= 0 || c.NumAccounts > 10000 {
		return fmt.Errorf("accounts must be between 1 and 10000")
	}
	return nil
}

// parseIntEnv parses a string environment variable as an integer.
func parseIntEnv(s string) (int, error) {
	return strconv.Atoi(s)
}

// parseInt64Env parses a string environment variable as an int64.
func parseInt64Env(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
