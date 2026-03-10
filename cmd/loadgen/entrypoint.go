package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/execnode"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/transport"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func main() {
	// Parse flags - preserving exact same interface as original
	builderURL := flag.String("builder", getEnvOrDefault("BUILDER_RPC_URL", "http://localhost:13000"), "Block builder RPC URL")
	l2URL := flag.String("l2", getEnvOrDefault("L2_RPC_URL", "http://localhost:13000"), "L2 RPC URL")
	preconfWS := flag.String("preconf-ws", getEnvOrDefault("PRECONF_WS_URL", ""), "Preconfirmation WebSocket URL")
	chainID := flag.Int64("chainid", 42069, "Chain ID")
	gasPrice := flag.Int64("gasprice", 1000000000, "Gas price in wei (default 1 gwei)")
	gasLimit := flag.Uint64("gaslimit", 21000, "Gas limit per transaction")
	listenAddr := flag.String("listen", getEnvOrDefault("LISTEN_ADDR", ":3001"), "HTTP API listen address")
	databasePath := flag.String("database", getEnvOrDefault("DATABASE_PATH", "./data/loadgen.db"), "SQLite database path")

	// Block time for account scaling (must match block-builder BLOCK_TIME_MS)
	blockTimeMS := flag.Int("block-time-ms", getEnvIntOrDefault("BLOCK_TIME_MS", config.DefaultBlockTimeMS), "Block time in milliseconds (for account scaling)")

	// Execution layer selection
	executionLayer := flag.String("execution-layer", getEnvOrDefault("EXECUTION_LAYER", "reth"), "Execution layer (reth, op-reth, gravity-reth, cdk-erigon)")

	// CLI mode flags
	patternFlag := flag.String("pattern", "constant", "Load pattern (constant, ramp, spike, max)")
	targetTPS := flag.Int("tps", 0, "Target TPS for constant pattern (CLI mode - if set, runs single test)")
	duration := flag.Duration("duration", 30*time.Second, "Test duration (CLI mode)")
	numAccounts := flag.Int("accounts", 0, "Number of accounts (0=auto-calculate based on TPS)")

	// Logging
	logLevel := flag.String("log-level", getEnvOrDefault("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")

	flag.Parse()

	// Setup logger
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// Start pprof server on localhost only (not reachable from outside the container)
	go func() {
		logger.Info("pprof listening", "addr", "localhost:6061")
		if err := http.ListenAndServe("localhost:6061", nil); err != nil {
			logger.Error("pprof server failed", "error", err)
		}
	}()

	// Initialize storage
	store, err := storage.NewSQLiteStorage(*databasePath)
	if err != nil {
		logger.Error("failed to initialize storage", "error", err, "path", *databasePath)
		os.Exit(1)
	}
	defer store.Close()
	logger.Info("initialized storage", "path", *databasePath)

	// Build config
	cfg := &config.Config{
		BuilderRPCURL:  *builderURL,
		L2RPCURL:       *l2URL,
		PreconfWSURL:   *preconfWS,
		ChainID:        *chainID,
		GasPrice:       *gasPrice,
		GasTipCap:      *gasPrice, // Use gasPrice as tip cap for EIP-1559 compatibility
		GasLimit:       *gasLimit,
		ListenAddr:     *listenAddr,
		DatabasePath:   *databasePath,
		BlockTimeMS:    *blockTimeMS,
		ExecutionLayer: *executionLayer,
	}

	// Resolve execution layer capabilities
	cfg.Capabilities = execnode.DefaultRegistry().Get(cfg.ExecutionLayer)
	if cfg.Capabilities == nil {
		logger.Error("unknown execution layer", "layer", cfg.ExecutionLayer,
			"supported", []string{"reth", "op-reth", "gravity-reth", "cdk-erigon"})
		os.Exit(1)
	}
	logger.Info("resolved execution layer capabilities",
		"layer", cfg.ExecutionLayer,
		"hasBlockBuilder", cfg.Capabilities.HasExternalBlockBuilder,
		"supportsPreconf", cfg.Capabilities.SupportsPreconfirmations,
		"requiresLegacyTx", cfg.Capabilities.RequiresLegacyTx)

	// Create load generator
	lg, err := NewLoadGenerator(cfg, store, logger)
	if err != nil {
		logger.Error("failed to create load generator", "error", err)
		os.Exit(1)
	}

	// Handle interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// If TPS flag is set, run in CLI mode (single test)
	if *targetTPS > 0 {
		go func() {
			<-sigChan
			lg.StopTest()
		}()

		err := lg.StartTest(types.StartTestRequest{
			Pattern:      types.LoadPattern(*patternFlag),
			DurationSec:  int(duration.Seconds()),
			NumAccounts:  *numAccounts,
			ConstantRate: *targetTPS,
		})
		if err != nil {
			logger.Error("failed to start test", "error", err)
			os.Exit(1)
		}

		// Wait for completion
		for {
			metrics := lg.GetMetrics()
			if metrics.Status == types.StatusCompleted || metrics.Status == types.StatusError {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Print final metrics
		finalMetrics := lg.GetMetrics()
		logger.Info("test completed",
			"txSent", finalMetrics.TxSent,
			"txConfirmed", finalMetrics.TxConfirmed,
			"txFailed", finalMetrics.TxFailed,
			"avgTPS", finalMetrics.AverageTPS,
		)
		return
	}

	// Server mode - start HTTP API
	go func() {
		<-sigChan
		logger.Info("shutting down...")
		lg.StopTest()
		os.Exit(0)
	}()

	// Create HTTP server
	server := transport.NewServer(lg, lg, logger, cfg.CORSAllowedOrigins)
	mux := server.Handler()

	logger.Info("starting HTTP server", "addr", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}