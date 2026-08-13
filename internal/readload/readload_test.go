package readload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// mockClient implements rpc.Client by embedding the interface: only the methods the
// read engine actually uses are defined, and any accidental use of another method
// panics loudly rather than silently succeeding.
type mockClient struct {
	rpc.Client

	head     uint64
	callFn   func(ctx context.Context, method string, params []any) (json.RawMessage, error)
	callCnt  int64 // atomic
	headErr  error
	lastCall atomic.Value // string
}

func (m *mockClient) Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	atomic.AddInt64(&m.callCnt, 1)
	m.lastCall.Store(method)
	if m.callFn != nil {
		return m.callFn(ctx, method, params)
	}
	return json.RawMessage(`"0x0"`), nil
}

func (m *mockClient) GetBlockNumber(_ context.Context) (uint64, error) {
	if m.headErr != nil {
		return 0, m.headErr
	}
	return m.head, nil
}

func (m *mockClient) calls() int64 { return atomic.LoadInt64(&m.callCnt) }

func testTargets(head uint64) Targets {
	addrs := make([]common.Address, 16)
	for i := range addrs {
		addrs[i] = common.BigToAddress(common.Big1)
		addrs[i][19] = byte(i + 1)
	}
	return Targets{
		Addresses: addrs,
		ERC20:     common.HexToAddress("0x00000000000000000000000000000000000000ff"),
		HeadBlock: func() uint64 { return head },
		TxHash:    func() (string, bool) { return "0xabc", true },
	}
}

func baseCfg() types.ReadLoadConfig {
	return types.ReadLoadConfig{
		Enabled:   true,
		TargetRPS: 100,
		Mix: types.ReadMix{
			EthCall: 60, GetBalance: 15, GetLogs: 10, GetBlockByNumber: 10, GetReceipt: 5,
		},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*types.ReadLoadConfig)
		wantErr string
	}{
		{"valid", func(c *types.ReadLoadConfig) {}, ""},
		{"disabled skips all checks", func(c *types.ReadLoadConfig) {
			c.Enabled = false
			c.TargetRPS = 0
			c.Mix = types.ReadMix{}
		}, ""},
		{"zero rps", func(c *types.ReadLoadConfig) { c.TargetRPS = 0 }, "targetRps must be positive"},
		{"mix under 100", func(c *types.ReadLoadConfig) { c.Mix.EthCall = 10 }, "must sum to 100"},
		{"mix over 100", func(c *types.ReadLoadConfig) { c.Mix.EthCall = 90 }, "must sum to 100"},
		// Sum deliberately still 100, so the range check is what fires rather than the
		// sum check masking it.
		{"negative share", func(c *types.ReadLoadConfig) {
			c.Mix.EthCall = 80
			c.Mix.GetLogs = -10
		}, "must be 0-100"},
		{"bad selection", func(c *types.ReadLoadConfig) { c.BlockSelection = "ancient" }, "invalid readLoad.blockSelection"},
		{"archive depth out of range", func(c *types.ReadLoadConfig) { c.ArchiveDepthPct = 101 }, "archiveDepthPct must be 1-100"},
		{"negative concurrency", func(c *types.ReadLoadConfig) { c.Concurrency = -1 }, "concurrency cannot be negative"},
		{"logs range exceeds window", func(c *types.ReadLoadConfig) {
			c.BlockWindow = 10
			c.LogsRangeBlocks = 50
		}, "exceeds blockWindow"},
		{"logs range may exceed window in archive mode", func(c *types.ReadLoadConfig) {
			c.BlockSelection = types.ReadBlockArchive
			c.BlockWindow = 10
			c.LogsRangeBlocks = 50
		}, ""},
		{"selection latest is valid", func(c *types.ReadLoadConfig) { c.BlockSelection = types.ReadBlockLatest }, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg()
			tc.mutate(&cfg)
			err := Validate(cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

// A read whose target is missing would return success and measure nothing. That is the
// failure mode that produced 21,375 gas/tx on the write side, so it must be refused.
func TestNewRejectsMissingTargets(t *testing.T) {
	t.Run("ethCall without ERC20", func(t *testing.T) {
		tg := testTargets(1000)
		tg.ERC20 = common.Address{}
		if _, err := New(baseCfg(), &mockClient{head: 1000}, tg, nil); err == nil {
			t.Fatal("expected error when mix.ethCall > 0 and no ERC-20 address")
		}
	})

	t.Run("no addresses", func(t *testing.T) {
		tg := testTargets(1000)
		tg.Addresses = nil
		if _, err := New(baseCfg(), &mockClient{head: 1000}, tg, nil); err == nil {
			t.Fatal("expected error with no target addresses")
		}
	})

	t.Run("nil HeadBlock", func(t *testing.T) {
		tg := testTargets(1000)
		tg.HeadBlock = nil
		if _, err := New(baseCfg(), &mockClient{head: 1000}, tg, nil); err == nil {
			t.Fatal("expected error with nil HeadBlock")
		}
	})

	t.Run("getReceipt without TxHash", func(t *testing.T) {
		tg := testTargets(1000)
		tg.TxHash = nil
		if _, err := New(baseCfg(), &mockClient{head: 1000}, tg, nil); err == nil {
			t.Fatal("expected error when mix.getReceipt > 0 and TxHash is nil")
		}
	})

	t.Run("nil client", func(t *testing.T) {
		if _, err := New(baseCfg(), nil, testTargets(1000), nil); err == nil {
			t.Fatal("expected error with nil client")
		}
	})

	t.Run("missing ERC20 is fine when ethCall share is zero", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Mix = types.ReadMix{GetBalance: 100}
		tg := testTargets(1000)
		tg.ERC20 = common.Address{}
		if _, err := New(cfg, &mockClient{head: 1000}, tg, nil); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})
}

// pick must never return a method with a zero share. The write-side equivalent of this
// bug silently routed all unallocated probability into 500k-gas heavy-compute txs.
func TestPickRespectsMixAndNeverFallsThrough(t *testing.T) {
	cfg := baseCfg()
	cfg.Mix = types.ReadMix{EthCall: 50, GetBalance: 50} // getLogs/getBlock/getReceipt = 0

	e, err := New(cfg, &mockClient{head: 1000}, testTargets(1000), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rnd := account.NewRand()
	counts := map[string]int{}
	const n = 20000
	for i := 0; i < n; i++ {
		counts[e.pick(rnd)]++
	}

	for _, banned := range []string{types.ReadMethodGetLogs, types.ReadMethodGetBlock, types.ReadMethodGetReceipt} {
		if counts[banned] != 0 {
			t.Errorf("method %s had zero share but was selected %d times", banned, counts[banned])
		}
	}
	// Each 50% bucket should land near half; generous bounds to stay deterministic.
	for _, m := range []string{types.ReadMethodCall, types.ReadMethodGetBalance} {
		share := float64(counts[m]) / float64(n)
		if share < 0.45 || share > 0.55 {
			t.Errorf("method %s share %.3f outside [0.45,0.55]", m, share)
		}
	}
}

func TestBlockTagSelection(t *testing.T) {
	const head = 10000
	rnd := account.NewRand()

	t.Run("latest", func(t *testing.T) {
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockLatest
		e, err := New(cfg, &mockClient{head: head}, testTargets(head), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		for i := 0; i < 100; i++ {
			if got := e.blockTag(rnd); got != "latest" {
				t.Fatalf("expected \"latest\", got %v", got)
			}
		}
	})

	t.Run("recent stays inside the window", func(t *testing.T) {
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockRecent
		cfg.BlockWindow = 64
		e, err := New(cfg, &mockClient{head: head}, testTargets(head), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		for i := 0; i < 2000; i++ {
			n := decodeTag(t, e.blockTag(rnd))
			if n > head || n < head-64+1 {
				t.Fatalf("block %d outside recent window [%d,%d]", n, head-64+1, head)
			}
		}
	})

	t.Run("archive spans full history", func(t *testing.T) {
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockArchive
		cfg.ArchiveDepthPct = 100
		e, err := New(cfg, &mockClient{head: head}, testTargets(head), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		var sawDeep bool
		for i := 0; i < 5000; i++ {
			n := decodeTag(t, e.blockTag(rnd))
			if n < 1 || n > head {
				t.Fatalf("block %d outside [1,%d]", n, head)
			}
			if n < head/10 {
				sawDeep = true
			}
		}
		if !sawDeep {
			t.Error("archive selection never sampled deep history; it would not exercise cold state")
		}
	})

	t.Run("archive depth narrows the window", func(t *testing.T) {
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockArchive
		cfg.ArchiveDepthPct = 10
		e, err := New(cfg, &mockClient{head: head}, testTargets(head), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		for i := 0; i < 2000; i++ {
			n := decodeTag(t, e.blockTag(rnd))
			if n < 1 || n > head/10 {
				t.Fatalf("depth 10%% sampled block %d, expected <= %d", n, head/10)
			}
		}
	})

	t.Run("empty chain degrades to latest", func(t *testing.T) {
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockRecent
		tg := testTargets(0)
		e, err := New(cfg, &mockClient{}, tg, nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if got := e.blockTag(rnd); got != "latest" {
			t.Fatalf("expected \"latest\" on empty chain, got %v", got)
		}
	})
}

func decodeTag(t *testing.T, tag any) uint64 {
	t.Helper()
	s, ok := tag.(string)
	if !ok {
		t.Fatalf("block tag was not a string: %#v", tag)
	}
	if s == "latest" {
		t.Fatalf("unexpected \"latest\" tag")
	}
	var n uint64
	if _, err := fmt.Sscanf(s, "0x%x", &n); err != nil {
		t.Fatalf("cannot parse block tag %q: %v", s, err)
	}
	return n
}

// eth_getLogs over a wide range is a self-inflicted DoS and yields a bimodal latency
// distribution, so the span must stay bounded.
func TestLogRangeIsBounded(t *testing.T) {
	const head = 5000
	rnd := account.NewRand()

	cfg := baseCfg()
	cfg.BlockSelection = types.ReadBlockRecent
	cfg.BlockWindow = 64
	cfg.LogsRangeBlocks = 16

	e, err := New(cfg, &mockClient{head: head}, testTargets(head), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 2000; i++ {
		from, to, ok := e.logRange(rnd)
		if !ok {
			t.Fatal("logRange not ok on a chain with history")
		}
		if to < from {
			t.Fatalf("inverted range [%d,%d]", from, to)
		}
		if span := to - from + 1; span > 16 {
			t.Fatalf("span %d exceeds logsRangeBlocks 16", span)
		}
		if to > head || from < head-64+1 {
			t.Fatalf("range [%d,%d] outside window [%d,%d]", from, to, head-64+1, head)
		}
	}
}

func TestProbe(t *testing.T) {
	t.Run("archive target detected", func(t *testing.T) {
		mc := &mockClient{head: 5000}
		e, err := New(baseCfg(), mc, testTargets(5000), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := e.Probe(context.Background()); err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if !e.Metrics().ArchiveTarget {
			t.Error("expected ArchiveTarget true when historical state is served")
		}
	})

	t.Run("pruned target detected but allowed in recent mode", func(t *testing.T) {
		mc := &mockClient{head: 5000, callFn: func(_ context.Context, _ string, _ []any) (json.RawMessage, error) {
			return nil, errors.New("missing trie node 0xdead (path )")
		}}
		e, err := New(baseCfg(), mc, testTargets(5000), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := e.Probe(context.Background()); err != nil {
			t.Fatalf("recent mode must tolerate a pruned target: %v", err)
		}
		if e.Metrics().ArchiveTarget {
			t.Error("expected ArchiveTarget false for a pruned node")
		}
	})

	// Generating an error storm and reporting it as latency percentiles is
	// indistinguishable from a performance result, so archive mode must refuse.
	t.Run("archive mode refuses a pruned target", func(t *testing.T) {
		mc := &mockClient{head: 5000, callFn: func(_ context.Context, _ string, _ []any) (json.RawMessage, error) {
			return nil, errors.New("state not available for block 1")
		}}
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockArchive
		e, err := New(cfg, mc, testTargets(5000), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = e.Probe(context.Background())
		if err == nil {
			t.Fatal("expected Probe to refuse archive selection against a pruned target")
		}
		if !strings.Contains(err.Error(), "requires an archive target") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("requireArchive=false overrides the refusal", func(t *testing.T) {
		mc := &mockClient{head: 5000, callFn: func(_ context.Context, _ string, _ []any) (json.RawMessage, error) {
			return nil, errors.New("missing trie node")
		}}
		cfg := baseCfg()
		cfg.BlockSelection = types.ReadBlockArchive
		no := false
		cfg.RequireArchive = &no
		e, err := New(cfg, mc, testTargets(5000), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := e.Probe(context.Background()); err != nil {
			t.Fatalf("expected probe to pass with requireArchive=false: %v", err)
		}
	})

	// A transport failure is not evidence about pruning; mislabelling it would put a
	// wrong archive/non-archive label on every result from the run.
	t.Run("transport error is not read as pruning", func(t *testing.T) {
		mc := &mockClient{head: 5000, callFn: func(_ context.Context, _ string, _ []any) (json.RawMessage, error) {
			return nil, errors.New("connection refused")
		}}
		e, err := New(baseCfg(), mc, testTargets(5000), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = e.Probe(context.Background())
		if err == nil || !strings.Contains(err.Error(), "non-state reason") {
			t.Fatalf("expected a non-state probe failure, got %v", err)
		}
	})

	t.Run("empty chain is refused", func(t *testing.T) {
		e, err := New(baseCfg(), &mockClient{head: 0}, testTargets(0), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := e.Probe(context.Background()); err == nil {
			t.Fatal("expected Probe to fail on a chain with no history")
		}
	})
}

func TestEngineRunsAndReports(t *testing.T) {
	mc := &mockClient{head: 5000}
	cfg := baseCfg()
	cfg.TargetRPS = 2000
	cfg.Concurrency = 16

	e, err := New(cfg, mc, testTargets(5000), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.Start(context.Background())
	time.Sleep(300 * time.Millisecond)
	e.Stop()

	m := e.Metrics()
	if m == nil {
		t.Fatal("nil metrics")
	}
	if m.ReadsSent == 0 {
		t.Fatal("no reads were issued")
	}
	if mc.calls() == 0 {
		t.Fatal("client was never called")
	}
	if m.ReadErrors != 0 {
		t.Errorf("expected no read errors, got %d", m.ReadErrors)
	}
	if m.Latency == nil {
		t.Error("expected pooled latency stats")
	}
	if len(m.ByMethod) != 5 {
		t.Errorf("expected per-method stats for 5 methods, got %d", len(m.ByMethod))
	}
	var total uint64
	for _, ms := range m.ByMethod {
		total += ms.Count
	}
	if total != m.ReadsSent {
		t.Errorf("per-method counts %d do not sum to readsSent %d", total, m.ReadsSent)
	}
	if m.TargetRPS != 2000 {
		t.Errorf("expected TargetRPS 2000, got %d", m.TargetRPS)
	}
}

func TestEngineCountsErrorsSeparatelyFromLatency(t *testing.T) {
	mc := &mockClient{head: 5000, callFn: func(_ context.Context, _ string, _ []any) (json.RawMessage, error) {
		return nil, errors.New("execution reverted")
	}}
	cfg := baseCfg()
	cfg.TargetRPS = 1000
	cfg.Concurrency = 8

	e, err := New(cfg, mc, testTargets(5000), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.Start(context.Background())
	time.Sleep(200 * time.Millisecond)
	e.Stop()

	m := e.Metrics()
	if m.ReadErrors == 0 {
		t.Fatal("expected read errors to be counted")
	}
	if m.ReadErrors != m.ReadsSent {
		t.Errorf("every read failed, so errors (%d) should equal sent (%d)", m.ReadErrors, m.ReadsSent)
	}
	// Latency of a failure describes the failure path; mixing it into the percentiles
	// makes them meaningless, so nothing should have been recorded.
	if m.Latency != nil {
		t.Errorf("expected no latency samples when every read errored, got %+v", m.Latency)
	}
}

// getReceipt with no confirmed hash yet must be skipped and counted, not silently
// lower the offered rate.
func TestUnavailableTargetIsSkippedAndCounted(t *testing.T) {
	cfg := baseCfg()
	cfg.Mix = types.ReadMix{GetReceipt: 100}
	cfg.TargetRPS = 1000
	cfg.Concurrency = 8

	tg := testTargets(5000)
	tg.TxHash = func() (string, bool) { return "", false }

	e, err := New(cfg, &mockClient{head: 5000}, tg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.Start(context.Background())
	time.Sleep(150 * time.Millisecond)
	e.Stop()

	if e.Skipped() == 0 {
		t.Fatal("expected skipped reads to be counted")
	}
	if e.Metrics().ReadsSent != 0 {
		t.Errorf("expected no reads sent when no target was available, got %d", e.Metrics().ReadsSent)
	}
}

func TestStopIsIdempotentAndBounded(t *testing.T) {
	e, err := New(baseCfg(), &mockClient{head: 100}, testTargets(100), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.Start(context.Background())

	done := make(chan struct{})
	go func() {
		e.Stop()
		e.Stop() // second call must be a no-op, not a panic or a hang
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(stopTimeout + 3*time.Second):
		t.Fatal("Stop did not return within its bound")
	}
}

func TestStopWithoutStart(t *testing.T) {
	e, err := New(baseCfg(), &mockClient{head: 100}, testTargets(100), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.Stop() // must not panic
	if m := e.Metrics(); m == nil {
		t.Fatal("expected metrics even without a run")
	}
}

func TestParentContextCancellationStopsWorkers(t *testing.T) {
	e, err := New(baseCfg(), &mockClient{head: 100}, testTargets(100), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() { e.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopTimeout + 3*time.Second):
		t.Fatal("workers did not exit on parent context cancellation")
	}
}

func TestConcurrencyDerivation(t *testing.T) {
	tests := []struct {
		rps, explicit, wantMin, wantMax int
	}{
		{100, 0, minConcurrency, minConcurrency}, // 100*0.05=5 -> floor
		{2000, 0, 100, 100},                      // 2000*0.05
		{100000, 0, maxConcurrency, maxConcurrency},
		{500, 64, 64, 64}, // explicit wins
	}
	for _, tc := range tests {
		cfg := baseCfg()
		cfg.TargetRPS = tc.rps
		cfg.Concurrency = tc.explicit
		e, err := New(cfg, &mockClient{head: 10}, testTargets(10), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if e.concurrency < tc.wantMin || e.concurrency > tc.wantMax {
			t.Errorf("rps=%d explicit=%d: concurrency %d outside [%d,%d]",
				tc.rps, tc.explicit, e.concurrency, tc.wantMin, tc.wantMax)
		}
	}
}

func TestBuildProducesWellFormedParams(t *testing.T) {
	rnd := account.NewRand()
	cfg := baseCfg()
	cfg.BlockSelection = types.ReadBlockLatest
	e, err := New(cfg, &mockClient{head: 5000}, testTargets(5000), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		method, params, ok := e.build(rnd)
		if !ok {
			t.Fatalf("build failed for %s", method)
		}
		seen[method] = true

		switch method {
		case types.ReadMethodCall:
			obj, isMap := params[0].(map[string]any)
			if !isMap {
				t.Fatalf("eth_call first param is not an object: %#v", params[0])
			}
			data, _ := obj["data"].(string)
			// 0x + 4-byte selector + 32-byte address argument
			if len(data) != 2+8+64 {
				t.Fatalf("eth_call data has wrong length %d: %s", len(data), data)
			}
			if !strings.HasPrefix(data, "0x70a08231") {
				t.Fatalf("eth_call is not balanceOf: %s", data)
			}
		case types.ReadMethodGetLogs:
			f, isMap := params[0].(map[string]any)
			if !isMap {
				t.Fatalf("eth_getLogs first param is not an object")
			}
			for _, k := range []string{"fromBlock", "toBlock", "address", "topics"} {
				if _, present := f[k]; !present {
					t.Fatalf("eth_getLogs filter missing %q", k)
				}
			}
		case types.ReadMethodGetBlock:
			if len(params) != 2 {
				t.Fatalf("eth_getBlockByNumber wants 2 params, got %d", len(params))
			}
			if _, isBool := params[1].(bool); !isBool {
				t.Fatalf("eth_getBlockByNumber second param must be bool")
			}
		}
	}

	if len(seen) != 5 {
		t.Errorf("expected all 5 methods exercised, saw %d", len(seen))
	}
}

func TestIsStateUnavailable(t *testing.T) {
	for _, s := range []string{
		"missing trie node 0xabc",
		"State not available",
		"header not found",
		"block not found",
		"this state is pruned",
	} {
		if !isStateUnavailable(errors.New(s)) {
			t.Errorf("expected %q to be classified as state-unavailable", s)
		}
	}
	for _, s := range []string{
		"connection refused",
		"execution reverted",
		"context deadline exceeded",
		"429 too many requests",
	} {
		if isStateUnavailable(errors.New(s)) {
			t.Errorf("did not expect %q to be classified as state-unavailable", s)
		}
	}
	if isStateUnavailable(nil) {
		t.Error("nil must not be state-unavailable")
	}
}

func TestRequireArchiveDefaults(t *testing.T) {
	cfg := baseCfg()
	cfg.BlockSelection = types.ReadBlockArchive
	if !RequireArchive(cfg) {
		t.Error("archive selection must require an archive target by default")
	}

	cfg.BlockSelection = types.ReadBlockRecent
	if RequireArchive(cfg) {
		t.Error("recent selection must not require an archive target")
	}

	yes := true
	cfg.RequireArchive = &yes
	if !RequireArchive(cfg) {
		t.Error("explicit requireArchive=true must be honoured in recent mode")
	}
}
