# Load Generator MCP Server

The load generator includes an [MCP](https://modelcontextprotocol.io/) (Model Context Protocol) server that lets AI assistants interact with a running load generator instance. It exposes 10 tools over stdio transport.

## Setup

### Claude Code

The `.mcp.json` in the repo root auto-configures the server. Open the project in Claude Code and the tools are available immediately.

### OpenCode

The `opencode.json` in the repo root auto-configures the server. Open the project in OpenCode and the tools are available immediately.

### Manual

```bash
# Build the binary
make mcp-build

# Run via stdio (used by MCP clients)
LOADGEN_URL=http://localhost:13001 go run ./cmd/mcp
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `LOADGEN_URL` | `http://localhost:13001` | Load generator HTTP endpoint |

## Tools

### `loadgen_status`

Get current load generator status: test state, TXs sent/confirmed/failed, TPS, latency stats, gas metrics.

**Parameters:** None

### `loadgen_health`

Quick health check. Verifies L2 RPC and builder RPC connectivity.

**Parameters:** None

### `loadgen_start`

Start a load test. Only one test can run at a time. **Mutating.**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `pattern` | string | Yes | Load pattern: `constant`, `ramp`, `spike`, `adaptive`, `realistic`, `adaptive-realistic` |
| `duration_sec` | number | Yes | Test duration in seconds (1-3600) |
| `transaction_type` | string | No | TX type: `eth-transfer`, `erc20-transfer`, `erc20-approve`, `uniswap-swap`, `storage-write`, `heavy-compute` |
| `num_accounts` | number | No | Number of accounts to use |
| `constant_rate` | number | No | TPS for constant pattern |
| `ramp_start` | number | No | Start TPS for ramp pattern |
| `ramp_end` | number | No | End TPS for ramp pattern |
| `ramp_steps` | number | No | Number of steps for ramp pattern |
| `baseline_rate` | number | No | Baseline TPS for spike pattern |
| `spike_rate` | number | No | Spike TPS for spike pattern |
| `spike_duration` | number | No | Spike duration in seconds |
| `spike_interval` | number | No | Interval between spikes in seconds |
| `adaptive_initial_rate` | number | No | Initial TPS for adaptive pattern |

### `loadgen_stop`

Stop the currently running load test. **Mutating.**

**Parameters:** None

### `loadgen_reset`

Reset the load generator to idle state, clearing current metrics. **Mutating.**

**Parameters:** None

### `loadgen_recycle`

Recycle funds from dynamic test accounts back to faucets. **Mutating.**

**Parameters:** None

### `loadgen_history`

List completed test runs with summary metrics.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `limit` | number | No | Max results to return (default: 10, max: 100) |
| `offset` | number | No | Results offset for pagination (default: 0) |

### `loadgen_test_detail`

Get detailed results for a specific test run.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Test run ID |

### `loadgen_test_txs`

Get transaction logs for a specific test run.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Test run ID |
| `limit` | number | No | Max transactions to return (default: 50, max: 1000) |
| `offset` | number | No | Offset for pagination (default: 0) |

### `loadgen_delete_run`

Delete a test run and its transaction logs. **Mutating.**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Test run ID to delete |

## Example Usage

With Claude Code or OpenCode, you can ask natural-language questions like:

- "What's the load generator status?"
- "Start a constant 100 TPS test for 60 seconds"
- "Start a ramp test from 10 to 500 TPS with uniswap swaps"
- "Show me the test history"
- "What were the results of the last test?"
- "Stop the current test"
