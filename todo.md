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
| **`internal/loadgen`** | **38.0%** | **4,736** |
| `internal/contract` | 0% | |
| **Overall** | **17.7%** | |

## Recommended Next Steps

### High Priority — Test Coverage for internal/loadgen
The refactor to `internal/loadgen` makes this code testable for the first time (was `package main` before). Priority files by impact:

1. **`helpers.go`** — pure functions (`parseHexUint64`, `GetEnvOrDefault`), easiest wins
2. **`blockmetrics.go`** — rolling window calculations, percentile math — pure logic, high value
3. **`api.go`** — handler logic can be tested with mock LoadGenerator
4. **`builder.go`** — `parseNodeName`, `fetchBuilderPressure` parsing logic
5. **`workers.go`** — core send/confirm loop, hardest to unit test (needs mocks)

### Medium Priority — Structural Improvements
- **Extract interfaces** for builder/RPC communication to enable mocking
- **Reduce file sizes** — `blockmetrics.go` (588), `websocket.go` (550), `workers.go` (540) could be split further
- **Move types to dedicated file** — `blockMetricsPoint`, `rollingGasPoint` etc. in helpers.go should be in a types file

### Lower Priority
- **`internal/contract`** — 0% coverage
- **`internal/rpc`** — 4% coverage, mostly integration-dependent
- **Dockerfile** — unstaged change pins Go 1.25.7 (unrelated to refactor)