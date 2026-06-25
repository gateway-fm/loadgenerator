package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/config"
	"github.com/gateway-fm/loadgenerator/internal/execnode"
	"github.com/gateway-fm/loadgenerator/internal/loadgen"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/transport"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func main() {
	builderURL := flag.String("builder", loadgen.GetEnvOrDefault("BUILDER_RPC_URL", "http://localhost:13000"), "Block builder RPC URL")
	l2URL := flag.String("l2", loadgen.GetEnvOrDefault("L2_RPC_URL", "http://localhost:13000"), "L2 RPC URL")
	preconfWS := flag.String("preconf-ws", loadgen.GetEnvOrDefault("PRECONF_WS_URL", ""), "Preconfirmation WebSocket URL")
	chainID := flag.Int64("chainid", 42069, "Chain ID")
	gasPrice := flag.Int64("gasprice", 1000000000, "Gas price in wei (default 1 gwei)")
	gasLimit := flag.Uint64("gaslimit", 21000, "Gas limit per transaction")
	listenAddr := flag.String("listen", loadgen.GetEnvOrDefault("LISTEN_ADDR", ":3001"), "HTTP API listen address")
	databasePath := flag.String("database", loadgen.GetEnvOrDefault("DATABASE_PATH", "./data/loadgen.db"), "SQLite database path")

	blockTimeMS := flag.Int("block-time-ms", loadgen.GetEnvIntOrDefault("BLOCK_TIME_MS", config.DefaultBlockTimeMS), "Block time in milliseconds (for account scaling)")

	executionLayer := flag.String("execution-layer", loadgen.GetEnvOrDefault("EXECUTION_LAYER", "reth"), "Execution layer (reth, op-reth, gravity-reth, cdk-erigon)")

	patternFlag := flag.String("pattern", "constant", "Load pattern (constant, ramp, spike, max)")
	targetTPS := flag.Int("tps", 0, "Target TPS for constant pattern (CLI mode - if set, runs single test)")
	duration := flag.Duration("duration", 30*time.Second, "Test duration (CLI mode)")
	numAccounts := flag.Int("accounts", 0, "Number of accounts (0=auto-calculate based on TPS)")

	logLevel := flag.String("log-level", loadgen.GetEnvOrDefault("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")

	flag.Parse()

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

	go func() {
		logger.Info("pprof listening", "addr", "localhost:6061")
		if err := http.ListenAndServe("localhost:6061", nil); err != nil {
			logger.Error("pprof server failed", "error", err)
		}
	}()

	store, err := storage.NewSQLiteStorage(*databasePath)
	if err != nil {
		logger.Error("failed to initialize storage", "error", err, "path", *databasePath)
		os.Exit(1)
	}
	defer store.Close()
	logger.Info("initialized storage", "path", *databasePath)

	// Gas pricing: env vars override flag defaults for gasless mode support
	gasTipCap := *gasPrice // Default: use gasPrice as tip cap
	var gasFeeCap int64    // 0 = auto-calculate from chain
	if v := os.Getenv("GAS_TIP_CAP"); v != "" {
		if tip, err := strconv.ParseInt(v, 10, 64); err == nil && tip >= 0 {
			gasTipCap = tip
		}
	}
	if v := os.Getenv("GAS_FEE_CAP"); v != "" {
		if fee, err := strconv.ParseInt(v, 10, 64); err == nil && fee >= 0 {
			gasFeeCap = fee
		}
	}
	cfg := &config.Config{
		BuilderRPCURL:        *builderURL,
		L2RPCURL:             *l2URL,
		PreconfWSURL:         *preconfWS,
		ChainID:              *chainID,
		GasPrice:             *gasPrice,
		GasTipCap:            gasTipCap,
		GasFeeCap:            gasFeeCap,
		GasLimit:             *gasLimit,
		ListenAddr:           *listenAddr,
		DatabasePath:         *databasePath,
		BlockTimeMS:          *blockTimeMS,
		ExecutionLayer:       *executionLayer,
		PrivacyRPCURL:        os.Getenv("PRIVACY_RPC_URL"),
		PrivacyAuthTokenFile: os.Getenv("PRIVACY_AUTH_TOKEN_FILE"),
		PrivacyOrgIDFile:     os.Getenv("PRIVACY_ORG_ID_FILE"),
	}

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

	lg, err := loadgen.NewLoadGenerator(cfg, store, logger)
	if err != nil {
		logger.Error("failed to create load generator", "error", err)
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

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

		for {
			metrics := lg.GetMetrics()
			if metrics.Status == types.StatusCompleted || metrics.Status == types.StatusError {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		finalMetrics := lg.GetMetrics()
		logger.Info("test completed",
			"txSent", finalMetrics.TxSent,
			"txConfirmed", finalMetrics.TxConfirmed,
			"txFailed", finalMetrics.TxFailed,
			"avgTPS", finalMetrics.AverageTPS,
		)
		return
	}

	go func() {
		<-sigChan
		logger.Info("shutting down...")
		lg.StopTest()
		os.Exit(0)
	}()

	server := transport.NewServer(lg, lg, logger, cfg.CORSAllowedOrigins)
	mux := server.Handler()

	logger.Info("starting HTTP server", "addr", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}