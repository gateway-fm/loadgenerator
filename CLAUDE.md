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
| `cmd/loadgen/main.go` | Main entry, worker management |
| `internal/account/account.go` | Nonce reservation pattern (ReserveNonce/Commit/Rollback) |
| `internal/account/manager.go` | Account initialization and funding |
| `internal/config/config.go` | Config loading with capability resolution |
| `internal/execnode/capabilities.go` | Execution layer capability definitions |
| `internal/execnode/registry.go` | Registry of built-in execution layers |
| `internal/rpc/client.go` | RPC client with nonce fetching |
| `internal/metrics/collector.go` | Latency and throughput tracking |
| `internal/storage/models.go` | Database models (MUST have json tags!) |
| `internal/storage/sqlite.go` | SQLite persistence |
| `internal/transport/http.go` | HTTP API handlers |
| `internal/txbuilder/` | Transaction builders per type |
| `internal/pipeline/` | TX pipeline (sign → send → confirm) |
| `pkg/types/types.go` | Public API types |

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

## Testing

```bash
make test              # All tests with race detector
make bench             # Benchmarks
make test-contract     # API contract tests (no stack needed)
make test-e2e          # E2E tests (requires running stack)
```

## Docker

```bash
make docker            # Build image
make docker-push       # Push to DockerHub
```

Image: `gatewayfm/loadgenerator`

## Integration with Sequencer PoC

This load generator is used by the [gateway-fm/blockbuilder](https://github.com/gateway-fm/blockbuilder) sequencer PoC. See that repo's docker-compose files for full stack setup.

## Execution Layer Capabilities

| Layer | Block Builder | Preconfirmations | Builder Status | Block Metrics WS |
|-------|---------------|------------------|----------------|------------------|
| `reth` / `op-reth` | External | Yes | Yes | Yes |
| `gravity-reth` | None (direct) | No | No | No |
| `cdk-erigon` | None (direct) | No | No | No |
