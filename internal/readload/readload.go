// Package readload generates read-query load (eth_call, eth_getBalance, eth_getLogs,
// eth_getBlockByNumber, eth_getTransactionReceipt) against an RPC endpoint, at a rate
// controlled independently of transaction load.
//
// Why this exists: load tests that only submit transactions characterise the write
// path and say nothing about the read path a production RPC tier mostly serves. A
// measured census over 2.85M requests found 98.2% eth_sendRawTransaction and nine
// eth_calls, which made ZFS ARC data-cache questions unanswerable (PRST-4293).
//
// # Isolation
//
// The engine shares NOTHING with the transaction path except the context and the
// target addresses. In particular it never touches the send-failure counters, the
// pending counter or the rate limiter that the write-side adaptive controller and
// circuit breaker read: a read error must not be able to trip the write circuit
// breaker and silently halve the offered write rate, because the resulting "reads
// lower the write ceiling" conclusion would be an artefact of our own instrumentation.
package readload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/ratelimit"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Defaults applied when the corresponding config field is zero.
const (
	defaultBlockWindow     = 64
	defaultLogsRangeBlocks = 16
	defaultArchiveDepthPct = 100

	// assumedLatencySec sizes the derived worker pool: concurrency must be at least
	// targetRPS * per-request latency or the pool itself caps the achieved rate, which
	// then reads as a server-side limit.
	assumedLatencySec = 0.05
	minConcurrency    = 8
	maxConcurrency    = 2048

	// stopTimeout bounds shutdown. An unbounded wait here deadlocks the test suite if
	// a request never returns — that has happened in this repo before.
	stopTimeout = 5 * time.Second

	// shortfallRatio is the achieved/target ratio below which the run is flagged as
	// unable to offer the requested rate.
	shortfallRatio = 0.95

	// headRefreshInterval is how often the cached chain head is refreshed. Block
	// selection only needs to be approximately current, and this keeps the caller's
	// lock off the per-request path.
	headRefreshInterval = 250 * time.Millisecond
)

// erc20BalanceOfSelector is the 4-byte selector for balanceOf(address).
var erc20BalanceOfSelector = []byte{0x70, 0xa0, 0x82, 0x31}

// transferTopic is keccak256("Transfer(address,address,uint256)"), the ERC-20 Transfer
// event signature used to filter eth_getLogs.
var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// Targets supplies the arguments reads are built from. Addresses and ERC20 are fixed
// for a run; HeadBlock and TxHash are functions because they advance during it.
//
// Randomising over the funded account set is what makes these reads real state reads:
// a random account randomises the storage slot (eth_call) and the account-trie path
// (eth_getBalance), so caches are exercised rather than bypassed. A fixed target such
// as totalSupply() is one hot slot and measures nothing.
type Targets struct {
	Addresses []common.Address
	ERC20     common.Address

	// HeadBlock returns the current chain head. Must be non-nil.
	HeadBlock func() uint64
	// TxHash returns a recently-confirmed transaction hash, or false if none is
	// available yet. Must be non-nil when the mix includes getReceipt.
	TxHash func() (string, bool)
}

// methodStats accumulates per-method counters and latency.
type methodStats struct {
	method string
	count  uint64 // atomic
	errors uint64 // atomic
	lat    *metrics.StreamingLatencyStats
}

// Engine drives read load. Create with New, run with Start, end with Stop.
type Engine struct {
	cfg     types.ReadLoadConfig
	client  rpc.Client
	targets Targets
	limiter *ratelimit.Limiter
	logger  *slog.Logger

	concurrency int

	// picker is a cumulative-weight table over enabled methods. Built once in New and
	// never mutated, so it needs no lock.
	picker []weighted
	// stats is keyed by RPC method name, populated in New and never mutated after, so
	// concurrent reads need no lock (the values are updated atomically).
	stats map[string]*methodStats
	// pooled latency across all methods, for the headline read latency.
	pooledLat *metrics.StreamingLatencyStats

	sent   uint64 // atomic
	errs   uint64 // atomic
	skips  uint64 // atomic - requests skipped for want of a target (e.g. no tx hash yet)
	closed uint64 // atomic - 1 once Stop has run

	// cachedHead is refreshed on a ticker so the per-request path never calls
	// Targets.HeadBlock, which in the real generator sits behind a mutex shared with
	// the block-metrics writer. At thousands of reads per second that contention would
	// be self-inflicted and would show up as read latency.
	cachedHead uint64 // atomic

	archiveTarget bool // set by Probe

	startTime time.Time
	elapsed   time.Duration // frozen at Stop
	mu        sync.Mutex    // guards startTime/elapsed/archiveTarget

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

type weighted struct {
	method     string
	cumulative int
}

// New validates cfg and builds an Engine. It returns an error rather than silently
// degrading: a read whose target is missing (eth_call with no ERC-20 deployed) would
// return success while measuring nothing, which is exactly how a sibling load path
// produced 21,375 gas/tx against an expected 75,700.
func New(cfg types.ReadLoadConfig, client rpc.Client, targets Targets, logger *slog.Logger) (*Engine, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("readload: nil rpc client")
	}
	if targets.HeadBlock == nil {
		return nil, errors.New("readload: Targets.HeadBlock must be set")
	}
	if len(targets.Addresses) == 0 {
		return nil, errors.New("readload: no target addresses (accounts not funded yet?)")
	}
	if cfg.Mix.EthCall > 0 && targets.ERC20 == (common.Address{}) {
		return nil, errors.New("readload: mix.ethCall > 0 but no ERC-20 contract address; " +
			"an eth_call to the zero address returns success and measures nothing")
	}
	if cfg.Mix.GetReceipt > 0 && targets.TxHash == nil {
		return nil, errors.New("readload: mix.getReceipt > 0 but Targets.TxHash is nil")
	}

	e := &Engine{
		cfg:       withDefaults(cfg),
		client:    client,
		targets:   targets,
		logger:    logger,
		stats:     make(map[string]*methodStats),
		pooledLat: metrics.NewStreamingLatencyStats(),
	}

	// Cumulative-weight table. Only methods with a non-zero share are included, and
	// the final entry's cumulative total is the mix sum (validated to be 100), so
	// selection can never fall through to an unintended method — the failure mode that
	// silently turned an unallocated share into 500k-gas heavy-compute transactions on
	// the write side.
	cum := 0
	for _, m := range []struct {
		name  string
		share int
	}{
		{types.ReadMethodCall, cfg.Mix.EthCall},
		{types.ReadMethodGetBalance, cfg.Mix.GetBalance},
		{types.ReadMethodGetLogs, cfg.Mix.GetLogs},
		{types.ReadMethodGetBlock, cfg.Mix.GetBlockByNumber},
		{types.ReadMethodGetReceipt, cfg.Mix.GetReceipt},
	} {
		if m.share <= 0 {
			continue
		}
		cum += m.share
		e.picker = append(e.picker, weighted{method: m.name, cumulative: cum})
		e.stats[m.name] = &methodStats{method: m.name, lat: metrics.NewStreamingLatencyStats()}
	}

	e.limiter = ratelimit.New(float64(e.cfg.TargetRPS))
	e.concurrency = e.cfg.Concurrency
	if e.concurrency <= 0 {
		e.concurrency = int(float64(e.cfg.TargetRPS) * assumedLatencySec)
	}
	if e.concurrency < minConcurrency {
		e.concurrency = minConcurrency
	}
	if e.concurrency > maxConcurrency {
		e.concurrency = maxConcurrency
	}

	return e, nil
}

// Validate checks a read-load config independently of any live target, so the HTTP
// layer can reject a bad request before a test starts.
func Validate(cfg types.ReadLoadConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.TargetRPS <= 0 {
		return fmt.Errorf("readLoad.targetRps must be positive, got %d", cfg.TargetRPS)
	}
	if sum := cfg.Mix.Sum(); sum != 100 {
		return fmt.Errorf("readLoad.mix must sum to 100, got %d", sum)
	}
	for name, v := range map[string]int{
		"ethCall":          cfg.Mix.EthCall,
		"getBalance":       cfg.Mix.GetBalance,
		"getLogs":          cfg.Mix.GetLogs,
		"getBlockByNumber": cfg.Mix.GetBlockByNumber,
		"getReceipt":       cfg.Mix.GetReceipt,
	} {
		if v < 0 || v > 100 {
			return fmt.Errorf("readLoad.mix.%s must be 0-100, got %d", name, v)
		}
	}
	switch cfg.BlockSelection {
	case "", types.ReadBlockLatest, types.ReadBlockRecent, types.ReadBlockArchive:
	default:
		return fmt.Errorf("invalid readLoad.blockSelection: %s (valid: latest, recent, archive)", cfg.BlockSelection)
	}
	if cfg.BlockWindow < 0 {
		return fmt.Errorf("readLoad.blockWindow cannot be negative, got %d", cfg.BlockWindow)
	}
	if cfg.LogsRangeBlocks < 0 {
		return fmt.Errorf("readLoad.logsRangeBlocks cannot be negative, got %d", cfg.LogsRangeBlocks)
	}
	if cfg.ArchiveDepthPct < 0 || cfg.ArchiveDepthPct > 100 {
		return fmt.Errorf("readLoad.archiveDepthPct must be 1-100, got %d", cfg.ArchiveDepthPct)
	}
	if cfg.Concurrency < 0 {
		return fmt.Errorf("readLoad.concurrency cannot be negative, got %d", cfg.Concurrency)
	}
	// A bounded logs span matters: eth_getLogs over a wide range at high tx rates is a
	// self-inflicted denial of service and produces a bimodal latency distribution
	// that says nothing useful.
	if cfg.BlockWindow > 0 && cfg.LogsRangeBlocks > cfg.BlockWindow &&
		cfg.BlockSelection != types.ReadBlockArchive {
		return fmt.Errorf("readLoad.logsRangeBlocks (%d) exceeds blockWindow (%d)",
			cfg.LogsRangeBlocks, cfg.BlockWindow)
	}
	return nil
}

func withDefaults(cfg types.ReadLoadConfig) types.ReadLoadConfig {
	if cfg.BlockSelection == "" {
		cfg.BlockSelection = types.ReadBlockRecent
	}
	if cfg.BlockWindow <= 0 {
		cfg.BlockWindow = defaultBlockWindow
	}
	if cfg.LogsRangeBlocks <= 0 {
		cfg.LogsRangeBlocks = defaultLogsRangeBlocks
	}
	if cfg.ArchiveDepthPct <= 0 {
		cfg.ArchiveDepthPct = defaultArchiveDepthPct
	}
	return cfg
}

// RequireArchive reports whether the run must refuse to start against a non-archive
// target. Defaults to true for archive selection, false otherwise.
func RequireArchive(cfg types.ReadLoadConfig) bool {
	if cfg.RequireArchive != nil {
		return *cfg.RequireArchive
	}
	return cfg.BlockSelection == types.ReadBlockArchive
}

// isStateUnavailable reports whether err is a pruned node refusing a historical state
// query, as opposed to a transport or syntax error. These are the shapes geth, erigon,
// reth and Nitro use.
func isStateUnavailable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		"missing trie node",
		"state not available",
		"state is not available",
		"state unavailable",
		"header not found",
		"block not found",
		"pruned",
		"not found in the database",
		"no state available",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// Probe determines whether the target can serve historical state, and enforces
// RequireArchive. It runs before any load is generated.
//
// Reporting the detected capability on every run is deliberate: an archive result and
// a pruned result must never be silently compared.
func (e *Engine) Probe(ctx context.Context) error {
	head, err := e.client.GetBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("readload: probe could not read head block: %w", err)
	}
	if head == 0 {
		return errors.New("readload: probe found head block 0; chain has no history yet")
	}

	// Query the shallowest block this run would touch. Block 1 for archive mode; for
	// latest/recent the probe is informational, so still use block 1 to learn the
	// target's capability without affecting the decision.
	probeAddr := e.targets.Addresses[0].Hex()
	_, callErr := e.client.Call(ctx, types.ReadMethodGetBalance, []any{probeAddr, hexutil.EncodeUint64(1)})

	archive := callErr == nil
	if callErr != nil && !isStateUnavailable(callErr) {
		// A transport-level failure is not evidence about pruning; surface it rather
		// than mislabelling the node.
		return fmt.Errorf("readload: probe failed for a non-state reason: %w", callErr)
	}

	e.mu.Lock()
	e.archiveTarget = archive
	e.mu.Unlock()

	if !archive && RequireArchive(e.cfg) {
		return fmt.Errorf("readload: blockSelection=%s requires an archive target, but the "+
			"endpoint cannot serve historical state (%v). Point readLoad.rpcUrl at an archive "+
			"node, or use blockSelection=recent. Refusing to start rather than generating an "+
			"error storm and reporting it as latency", e.cfg.BlockSelection, callErr)
	}

	e.logger.Info("read load: target capability probed",
		"archive", archive,
		"blockSelection", e.cfg.BlockSelection,
		"headBlock", head,
		"targetRps", e.cfg.TargetRPS,
		"concurrency", e.concurrency)
	return nil
}

// Start launches the worker pool. It returns immediately; call Stop to end the run.
func (e *Engine) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancel = cancel
	e.startTime = time.Now()
	e.mu.Unlock()

	atomic.StoreUint64(&e.cachedHead, e.targets.HeadBlock())
	e.wg.Add(1)
	go e.headRefresher(ctx)

	for i := 0; i < e.concurrency; i++ {
		e.wg.Add(1)
		go e.worker(ctx, i)
	}
	e.logger.Info("read load started",
		"targetRps", e.cfg.TargetRPS,
		"workers", e.concurrency,
		"blockSelection", e.cfg.BlockSelection)
}

// Stop cancels the workers and waits for them, bounded by stopTimeout. Safe to call
// more than once and safe to call when Start never ran.
func (e *Engine) Stop() {
	if !atomic.CompareAndSwapUint64(&e.closed, 0, 1) {
		return
	}

	e.mu.Lock()
	if !e.startTime.IsZero() {
		e.elapsed = time.Since(e.startTime)
	}
	cancel := e.cancel
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopTimeout):
		e.logger.Warn("read load: stop timeout, some read workers may still be in flight",
			"timeout", stopTimeout)
	}

	m := e.Metrics()
	if m != nil {
		e.logger.Info("read load stopped",
			"readsSent", m.ReadsSent,
			"readErrors", m.ReadErrors,
			"achievedRps", m.ReadRPS,
			"targetRps", m.TargetRPS,
			"rateShortfall", m.RateShortfall)
	}
}

// headRefresher keeps cachedHead current so workers never take the caller's lock.
func (e *Engine) headRefresher(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(headRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			atomic.StoreUint64(&e.cachedHead, e.targets.HeadBlock())
		}
	}
}

// head returns the cached chain head, falling back to a direct read when the cache has
// not been populated yet (which is the case when block selection helpers are exercised
// without a running engine).
func (e *Engine) head() uint64 {
	if h := atomic.LoadUint64(&e.cachedHead); h != 0 {
		return h
	}
	return e.targets.HeadBlock()
}

// worker issues reads at the shared rate limit until the context ends.
func (e *Engine) worker(ctx context.Context, id int) {
	defer e.wg.Done()

	rnd := account.NewRand()

	for {
		if ctx.Err() != nil {
			return
		}
		if err := e.limiter.Wait(ctx); err != nil {
			return // context cancelled
		}

		method, params, ok := e.build(rnd)
		if !ok {
			// No usable target yet (e.g. no confirmed tx hash for getReceipt). Count
			// it so a persistently unbuildable mix is visible rather than looking like
			// a quietly lower rate.
			atomic.AddUint64(&e.skips, 1)
			continue
		}

		st := e.stats[method]
		start := time.Now()
		_, err := e.client.Call(ctx, method, params)
		latMs := float64(time.Since(start).Microseconds()) / 1000.0

		atomic.AddUint64(&st.count, 1)
		atomic.AddUint64(&e.sent, 1)

		if err != nil {
			// A cancellation at shutdown is a boundary artefact, not a read error.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			atomic.AddUint64(&st.errors, 1)
			atomic.AddUint64(&e.errs, 1)
			continue
		}

		// Latency is recorded for successful reads only; an error's latency describes
		// the failure path, and mixing the two makes the percentiles meaningless.
		st.lat.Add(latMs)
		e.pooledLat.Add(latMs)
	}
}

// pick selects a method from the cumulative-weight table.
func (e *Engine) pick(rnd *account.Rand) string {
	roll := rnd.IntN(100)
	for _, w := range e.picker {
		if roll < w.cumulative {
			return w.method
		}
	}
	// Unreachable: the table's final cumulative is the validated mix sum of 100.
	return e.picker[len(e.picker)-1].method
}

// build returns the method and params for one read, or ok=false if no target is
// available yet.
func (e *Engine) build(rnd *account.Rand) (string, []any, bool) {
	method := e.pick(rnd)
	addr := e.targets.Addresses[rnd.IntN(len(e.targets.Addresses))]

	switch method {
	case types.ReadMethodCall:
		data := make([]byte, 0, 4+32)
		data = append(data, erc20BalanceOfSelector...)
		data = append(data, common.LeftPadBytes(addr.Bytes(), 32)...)
		callObj := map[string]any{
			"to":   e.targets.ERC20.Hex(),
			"data": hexutil.Encode(data),
		}
		return method, []any{callObj, e.blockTag(rnd)}, true

	case types.ReadMethodGetBalance:
		return method, []any{addr.Hex(), e.blockTag(rnd)}, true

	case types.ReadMethodGetBlock:
		return method, []any{e.blockTag(rnd), e.cfg.FullBlocks}, true

	case types.ReadMethodGetLogs:
		from, to, ok := e.logRange(rnd)
		if !ok {
			return method, nil, false
		}
		filter := map[string]any{
			"fromBlock": hexutil.EncodeUint64(from),
			"toBlock":   hexutil.EncodeUint64(to),
			"address":   e.targets.ERC20.Hex(),
			"topics":    []any{transferTopic.Hex()},
		}
		return method, []any{filter}, true

	case types.ReadMethodGetReceipt:
		hash, ok := e.targets.TxHash()
		if !ok || hash == "" {
			return method, nil, false
		}
		return method, []any{hash}, true
	}

	return method, nil, false
}

// blockTag returns the block argument for a state read, per the selection mode.
func (e *Engine) blockTag(rnd *account.Rand) any {
	head := e.head()

	switch e.cfg.BlockSelection {
	case types.ReadBlockLatest:
		return "latest"

	case types.ReadBlockArchive:
		if head <= 1 {
			return "latest"
		}
		// Sample the deepest ArchiveDepthPct% of history: depth 100 spans [1, head],
		// smaller values walk the window toward the head, which is how the real
		// pruning horizon is located by sweep.
		span := head * uint64(e.cfg.ArchiveDepthPct) / 100
		if span < 1 {
			span = 1
		}
		return hexutil.EncodeUint64(1 + uint64(rnd.IntN(int(min64(span, head)))))

	default: // ReadBlockRecent
		if head == 0 {
			return "latest"
		}
		window := uint64(e.cfg.BlockWindow)
		if window > head {
			window = head
		}
		if window == 0 {
			return "latest"
		}
		return hexutil.EncodeUint64(head - uint64(rnd.IntN(int(window))))
	}
}

// logRange returns a bounded [from, to] block span for eth_getLogs.
func (e *Engine) logRange(rnd *account.Rand) (uint64, uint64, bool) {
	head := e.head()
	span := uint64(e.cfg.LogsRangeBlocks)
	if head == 0 || span == 0 {
		return 0, 0, false
	}
	if span > head {
		span = head
	}

	var oldest uint64 = 1
	if e.cfg.BlockSelection != types.ReadBlockArchive {
		window := uint64(e.cfg.BlockWindow)
		if window > head {
			window = head
		}
		if window < span {
			window = span
		}
		oldest = head - window + 1
		if oldest < 1 {
			oldest = 1
		}
	}

	latestStart := head - span + 1
	if latestStart < oldest {
		return oldest, head, true
	}
	from := oldest + uint64(rnd.IntN(int(latestStart-oldest+1)))
	return from, from + span - 1, true
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// Metrics returns a snapshot of read-path results. Safe to call at any time.
func (e *Engine) Metrics() *types.ReadLoadMetrics {
	if e == nil {
		return nil
	}

	e.mu.Lock()
	elapsed := e.elapsed
	if elapsed == 0 && !e.startTime.IsZero() {
		elapsed = time.Since(e.startTime)
	}
	archive := e.archiveTarget
	e.mu.Unlock()

	sent := atomic.LoadUint64(&e.sent)
	var rps float64
	if elapsed > 0 {
		rps = float64(sent) / elapsed.Seconds()
	}

	byMethod := make([]types.ReadMethodStats, 0, len(e.stats))
	for _, w := range e.picker { // stable, mix-declaration order
		st := e.stats[w.method]
		byMethod = append(byMethod, types.ReadMethodStats{
			Method:  st.method,
			Count:   atomic.LoadUint64(&st.count),
			Errors:  atomic.LoadUint64(&st.errors),
			Latency: st.lat.GetStats(),
		})
	}

	return &types.ReadLoadMetrics{
		TargetRPS:      e.cfg.TargetRPS,
		ReadRPS:        rps,
		ReadsSent:      sent,
		ReadErrors:     atomic.LoadUint64(&e.errs),
		BlockSelection: e.cfg.BlockSelection,
		ArchiveTarget:  archive,
		RateShortfall:  elapsed > 0 && rps < float64(e.cfg.TargetRPS)*shortfallRatio,
		Latency:        e.pooledLat.GetStats(),
		ByMethod:       byMethod,
	}
}

// Skipped returns the number of reads skipped for want of a target. Exposed for tests
// and diagnostics.
func (e *Engine) Skipped() uint64 { return atomic.LoadUint64(&e.skips) }
