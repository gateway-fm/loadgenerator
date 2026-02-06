# Load Generator

High-throughput transaction load generator for testing blockchain sequencers.

- **Docker image**: `gatewayfm/loadgenerator`
- **Go module**: `github.com/gateway-fm/loadgenerator`
- **Used by**: [gateway-fm/blockbuilder](https://github.com/gateway-fm/blockbuilder) sequencer PoC

## Quick Start

### As a Docker service (with blockbuilder stack)

The load generator is typically run as part of the [blockbuilder](https://github.com/gateway-fm/blockbuilder) Docker Compose stack. See that repo for full instructions.

### Standalone

```bash
# Build
make build

# Run locally (requires block-builder at localhost:13000)
make run

# Run a load test via API
curl -X POST http://localhost:13001/start \
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

### Burst

```json
{
  "pattern": "burst",
  "burstSize": 500,
  "burstCount": 10,
  "burstIntervalMs": 1000
}
```

Repeated bursts of transactions.

### Poisson

```json
{
  "pattern": "poisson",
  "targetRate": 100,
  "durationSec": 60
}
```

Randomized timing based on exponential distribution.

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

## API Reference

### Start Test

```bash
POST /start
```

```json
{
  "pattern": "constant",
  "constantRate": 100,
  "durationSec": 60,
  "numAccounts": 10,
  "transactionType": "uniswap-swap",
  "txTypeRatios": {
    "ethTransfer": 20,
    "erc20Transfer": 35,
    "uniswapSwap": 45
  }
}
```

### Stop Test

```bash
POST /stop
```

### Get Status

```bash
GET /status
```

```json
{
  "running": true,
  "tps": 100,
  "mgasPerSec": 18.5,
  "totalTxs": 6000,
  "confirmedTxs": 5980,
  "failedTxs": 20,
  "avgLatencyMs": 235
}
```

### Get History

```bash
GET /history
GET /history/{id}
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

## Development

```bash
# Run tests
make test

# Run benchmarks
make bench

# Build binary
make build

# Run locally (requires running block-builder)
make run

# Clean
make clean
```

## Examples

### Mainnet-like Mix

```json
{
  "txTypeRatios": {
    "ethTransfer": 20,
    "erc20Transfer": 35,
    "erc20Approve": 10,
    "uniswapSwap": 25,
    "storageWrite": 5,
    "heavyCompute": 5
  }
}
```

### DeFi-heavy Load

```json
{
  "transactionType": "uniswap-swap"
}
```

### TPS Maximization

```json
{
  "transactionType": "eth-transfer"
}
```
