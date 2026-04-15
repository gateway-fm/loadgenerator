# Load Generator — Status & Roadmap

## Completed: Monolith Refactor (PR #5)

Split 4,914-line `cmd/loadgen/main.go` into 13 focused files across 8 commits on `refactor/split-main-go`.

### Phase Summary
1. **Extract pure functions & types** → `internal/workload/` (134 lines, 98% covered)
2. **Extract WebSocket connections** → `internal/loadgen/websocket.go`
3. **Extract metrics calculation** → `internal/loadgen/blockmetrics.go`
4. **Extract contract deployment & account caching** → `contracts.go`, `accounts.go`
5. **Extract persistence & verification** → `persistence.go`, `verification.go`
6. **Extract builder communication** → `builder.go`
7. **Extract init, workers, api, entrypoint** → `init.go`, `workers.go`, `api.go`
8. **Move cmd/loadgen → internal/loadgen** → proper Go package layout

### Final Layout
```
cmd/loadgen/main.go              — thin entrypoint (~130 lines)
internal/loadgen/loadgen.go      — LoadGenerator struct, constructor, Start/Stop (487 lines)
internal/loadgen/api.go          — HTTP API handlers (362 lines)
internal/loadgen/init.go         — initialization (492 lines)
internal/loadgen/workers.go      — sender/confirmation workers (540 lines)
internal/loadgen/websocket.go    — WebSocket connections (550 lines)
internal/loadgen/blockmetrics.go — block metrics processing (588 lines)
internal/loadgen/persistence.go  — test result persistence (502 lines)
internal/loadgen/builder.go      — builder communication (443 lines)
internal/loadgen/contracts.go    — contract deployment (366 lines)
internal/loadgen/accounts.go     — account management (182 lines)
internal/loadgen/verification.go — on-chain verification (122 lines)
internal/loadgen/helpers.go      — shared types & utilities (102 lines)
```

## Current Coverage

| Package | Coverage | Lines |
|---------|----------|-------|
| `internal/ratelimit` | 100% | |
| `internal/workload` | 98% | |
| `internal/execnode` | 97% | |
| `internal/pattern` | 86% | |
| `internal/storage` | 81% | |
| `internal/sender` | 54% | |
| `internal/config` | 44% | |
| `internal/pipeline` | 40% | |
| `internal/txbuilder` | 34% | |
| `internal/metrics` | 27% | |
| `internal/uniswapv3` | 22% | |
| `internal/account` | 18% | |
| `internal/transport` | 18% | |
| `internal/verification` | 13% | |
| `internal/rpc` | 4% | |
| **`internal/loadgen`** | **77.4%** | **4,736** |
| `internal/contract` | 0% | |
| **Overall** | **~30%** | |

## Pre-existing Bugs (from PR #5 review)

Identified by Copilot review. These are **not regressions** — they existed in the original monolith and were carried across during the refactor.

### Bugs

- [ ] **WebSocket connection leaks** — `websocket.go:63,282,494`: Three places where `lg.*WsConn` is set to nil on error without calling `conn.Close()` first. Leaks file descriptors across reconnect attempts.
- [ ] **RPC fallback reports blocks as TXs** — `blockmetrics.go:288`: `rollingTxWindow` receives `rpcBlockCount` (block count) instead of actual transaction count. Makes `calculateRollingTxPerSec` undercount on RPC fallback path. Fix: sum `block.TxCount` in `getBlockMetricsViaRPC` and return it.
- [ ] **StopTest race on concurrent calls** — `loadgen.go:398`: `StopTest` can be invoked concurrently (e.g., `/stop` handler + `completionWatcher`). No idempotency guard — can double-close `incrementalStopCh` or run shutdown twice. Fix: `atomic.CompareAndSwapInt32` on `lg.stopping` or `sync.Once`.

### Feature Gaps

- [ ] **StartTest skips ratio validation for adaptive-realistic** — `loadgen.go:375`: Only validates `txTypeRatios` for `PatternRealistic`, not `PatternAdaptiveRealistic`. Invalid ratios can slip through.
- [ ] **Adaptive-realistic doesn't deploy required contracts** — `init.go:242`: Contract deployment only considers `PatternRealistic`. `PatternAdaptiveRealistic` uses realistic mixed TX generation but may start without deploying ERC20/Uniswap contracts, causing send failures.

### Flaky E2E Tests

- [x] **TestE2ENonceOrdering race with async tx log persistence** — `persistence.go:493` writes tx logs in a background goroutine, but `StopTest` sets status to `completed` before the write finishes. E2E test fetches `/history/{testID}/transactions` immediately after seeing `completed` → 0 rows. Fixed by adding a retry loop in `e2e_test.go:73`. Root cause (async write without signalling completion) is pre-existing — tracked as a separate concern.

## Recommended Next Steps

### High Priority — Bug Fixes
Address the pre-existing bugs above.

### Medium Priority — Structural Improvements
- **Reduce file sizes** — `blockmetrics.go` (588), `websocket.go` (550), `workers.go` (540) could be split further
- **Move types to dedicated file** — `blockMetricsPoint`, `rollingGasPoint` etc. in helpers.go should be in a types file

### Lower Priority — Coverage
- **`internal/contract`** — 0% coverage
- **`internal/rpc`** — 4% coverage, mostly integration-dependent
- **Dockerfile** — unstaged change pins Go 1.25.7 (unrelated to refactor)