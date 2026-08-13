package loadgen

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/readload"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

const (
	// readClientTimeout is generous compared with the send path's 2s: a bounded
	// eth_getLogs span is legitimately slow, and clipping it would report a real
	// (if dear) read as an error.
	readClientTimeout = 10 * time.Second

	// readClientRetries is deliberately ZERO. Retrying a read would both understate
	// the error rate and push the offered rate above targetRps, so a failed read is
	// counted as a failure instead.
	readClientRetries = 0

	// readProbeTimeout bounds the pre-run capability probe.
	readProbeTimeout = 15 * time.Second
)

// setupReadLoad builds and probes the read-load engine when the request asks for it.
// It runs BEFORE the sender workers start, so an unsuitable target (e.g. archive
// selection against a pruned node) aborts the test cleanly rather than mid-run.
//
// Returns nil and leaves lg.readEngine nil when read load is not requested — the
// write-only path then allocates nothing and behaves exactly as before.
func (lg *LoadGenerator) setupReadLoad(req types.StartTestRequest) error {
	lg.readEngine.Store(nil)

	if req.ReadLoad == nil || !req.ReadLoad.Enabled {
		return nil
	}
	cfg := *req.ReadLoad

	// Reads get their own client, and therefore their own connection pool, even when
	// pointed at the same URL as writes. Sharing the send/confirm pool would let read
	// concurrency starve transaction submission, which would look like a chain limit.
	url := cfg.RPCURL
	if url == "" {
		url = lg.cfg.L2RPCURL
	}
	clientCfg := rpc.DefaultClientConfig(url)
	clientCfg.Timeout = readClientTimeout
	clientCfg.MaxRetries = readClientRetries
	clientCfg.Logger = lg.logger
	readClient := rpc.NewHTTPClient(clientCfg)

	lg.contractsMu.RLock()
	erc20 := lg.erc20Contract
	lg.contractsMu.RUnlock()

	// Build a fresh slice rather than appending onto what GetAccounts returns: that
	// slice is the manager's own, and appending into its spare capacity would write
	// through to the manager's backing array. contracts.go joins the two the same way.
	builtIn := lg.accountMgr.GetAccounts()
	dynamic := lg.accountMgr.GetDynamicAccounts()
	addrs := make([]common.Address, 0, len(builtIn)+len(dynamic))
	for _, acc := range builtIn {
		addrs = append(addrs, acc.Address)
	}
	for _, acc := range dynamic {
		addrs = append(addrs, acc.Address)
	}

	targets := readload.Targets{
		Addresses: addrs,
		ERC20:     erc20,
		HeadBlock: func() uint64 { return lg.readHeadBlock(readClient) },
		TxHash:    lg.sampleConfirmedTxHash,
	}

	engine, err := readload.New(cfg, readClient, targets, lg.logger)
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), readProbeTimeout)
	defer cancel()
	if err := engine.Probe(probeCtx); err != nil {
		return err
	}

	lg.readEngine.Store(engine)
	lg.logger.Info("read load configured",
		"url", url,
		"targetRps", cfg.TargetRPS,
		"blockSelection", cfg.BlockSelection,
		"accounts", len(addrs))
	return nil
}

// readHeadBlock reports the chain head for read block selection. It prefers the head
// the test is already tracking, and falls back to an RPC lookup when nothing has been
// observed yet — otherwise a run without a block-metrics WebSocket would silently
// degrade every state read to "latest" and stop exercising random state.
//
// Only the engine's 250ms head refresher calls this, so the RPC fallback is cheap.
func (lg *LoadGenerator) readHeadBlock(client rpc.Client) uint64 {
	lg.blockMetricsMu.Lock()
	head := lg.lastBlockNumber
	if lg.rpcLastBlockNumber > head {
		head = lg.rpcLastBlockNumber
	}
	lg.blockMetricsMu.Unlock()

	if head > 0 {
		return head
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if n, err := client.GetBlockNumber(ctx); err == nil {
		return n
	}
	return 0
}

// sampleConfirmedTxHash returns a recently-confirmed transaction hash for
// eth_getTransactionReceipt, or false when none is available.
//
// Sampled UNIFORMLY over the buffer rather than always taking the newest entry: every
// entry is equally inside a pruned node's horizon, and always returning the newest would
// point every concurrent getReceipt read at one hash — the same mistake as calling
// totalSupply() for eth_call, which measures a cache rather than the read path.
//
// The slice is read without clearing it: incremental verification drains the same
// buffer, so it is legitimately empty at times, and the engine counts those as skips
// rather than pretending to have issued a read.
func (lg *LoadGenerator) sampleConfirmedTxHash() (string, bool) {
	lg.recentConfirmedMu.Lock()
	defer lg.recentConfirmedMu.Unlock()

	n := len(lg.recentConfirmedHashes)
	if n == 0 {
		return "", false
	}
	return lg.recentConfirmedHashes[rand.IntN(n)], true
}

// startReadLoad begins read traffic. Safe to call when read load is not configured.
func (lg *LoadGenerator) startReadLoad() {
	if e := lg.readEngine.Load(); e != nil {
		e.Start(lg.ctx)
	}
}

// stopReadLoad ends read traffic. Safe to call when read load is not configured or
// already stopped.
func (lg *LoadGenerator) stopReadLoad() {
	if e := lg.readEngine.Load(); e != nil {
		e.Stop()
	}
}

// readLoadMetrics returns a read-path snapshot, or nil when read load is not enabled.
func (lg *LoadGenerator) readLoadMetrics() *types.ReadLoadMetrics {
	// Metrics() is nil-safe, but Load() may legitimately return nil before setup runs.
	return lg.readEngine.Load().Metrics()
}
