# GasStorm Load Generator

High-throughput transaction load generator for benchmarking blockchain sequencers and execution layers.

- **Docker image**: `gatewayfm/loadgenerator`
- **Go module**: `github.com/gateway-fm/loadgenerator`
- **Used by**: [gateway-fm/gasstorm](https://github.com/gateway-fm/gasstorm)
- **API spec**: [`api/openapi.yaml`](api/openapi.yaml)

## Quick Start

### As a Docker service (with GasStorm stack)

The load generator is typically run as part of the [GasStorm](https://github.com/gateway-fm/gasstorm) Docker Compose stack. See that repo for full instructions.

### Standalone

```bash
# Build
make build

# Run locally (requires block-builder at localhost:13000)
make run

# Run a load test via API
curl -X POST http://localhost:13001/v1/start \
  -H "Content-Type: application/json" \
  -d '{"pattern":"constant","constantRate":100,"durationSec":60}'

# Or use the dashboard
open http://localhost:18000/load-test/
```

### Docker

```bash
make docker                              # Build image
docker run -p 13001:3001 gatewayfm/loadgenerator  # Run
```

## API Reference

All endpoints are available under the `/v1/` prefix. Legacy unversioned endpoints (e.g., `/status`) are maintained for backwards compatibility. Health and metrics endpoints are unversioned (standard Kubernetes/Prometheus paths).

Full schema definitions are in [`api/openapi.yaml`](api/openapi.yaml).

### Start Test

Start a new load test. Only one test can run at a time.

```bash
curl -X POST http://localhost:13001/v1/start \
  -H "Content-Type: application/json" \
  -d '{
    "pattern": "constant",
    "constantRate": 100,
    "durationSec": 60,
    "numAccounts": 10,
    "transactionType": "uniswap-swap"
  }'
```

Response:

```json
{"status": "started"}
```

### Stop Test

Stop the currently running test gracefully.

```bash
curl -X POST http://localhost:13001/v1/stop
```

Response:

```json
{"status": "stopped"}
```

### Reset

Reset the load generator state, clearing current metrics.

```bash
curl -X POST http://localhost:13001/v1/reset
```

Response:

```json
{"status": "reset"}
```

### Recycle Funds

Transfer remaining funds from dynamically created test accounts back to the faucet.

```bash
curl -X POST http://localhost:13001/v1/recycle
```

Response:

```json
{"status": "success", "recycled": 10}
```

### Get Status

Get real-time metrics for the currently running or most recent test.

```bash
curl http://localhost:13001/v1/status
```

Response (abbreviated):

```json
{
  "status": "running",
  "txSent": 6000,
  "txConfirmed": 5980,
  "txFailed": 20,
  "currentTps": 98.5,
  "averageTps": 99.2,
  "elapsedMs": 60500,
  "durationMs": 60000,
  "targetTps": 100,
  "pattern": "constant",
  "transactionType": "eth-transfer",
  "peakTps": 105,
  "totalGasUsed": 125580000,
  "blockCount": 60,
  "peakMgasPerSec": 18.5,
  "avgMgasPerSec": 15.2,
  "avgFillRate": 42.5,
  "currentMgasPerSec": 16.1,
  "currentFillRate": 40.2,
  "latency": {
    "count": 5980,
    "min": 45.0,
    "max": 890.0,
    "avg": 235.0,
    "p50": 210.0,
    "p95": 450.0,
    "p99": 720.0
  }
}
```

### Get History

Get a paginated list of past test runs.

```bash
# Default: 50 results, offset 0
curl http://localhost:13001/v1/history

# With pagination
curl "http://localhost:13001/v1/history?limit=10&offset=20"
```

Response:

```json
{
  "runs": [
    {
      "id": "abc123",
      "startedAt": "2026-01-15T10:30:00Z",
      "completedAt": "2026-01-15T10:31:00Z",
      "pattern": "constant",
      "transactionType": "eth-transfer",
      "durationMs": 60000,
      "txSent": 6000,
      "txConfirmed": 5980,
      "txFailed": 20,
      "averageTps": 99.2,
      "peakTps": 105,
      "status": "completed",
      "peakMgasPerSec": 18.5,
      "avgMgasPerSec": 15.2
    }
  ],
  "total": 42,
  "limit": 50,
  "offset": 0
}
```

### Get History Detail

Get detailed information about a specific test run, including full time series data.

```bash
curl http://localhost:13001/v1/history/abc123
```

Response:

```json
{
  "run": { "id": "abc123", "status": "completed", "..." : "..." },
  "timeSeries": [
    {
      "timestampMs": 200,
      "txSent": 20,
      "txConfirmed": 18,
      "txFailed": 0,
      "currentTps": 100.0,
      "targetTps": 100,
      "pendingCount": 2,
      "gasUsed": 378000,
      "gasLimit": 30000000,
      "blockCount": 1,
      "mgasPerSec": 15.1,
      "fillRate": 1.26,
      "avgBlockTimeMs": 200.0,
      "baseFeeGwei": 0.001,
      "gasPriceGwei": 1.5
    }
  ]
}
```

### Update Test Run Metadata

Update the custom name or favorite status of a test run.

```bash
curl -X PATCH http://localhost:13001/v1/history/abc123 \
  -H "Content-Type: application/json" \
  -d '{"customName": "Baseline 100 TPS", "isFavorite": true}'
```

### Delete Test Run

Permanently delete a test run and all associated data.

```bash
curl -X DELETE http://localhost:13001/v1/history/abc123
```

Response:

```json
{"deleted": true}
```

### Get Test Run Transactions

Get a paginated list of individual transaction logs for a test run.

```bash
curl "http://localhost:13001/v1/history/abc123/transactions?limit=100&offset=0"
```

Response:

```json
{
  "transactions": [
    {
      "txHash": "0xabc...",
      "sentAtMs": 1705312200500,
      "confirmedAtMs": 1705312200735,
      "confirmLatencyMs": 235,
      "status": "confirmed",
      "fromAccount": 0,
      "nonce": 42
    }
  ],
  "total": 6000,
  "limit": 100,
  "offset": 0
}
```

### Health (Liveness Probe)

Returns basic health status. Always returns 200 if the service is running.

```bash
curl http://localhost:13001/health
```

Response:

```json
{
  "status": "healthy",
  "timestamp": "2026-01-15T10:30:00Z",
  "uptime_seconds": 3600.5
}
```

### Ready (Readiness Probe)

Returns readiness status including dependency checks. Returns 503 if any dependency is unavailable.

```bash
curl http://localhost:13001/ready
```

Response:

```json
{
  "ready": true,
  "checks": [
    {"name": "l2-rpc", "status": "ok", "latency_ms": 12},
    {"name": "builder-rpc", "status": "ok", "latency_ms": 5}
  ]
}
```

### Prometheus Metrics

Standard Prometheus scrape endpoint.

```bash
curl http://localhost:13001/metrics
```

### WebSocket (Real-time Metrics)

Connect for real-time metrics streaming. Sends `TestMetrics` JSON every 200ms while a test is running.

```bash
websocat ws://localhost:13001/v1/ws
```

## Metrics Explained

### MGas/s (Megagas per Second)

MGas/s measures how much gas the chain is processing per second. It is calculated two ways:

- **Current (rolling)**: Uses a **5-second rolling window**. Block gas data is accumulated in the window. The formula is `totalGasInWindow / 1,000,000 / actualWindowDuration`. A minimum of 3 blocks or 1 second of data is required before reporting, to prevent startup spikes. When the test ends, the last calculated value is frozen to avoid sawtooth decay.
- **Average**: Computed over the full test duration as `cumulativeGasUsed / 1,000,000 / elapsedSeconds`.
- **Peak**: The highest rolling MGas/s value observed after the first second of the test.

### TPS (Transactions per Second)

- **Current TPS**: Calculated every 200ms using the number of transactions sent in the recent interval.
- **Average TPS**: `totalTxSent / elapsedSeconds` over the full test duration.
- **Peak TPS**: The highest instantaneous TPS observed during the test.

### Sample Interval

Time series data points are recorded every **200ms**. Each point captures cumulative TX counts, current TPS, pending count, and block metrics (gasUsed, gasLimit, blockCount, MGas/s, fill rate) for the period since the last sample.

### Fill Rate

Block fill rate is `gasUsed / gasLimit * 100`, expressed as a percentage. Average fill rate across the test uses cumulative gas totals.

## Transaction Types

| Type | Gas | Description |
|------|-----|-------------|
| `eth-transfer` | 21,000 | Basic ETH transfers |
| `erc20-transfer` | ~65,000 | ERC20 token transfers |
| `erc20-approve` | ~46,000 | ERC20 approvals |
| `uniswap-swap` | ~180,000-250,000 | Real Uniswap V3 AMM swaps |
| `storage-write` | ~43,000 | Storage writes |
| `heavy-compute` | ~500,000 | Compute-intensive operations |

### Uniswap V3 Setup

The `uniswap-swap` type deploys full Uniswap V3 infrastructure:
- WETH9 - Wrapped ETH
- MockUSDC - ERC20 stablecoin (6 decimals)
- UniswapV3Factory - Pool factory
- SwapRouter - Swap execution
- NonfungiblePositionManager - Liquidity management
- WETH/USDC Pool - 0.3% fee tier

**Per-account setup:**
- Mint 10,000 USDC
- Wrap 5 ETH to WETH
- Approve SwapRouter for both tokens

## Load Patterns

### Constant Rate

```json
{
  "pattern": "constant",
  "constantRate": 100,
  "durationSec": 60
}
```

Sustained TPS for specified duration.

### Ramp

```json
{
  "pattern": "ramp",
  "rampStart": 10,
  "rampEnd": 500,
  "rampSteps": 10,
  "durationSec": 60
}
```

Linear ramp from start to end TPS over N steps.

### Spike

```json
{
  "pattern": "spike",
  "baselineRate": 50,
  "spikeRate": 500,
  "spikeDuration": 5,
  "spikeInterval": 15,
  "durationSec": 120
}
```

Repeated bursts of transactions at spike rate with baseline between spikes.

### Adaptive

```json
{
  "pattern": "adaptive",
  "adaptiveInitialRate": 100,
  "durationSec": 60
}
```

Automatically adjusts rate based on backpressure (pending TX count).

### Realistic

```json
{
  "pattern": "realistic",
  "durationSec": 120,
  "realisticConfig": {
    "numAccounts": 50,
    "targetTps": 200,
    "minTipGwei": 0.1,
    "maxTipGwei": 10.0,
    "tipDistribution": "exponential",
    "txTypeRatios": {
      "ethTransfer": 20,
      "erc20Transfer": 35,
      "erc20Approve": 10,
      "uniswapSwap": 25,
      "storageWrite": 5,
      "heavyCompute": 5
    }
  }
}
```

Mixed transaction types with configurable tip distributions.

### Adaptive Realistic

```json
{
  "pattern": "adaptive-realistic",
  "durationSec": 120,
  "adaptiveInitialRate": 100,
  "realisticConfig": {
    "numAccounts": 50,
    "targetTps": 200,
    "minTipGwei": 0.1,
    "maxTipGwei": 10.0,
    "tipDistribution": "power-law",
    "txTypeRatios": {
      "ethTransfer": 20,
      "erc20Transfer": 35,
      "uniswapSwap": 45
    }
  }
}
```

Combines adaptive rate control with realistic mixed transaction types.

## Account Management

### Nonce Reservation Pattern

```go
// Reserve nonce atomically
nonce := account.ReserveNonce()

// Send async with callback
sender.SendAsync(ctx, tx, func(err error) {
    if err != nil {
        nonce.Rollback()  // Return to pool
    } else {
        nonce.Commit()    // Mark used
    }
})
```

**Benefits:**
- Prevents nonce gaps
- Atomic reservation
- Fast local tracking (no per-TX RPC)

### Circuit Breaker

Opens when:
- Failure rate > 5%
- Revocation rate > 20%

Recovery:
```go
func (cb *CircuitBreaker) onOpen() {
    cb.rateLimiter.SetRate(10)  // Slow down
    cb.resyncAllNonces()         // Fetch fresh nonces
}
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `EXECUTION_LAYER` | reth | Execution layer: `reth`, `cdk-erigon`, `gravity-reth` |
| `BUILDER_RPC_URL` | http://block-builder:3000 | Transaction submission endpoint |
| `L2_RPC_URL` | http://block-builder:3000 | L2 node for confirmations |
| `L2_WS_URL` | ws://l2-reth:8546 | L2 WebSocket for block metrics |
| `PRECONF_WS_URL` | ws://block-builder:3001/ws/preconfirmations | Preconfirmation WebSocket |
| `LISTEN_ADDR` | :3001 | API listen address |
| `DATABASE_PATH` | /data/loadgen.db | SQLite database |
| `BLOCK_TIME_MS` | 150 | Block interval in milliseconds |

## Gas Profiles

| Type | Gas Limit | TXs per 30M Block |
|------|-----------|-------------------|
| eth-transfer | 21,000 | ~1,428 |
| erc20-approve | 46,000 | ~652 |
| storage-write | 43,000 | ~697 |
| erc20-transfer | 70,000 | ~428 |
| uniswap-swap | 250,000 | ~120 |
| heavy-compute | 500,000 | ~60 |

## MCP Server

An [MCP](https://modelcontextprotocol.io/) server is included for AI-assisted interaction with a running load generator. It exposes 10 tools (status, start/stop tests, history, transaction logs, fund recycling) over stdio transport. Works with Claude Code and OpenCode out of the box via `.mcp.json` / `opencode.json`.

See [docs/mcp.md](docs/mcp.md) for full tool reference and setup.

## Development

```bash
# Run tests
make test

# Run benchmarks
make bench

# Run API contract tests (no running stack required)
make test-contract

# Run E2E tests (requires running stack)
make test-e2e

# Build binary
make build

# Run locally (requires running block-builder)
make run

# Build Docker image
make docker

# Push Docker image
make docker-push

# Clean
make clean
```

## Examples

### Mainnet-like Mix

```json
{
  "pattern": "realistic",
  "durationSec": 120,
  "realisticConfig": {
    "numAccounts": 50,
    "targetTps": 200,
    "minTipGwei": 0.1,
    "maxTipGwei": 10.0,
    "tipDistribution": "exponential",
    "txTypeRatios": {
      "ethTransfer": 20,
      "erc20Transfer": 35,
      "erc20Approve": 10,
      "uniswapSwap": 25,
      "storageWrite": 5,
      "heavyCompute": 5
    }
  }
}
```

### DeFi-heavy Load

```json
{
  "pattern": "constant",
  "constantRate": 100,
  "durationSec": 60,
  "transactionType": "uniswap-swap"
}
```

### TPS Maximization

```json
{
  "pattern": "constant",
  "constantRate": 1000,
  "durationSec": 60,
  "transactionType": "eth-transfer"
}
```
