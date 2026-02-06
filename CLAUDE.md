# Load Generator

High-throughput transaction load generator for testing blockchain sequencers.

## Git Workflow

When committing, use `--no-gpg-sign` to avoid GPG timeout issues:
```bash
git commit --no-gpg-sign -m "commit message"
```

## Go Setup

Go 1.25 is installed locally at `~/go-local/go/bin`. Just run `go` directly:

```bash
go test ./...
go build ./...
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                       Load Generator                            │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │  Accounts   │  │ TX Builders │  │    Metrics Collector    │ │
│  │  (funded)   │  │  (per type) │  │  (latency, throughput)  │ │
│  └──────┬──────┘  └──────┬──────┘  └────────────┬────────────┘ │
│         │                │                      │               │
│         ▼                ▼                      │               │
│  ┌────────────────────────────────┐             │               │
│  │         TX Pipeline            │─────────────┘               │
│  │  (sign, send, track, confirm)  │                             │
│  └───────────────┬────────────────┘                             │
└──────────────────┼──────────────────────────────────────────────┘
                   │
                   ▼ HTTP: eth_sendRawTransaction
┌──────────────────────────────────────────────────────────────────┐
│              Block Builder / Sequencer RPC                        │
└──────────────────────────────────────────────────────────────────┘
```

## Key Files

| File | Purpose |
|------|---------|
| `cmd/loadgen/main.go` | Main entry, worker management, GetMetrics(), test lifecycle |
| `internal/account/account.go` | Nonce reservation pattern (ReserveNonce/Commit/Rollback) |
| `internal/account/manager.go` | Account initialization and funding |
| `internal/config/config.go` | Config loading with capability resolution |
| `internal/execnode/capabilities.go` | Execution layer capability definitions |
| `internal/execnode/registry.go` | Registry of built-in execution layers |
| `internal/rpc/client.go` | RPC client with nonce fetching |
| `internal/metrics/collector.go` | Latency and throughput tracking |
| `internal/metrics/prometheus.go` | Prometheus metrics integration |
| `internal/storage/models.go` | Database models (MUST have json tags!) |
| `internal/storage/sqlite.go` | SQLite persistence |
| `internal/transport/http.go` | HTTP API handlers and input validation |
| `internal/transport/websocket.go` | WebSocket server for real-time metrics |
| `internal/txbuilder/` | Transaction builders per type |
| `internal/pipeline/` | TX pipeline (sign -> send -> confirm) |
| `internal/verification/` | Post-test on-chain verification |
| `pkg/types/types.go` | Public API types |
| `api/openapi.yaml` | OpenAPI 3.1 specification for the HTTP API |

## OpenAPI Specification

The canonical API spec is at `api/openapi.yaml`. It covers all endpoints:

- **Test Control**: `/v1/start`, `/v1/stop`, `/v1/reset`, `/v1/recycle`, `/v1/status`
- **Test History**: `/v1/history`, `/v1/history/{id}`, `/v1/history/{id}/transactions`
- **Health**: `/health`, `/ready`
- **Metrics**: `/metrics` (Prometheus), `/v1/ws` (WebSocket)

Legacy unversioned endpoints (e.g., `/status`) mirror the v1 endpoints for backwards compatibility.

## Storage Schema

All models are in `internal/storage/models.go`. JSON tags use camelCase to match TypeScript API expectations.

### TestRun

Primary model for a persisted test run with summary statistics. Stored in SQLite.

| Field | Type | JSON Key | Description |
|-------|------|----------|-------------|
| ID | string | `id` | Unique test run identifier |
| StartedAt | time.Time | `startedAt` | When test started |
| CompletedAt | *time.Time | `completedAt` | When test completed (nil if running) |
| Pattern | LoadPattern | `pattern` | Load pattern used (constant, ramp, spike, etc.) |
| TransactionType | TransactionType | `transactionType` | TX type (eth-transfer, uniswap-swap, etc.) |
| DurationMs | int64 | `durationMs` | Test duration in milliseconds |
| TxSent | uint64 | `txSent` | Total transactions sent |
| TxConfirmed | uint64 | `txConfirmed` | Total confirmed |
| TxFailed | uint64 | `txFailed` | Total failed |
| TxDiscarded | uint64 | `txDiscarded` | TXs still pending at test end |
| AverageTPS | float64 | `averageTps` | Average throughput |
| PeakTPS | int | `peakTps` | Peak instantaneous TPS |
| LatencyStats | *LatencyStats | `latencyStats` | Confirmation latency percentiles |
| PreconfLatency | *LatencyStats | `preconfLatency` | Preconfirmation latency percentiles |
| Config | *StartTestRequest | `config` | Original test configuration |
| Status | string | `status` | "running", "completed", or "error" |
| BlockCount | int | `blockCount` | Total blocks produced during test |
| TotalGasUsed | uint64 | `totalGasUsed` | Total gas across all blocks |
| AvgFillRate | float64 | `avgFillRate` | Average block fill rate (0-100) |
| PeakMgasPerSec | float64 | `peakMgasPerSec` | Peak MGas/s observed |
| AvgMgasPerSec | float64 | `avgMgasPerSec` | Average MGas/s |
| CustomName | *string | `customName` | User-defined test name |
| IsFavorite | bool | `isFavorite` | Whether test is starred |
| Environment | *EnvironmentSnapshot | `environment` | Builder and load-gen config at test start |
| Verification | *VerificationResult | `verification` | Post-test verification results |
| DeployedContracts | []DeployedContract | `deployedContracts` | Contract addresses deployed for test |
| TestAccounts | *TestAccountsInfo | `testAccounts` | Account info (roles, counts) |

### TimeSeriesPoint

Metrics sample recorded every 200ms during a test.

| Field | Type | JSON Key | Description |
|-------|------|----------|-------------|
| TimestampMs | int64 | `timestampMs` | Milliseconds since test start |
| TxSent | uint64 | `txSent` | Cumulative TX sent |
| TxConfirmed | uint64 | `txConfirmed` | Cumulative TX confirmed |
| TxFailed | uint64 | `txFailed` | Cumulative TX failed |
| CurrentTPS | float64 | `currentTps` | Instantaneous TPS |
| TargetTPS | int | `targetTps` | Target rate |
| PendingCount | int64 | `pendingCount` | Pending TX count |
| GasUsed | uint64 | `gasUsed` | Gas used in blocks during this period |
| GasLimit | uint64 | `gasLimit` | Gas limit of blocks |
| BlockCount | int | `blockCount` | Blocks produced in period |
| MgasPerSec | float64 | `mgasPerSec` | Rolling window MGas/s |
| FillRate | float64 | `fillRate` | Gas usage percentage (0-100) |
| AvgBlockTimeMs | float64 | `avgBlockTimeMs` | Average block time in period |
| BaseFeeGwei | float64 | `baseFeeGwei` | Latest base fee per gas |
| GasPriceGwei | float64 | `gasPriceGwei` | Latest eth_gasPrice |

### TxLogEntry

Individual transaction record for per-TX analysis.

| Field | Type | JSON Key | Description |
|-------|------|----------|-------------|
| TxHash | string | `txHash` | Transaction hash |
| SentAtMs | int64 | `sentAtMs` | When TX was sent (unix ms) |
| ConfirmedAtMs | int64 | `confirmedAtMs` | When confirmed (unix ms) |
| PreconfAtMs | int64 | `preconfAtMs` | When preconfirmed (unix ms) |
| ConfirmLatencyMs | int64 | `confirmLatencyMs` | Confirmation latency |
| PreconfLatencyMs | int64 | `preconfLatencyMs` | Preconfirmation latency |
| Status | string | `status` | "pending", "confirmed", or "failed" |
| ErrorReason | string | `errorReason` | Error message if failed |
| FromAccount | int | `fromAccount` | Account index |
| Nonce | uint64 | `nonce` | Transaction nonce |
| GasTipGwei | float64 | `gasTipGwei` | Gas tip in gwei |

### EnvironmentSnapshot

Captures builder and load-gen config at test start for reproducibility.

| Field | Type | JSON Key | Description |
|-------|------|----------|-------------|
| BuilderBlockTimeMs | int | `builderBlockTimeMs` | Block time from builder config |
| BuilderGasLimit | uint64 | `builderGasLimit` | Gas limit from builder config |
| BuilderMaxTxsPerBlock | int | `builderMaxTxsPerBlock` | Max TXs per block |
| BuilderTxOrdering | string | `builderTxOrdering` | "fifo", "tip_desc", or "tip_asc" |
| BuilderEnablePreconfs | bool | `builderEnablePreconfs` | Preconfirmations enabled |
| NodeName | string | `nodeName` | "op-reth", "gravity-reth", "cdk-erigon" |
| NodeVersion | string | `nodeVersion` | e.g. "reth/v1.9.3" |
| ChainID | uint64 | `chainId` | Chain ID from eth_chainId |

### VerificationResult

Post-test verification comparing load-gen metrics with on-chain state.

| Field | Type | JSON Key | Description |
|-------|------|----------|-------------|
| MetricsMatch | bool | `metricsMatch` | Whether metrics match on-chain |
| TxCountDelta | int64 | `txCountDelta` | OnChain - Confirmed (positive = missed) |
| GasUsedDelta | int64 | `gasUsedDelta` | Gas used discrepancy |
| TipOrdering | *TipOrderingResult | `tipOrdering` | Tip ordering verification |
| TxReceipts | *TxReceiptVerification | `txReceipts` | TX receipt sampling |
| AllChecksPass | bool | `allChecksPass` | All verification checks passed |
| Warnings | []string | `warnings` | Non-fatal warnings |

### Pagination Types

- **PaginatedTestRuns**: `{runs, total, limit, offset}` -- list of TestRun
- **PaginatedTxLogs**: `{transactions, total, limit, offset}` -- list of TxLogEntry
- **TestRunDetail**: `{run, timeSeries}` -- TestRun + []TimeSeriesPoint

## Nonce Management

The load generator uses a **reservation pattern** for high-throughput nonce management:

```go
// Reserve nonce atomically
n := account.ReserveNonce()  // Increments immediately

// Send transaction asynchronously
queued := sender.SendAsync(ctx, txData, func(err error) {
    if err != nil {
        n.Rollback()  // Return nonce to pool on failure
    } else {
        n.Commit()    // Mark nonce as successfully used
    }
})
```

**Key Design Decisions:**
- **Local tracking**: Nonces are tracked locally (not fetched per-TX) for speed
- **Reservation pattern**: ReserveNonce increments immediately, preventing gaps
- **Commit in callback**: Never commit before async send completes
- **Reactive sync**: Resync only on circuit breaker open (not periodic)

**Nonce Fetch Strategy** (`internal/rpc/client.go`):
1. First tries `eth_getPendingNonce` (block builder's view)
2. Falls back to `eth_getTransactionCount("pending")` to include mempool

**Circuit Breaker Recovery:**
- Opens when failure rate > 5% or revocation rate > 20%
- Triggers `resyncAllNonces()` to fetch fresh nonces from builder
- Rate is reduced to 10 TPS until recovery

## Configuration (Environment Variables)

| Variable | Default | Description |
|----------|---------|-------------|
| `EXECUTION_LAYER` | reth | Execution layer: `reth`, `cdk-erigon`, `gravity-reth` |
| `BUILDER_RPC_URL` | http://block-builder:3000 | Transaction submission endpoint |
| `L2_RPC_URL` | http://block-builder:3000 | L2 node for confirmations |
| `L2_WS_URL` | ws://l2-reth:8546 | L2 WebSocket for block metrics |
| `PRECONF_WS_URL` | ws://block-builder:3001/ws/preconfirmations | Preconfirmation WebSocket |
| `LISTEN_ADDR` | :3001 | API listen address |
| `DATABASE_PATH` | /data/loadgen.db | SQLite database path |
| `BLOCK_TIME_MS` | 150 | Block interval in milliseconds |

## API Response Format

- ALL JSON responses use **camelCase** field names (not PascalCase)
- Go structs MUST have `json:"fieldName"` tags
- TypeScript types in the dashboard define the contract
- The OpenAPI spec at `api/openapi.yaml` is the canonical API reference

## Testing

### Test Categories

| Command | Description | Stack Required |
|---------|-------------|----------------|
| `make test` | All unit tests with `-race` flag | No |
| `make bench` | Benchmarks with `-benchmem` | No |
| `make test-contract` | API contract tests (JSON shapes, field names) | No |
| `make test-e2e` | E2E integration tests | Yes (running GasStorm stack) |

### Running Tests

```bash
# All unit tests (always run before committing)
make test

# Benchmarks
make bench

# API contract tests (validates JSON field names, camelCase, response shapes)
# These do NOT require a running stack
make test-contract

# E2E tests (requires running stack at localhost)
# Environment: BUILDER_RPC_URL, LOADGEN_API_URL, L2_RPC_URL, PRECONF_WS_URL
make test-e2e
```

### Test File Locations

| Directory | What it tests |
|-----------|---------------|
| `internal/account/*_test.go` | Nonce reservation, account funding |
| `internal/metrics/*_test.go` | Latency stats, collectors, streaming |
| `internal/storage/*_test.go` | SQLite persistence, model serialization |
| `internal/pipeline/*_test.go` | TX pipeline (sign, send, confirm) |
| `internal/contract/*_test.go` | API contract validation (JSON shapes) |
| `internal/integration/*_test.go` | E2E and metrics consistency tests |
| `cmd/loadgen/*_test.go` | Main generator tests |

## Docker

```bash
make docker            # Build image
make docker-push       # Push to DockerHub
```

Image: `gatewayfm/loadgenerator`

## Integration with GasStorm

This load generator is used by the [gateway-fm/gasstorm](https://github.com/gateway-fm/gasstorm) sequencer benchmarking stack. See that repo's docker-compose files for full stack setup.

## Execution Layer Capabilities

| Layer | Block Builder | Preconfirmations | Builder Status | Block Metrics WS |
|-------|---------------|------------------|----------------|------------------|
| `reth` / `op-reth` | External | Yes | Yes | Yes |
| `gravity-reth` | None (direct) | No | No | No |
| `cdk-erigon` | None (direct) | No | No | No |
