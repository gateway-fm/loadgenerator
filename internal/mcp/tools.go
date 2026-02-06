package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterTools registers all load generator tools on the MCP server.
func RegisterTools(s *server.MCPServer, client *Client) {
	registerStatus(s, client)
	registerHealth(s, client)
	registerStart(s, client)
	registerStop(s, client)
	registerReset(s, client)
	registerRecycle(s, client)
	registerHistory(s, client)
	registerTestDetail(s, client)
	registerTestTxs(s, client)
	registerDeleteRun(s, client)
}

func registerStatus(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_status",
		gomcp.WithDescription("Get current load generator status: test state, TXs sent/confirmed/failed, TPS, latency stats, gas metrics."),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		raw, err := client.Get("/v1/status")
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Load generator unreachable: %v\n\nIs the stack running? Try: make run", err)), nil
		}
		return gomcp.NewToolResultText(formatStatus(raw)), nil
	})
}

func registerHealth(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_health",
		gomcp.WithDescription("Quick health check for the load generator. Checks L2 RPC and builder RPC connectivity."),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		raw, err := client.Get("/ready")
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Load generator unhealthy: %v", err)), nil
		}
		return gomcp.NewToolResultText(formatHealth(raw)), nil
	})
}

func registerStart(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_start",
		gomcp.WithDescription("Start a load test. This is a MUTATING operation. Patterns: constant, ramp, spike, adaptive, realistic, adaptive-realistic."),
		gomcp.WithString("pattern",
			gomcp.Required(),
			gomcp.Description("Load pattern: constant, ramp, spike, adaptive, realistic, adaptive-realistic"),
		),
		gomcp.WithNumber("duration_sec",
			gomcp.Required(),
			gomcp.Description("Test duration in seconds (1-3600)"),
		),
		gomcp.WithString("transaction_type",
			gomcp.Description("TX type: eth-transfer (default), erc20-transfer, erc20-approve, uniswap-swap, storage-write, heavy-compute"),
		),
		gomcp.WithNumber("num_accounts",
			gomcp.Description("Number of accounts to use (default: auto)"),
		),
		gomcp.WithNumber("constant_rate",
			gomcp.Description("TPS for constant pattern"),
		),
		gomcp.WithNumber("ramp_start",
			gomcp.Description("Start TPS for ramp pattern"),
		),
		gomcp.WithNumber("ramp_end",
			gomcp.Description("End TPS for ramp pattern"),
		),
		gomcp.WithNumber("ramp_steps",
			gomcp.Description("Number of steps for ramp pattern"),
		),
		gomcp.WithNumber("baseline_rate",
			gomcp.Description("Baseline TPS for spike pattern"),
		),
		gomcp.WithNumber("spike_rate",
			gomcp.Description("Spike TPS for spike pattern"),
		),
		gomcp.WithNumber("spike_duration",
			gomcp.Description("Spike duration in seconds"),
		),
		gomcp.WithNumber("spike_interval",
			gomcp.Description("Interval between spikes in seconds"),
		),
		gomcp.WithNumber("adaptive_initial_rate",
			gomcp.Description("Initial TPS for adaptive pattern"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		pattern, err := req.RequireString("pattern")
		if err != nil {
			return gomcp.NewToolResultError("pattern is required"), nil
		}
		durationSec := req.GetInt("duration_sec", 0)
		if durationSec <= 0 {
			return gomcp.NewToolResultError("duration_sec must be positive"), nil
		}

		payload := map[string]any{
			"pattern":     pattern,
			"durationSec": durationSec,
		}

		if v := req.GetString("transaction_type", ""); v != "" {
			payload["transactionType"] = v
		}
		if v := req.GetInt("num_accounts", 0); v > 0 {
			payload["numAccounts"] = v
		}
		if v := req.GetInt("constant_rate", 0); v > 0 {
			payload["constantRate"] = v
		}
		if v := req.GetInt("ramp_start", 0); v > 0 {
			payload["rampStart"] = v
		}
		if v := req.GetInt("ramp_end", 0); v > 0 {
			payload["rampEnd"] = v
		}
		if v := req.GetInt("ramp_steps", 0); v > 0 {
			payload["rampSteps"] = v
		}
		if v := req.GetInt("baseline_rate", 0); v > 0 {
			payload["baselineRate"] = v
		}
		if v := req.GetInt("spike_rate", 0); v > 0 {
			payload["spikeRate"] = v
		}
		if v := req.GetInt("spike_duration", 0); v > 0 {
			payload["spikeDuration"] = v
		}
		if v := req.GetInt("spike_interval", 0); v > 0 {
			payload["spikeInterval"] = v
		}
		if v := req.GetInt("adaptive_initial_rate", 0); v > 0 {
			payload["adaptiveInitialRate"] = v
		}

		_, err = client.Post("/v1/start", payload)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Start test failed: %v", err)), nil
		}

		return gomcp.NewToolResultText(joinLines(
			section("Test Started"),
			kv("Pattern", pattern),
			kv("Duration", fmt.Sprintf("%ds", durationSec)),
		)), nil
	})
}

func registerStop(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_stop",
		gomcp.WithDescription("Stop the currently running load test. This is a MUTATING operation."),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		_, err := client.Post("/v1/stop", nil)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Stop failed: %v", err)), nil
		}
		return gomcp.NewToolResultText(joinLines(
			section("Test Stopped"),
			"The test has been stopped. Results will be available in history.",
		)), nil
	})
}

func registerReset(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_reset",
		gomcp.WithDescription("Reset the load generator to idle state. This is a MUTATING operation."),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		_, err := client.Post("/v1/reset", nil)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Reset failed: %v", err)), nil
		}
		return gomcp.NewToolResultText(joinLines(
			section("Load Generator Reset"),
			"State cleared. Ready for a new test.",
		)), nil
	})
}

func registerRecycle(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_recycle",
		gomcp.WithDescription("Recycle funds from dynamic test accounts back to faucets. This is a MUTATING operation."),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		raw, err := client.Post("/v1/recycle", nil)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Recycle failed: %v", err)), nil
		}
		var result map[string]any
		json.Unmarshal(raw, &result)
		status := "success"
		if s, ok := result["status"].(string); ok {
			status = s
		}
		recycled := 0
		if r, ok := result["recycled"].(float64); ok {
			recycled = int(r)
		}
		return gomcp.NewToolResultText(joinLines(
			section("Funds Recycled"),
			kv("Status", status),
			kv("Accounts Recycled", formatNumber(recycled)),
		)), nil
	})
}

func registerHistory(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_history",
		gomcp.WithDescription("List completed test runs with summary metrics (paginated)."),
		gomcp.WithNumber("limit",
			gomcp.Description("Max results to return (default: 10, max: 100)"),
		),
		gomcp.WithNumber("offset",
			gomcp.Description("Results offset for pagination (default: 0)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		limit := req.GetInt("limit", 10)
		offset := req.GetInt("offset", 0)
		path := fmt.Sprintf("/v1/history?limit=%d&offset=%d", limit, offset)

		raw, err := client.Get(path)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("History failed: %v", err)), nil
		}
		return gomcp.NewToolResultText(formatHistory(raw)), nil
	})
}

func registerTestDetail(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_test_detail",
		gomcp.WithDescription("Get detailed results for a specific test run by ID."),
		gomcp.WithString("id",
			gomcp.Required(),
			gomcp.Description("Test run ID"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return gomcp.NewToolResultError("id is required"), nil
		}
		raw, err := client.Get("/v1/history/" + id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Test detail failed: %v", err)), nil
		}
		return gomcp.NewToolResultText(formatTestDetail(raw)), nil
	})
}

func registerTestTxs(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_test_txs",
		gomcp.WithDescription("Get transaction logs for a specific test run (paginated)."),
		gomcp.WithString("id",
			gomcp.Required(),
			gomcp.Description("Test run ID"),
		),
		gomcp.WithNumber("limit",
			gomcp.Description("Max transactions to return (default: 50, max: 1000)"),
		),
		gomcp.WithNumber("offset",
			gomcp.Description("Offset for pagination (default: 0)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return gomcp.NewToolResultError("id is required"), nil
		}
		limit := req.GetInt("limit", 50)
		offset := req.GetInt("offset", 0)
		path := fmt.Sprintf("/v1/history/%s/transactions?limit=%d&offset=%d", id, limit, offset)

		raw, err := client.Get(path)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Test transactions failed: %v", err)), nil
		}
		return gomcp.NewToolResultText(formatTestTxs(raw)), nil
	})
}

func registerDeleteRun(s *server.MCPServer, client *Client) {
	tool := gomcp.NewTool("loadgen_delete_run",
		gomcp.WithDescription("Delete a test run and its transaction logs. This is a MUTATING operation."),
		gomcp.WithString("id",
			gomcp.Required(),
			gomcp.Description("Test run ID to delete"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return gomcp.NewToolResultError("id is required"), nil
		}
		_, err = client.Delete("/v1/history/" + id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("Delete failed: %v", err)), nil
		}
		return gomcp.NewToolResultText(joinLines(
			section("Test Run Deleted"),
			kv("ID", id),
		)), nil
	})
}

// Response formatting functions

func formatStatus(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Sprintf("Error parsing status: %v", err)
	}

	status := getStr(m, "status")
	pattern := getStr(m, "pattern")
	txType := getStr(m, "transactionType")
	txSent := getNum(m, "txSent")
	txConfirmed := getNum(m, "txConfirmed")
	txFailed := getNum(m, "txFailed")
	currentTPS := getNum(m, "currentTps")
	avgTPS := getNum(m, "averageTps")
	elapsedMs := getNum(m, "elapsedMs")
	durationMs := getNum(m, "durationMs")

	lines := joinLines(
		section("Load Generator Status"),
		kv("Status", status),
		kv("Pattern", pattern),
		kv("TX Type", txType),
		kv("TXs Sent", formatNumber(txSent)),
		kv("TXs Confirmed", formatNumber(txConfirmed)),
		kv("TXs Failed", formatNumber(txFailed)),
		kv("Current TPS", fmt.Sprintf("%.0f", currentTPS)),
		kv("Average TPS", fmt.Sprintf("%.0f", avgTPS)),
		kv("Elapsed", fmt.Sprintf("%.1fs / %.1fs", elapsedMs/1000, durationMs/1000)),
	)

	// Latency stats
	if lat, ok := m["latency"].(map[string]any); ok {
		lines += "\n\n" + joinLines(
			section("Confirmation Latency"),
			kv("Min", formatMs(getNum(lat, "min"))),
			kv("P50", formatMs(getNum(lat, "p50"))),
			kv("P95", formatMs(getNum(lat, "p95"))),
			kv("P99", formatMs(getNum(lat, "p99"))),
			kv("Max", formatMs(getNum(lat, "max"))),
		)
	}

	// Gas metrics
	baseFee := getNum(m, "latestBaseFeeGwei")
	gasUsed := getNum(m, "latestGasUsed")
	mgasPerSec := getNum(m, "currentMgasPerSec")
	if baseFee > 0 || gasUsed > 0 {
		lines += "\n\n" + joinLines(
			section("Gas Metrics"),
			kv("Base Fee", fmt.Sprintf("%.2f gwei", baseFee)),
			kv("Gas Used", formatNumber(gasUsed)),
			kv("MGas/s", fmt.Sprintf("%.1f", mgasPerSec)),
		)
	}

	return lines
}

func formatHealth(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Sprintf("Error parsing health: %v", err)
	}

	ready, _ := m["ready"].(bool)
	state := "READY"
	if !ready {
		state = "NOT READY"
	}

	lines := section("Load Generator Health: " + state)

	if checks, ok := m["checks"].([]any); ok {
		for _, c := range checks {
			if check, ok := c.(map[string]any); ok {
				name := getStr(check, "name")
				status := getStr(check, "status")
				latencyMs := getNum(check, "latency_ms")
				errMsg := getStr(check, "error")
				line := fmt.Sprintf("  %-15s %s (%dms)", name, status, int64(latencyMs))
				if errMsg != "" {
					line += " - " + errMsg
				}
				lines += "\n" + line
			}
		}
	}

	return lines
}

func formatHistory(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Sprintf("Error parsing history: %v", err)
	}

	total := getNum(m, "total")
	lines := joinLines(
		section("Test History"),
		kv("Total Runs", formatNumber(total)),
		"",
	)

	runs, ok := m["runs"].([]any)
	if !ok || len(runs) == 0 {
		lines += "No test runs found."
		return lines
	}

	for _, r := range runs {
		run, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id := getStr(run, "id")
		pattern := getStr(run, "pattern")
		txType := getStr(run, "transactionType")
		txSent := getNum(run, "txSent")
		txConfirmed := getNum(run, "txConfirmed")
		avgTPS := getNum(run, "averageTps")
		startedAt := getStr(run, "startedAt")

		// Parse and format the timestamp
		t, err := time.Parse(time.RFC3339Nano, startedAt)
		started := startedAt
		if err == nil {
			started = t.Format("2006-01-02 15:04:05")
		}

		lines += fmt.Sprintf("### %s\n", id)
		lines += joinLines(
			kv("Pattern", pattern),
			kv("TX Type", txType),
			kv("TXs Sent", formatNumber(txSent)),
			kv("TXs Confirmed", formatNumber(txConfirmed)),
			kv("Avg TPS", fmt.Sprintf("%.0f", avgTPS)),
			kv("Started", started),
		)
		lines += "\n\n"
	}

	return lines
}

func formatTestDetail(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Sprintf("Error parsing test detail: %v", err)
	}

	// The detail response has a "run" field with the test result
	run, ok := m["run"].(map[string]any)
	if !ok {
		return "Test run not found"
	}

	id := getStr(run, "id")
	pattern := getStr(run, "pattern")
	txType := getStr(run, "transactionType")
	txSent := getNum(run, "txSent")
	txConfirmed := getNum(run, "txConfirmed")
	txFailed := getNum(run, "txFailed")
	avgTPS := getNum(run, "averageTps")
	durationMs := getNum(run, "durationMs")

	lines := joinLines(
		section("Test Run: "+id),
		kv("Pattern", pattern),
		kv("TX Type", txType),
		kv("Duration", fmt.Sprintf("%.1fs", durationMs/1000)),
		kv("TXs Sent", formatNumber(txSent)),
		kv("TXs Confirmed", formatNumber(txConfirmed)),
		kv("TXs Failed", formatNumber(txFailed)),
		kv("Avg TPS", fmt.Sprintf("%.0f", avgTPS)),
	)

	// Latency
	if lat, ok := run["latency"].(map[string]any); ok {
		lines += "\n\n" + joinLines(
			section("Confirmation Latency"),
			kv("Min", formatMs(getNum(lat, "min"))),
			kv("P50", formatMs(getNum(lat, "p50"))),
			kv("P95", formatMs(getNum(lat, "p95"))),
			kv("P99", formatMs(getNum(lat, "p99"))),
			kv("Max", formatMs(getNum(lat, "max"))),
		)
	}

	// Config
	if cfg, ok := run["config"].(map[string]any); ok {
		lines += "\n\n" + section("Config")
		for k, v := range cfg {
			switch val := v.(type) {
			case float64:
				if val == float64(int64(val)) {
					lines += "\n" + kv(k, formatNumber(val))
				} else {
					lines += "\n" + kv(k, fmt.Sprintf("%.2f", val))
				}
			case string:
				if val != "" {
					lines += "\n" + kv(k, val)
				}
			}
		}
	}

	return lines
}

func formatTestTxs(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Sprintf("Error parsing transactions: %v", err)
	}

	total := getNum(m, "total")
	lines := joinLines(
		section("Transaction Logs"),
		kv("Total", formatNumber(total)),
		"",
	)

	txs, ok := m["transactions"].([]any)
	if !ok || len(txs) == 0 {
		lines += "No transactions found."
		return lines
	}

	for i, t := range txs {
		if i >= 20 {
			lines += fmt.Sprintf("\n... and %d more", len(txs)-20)
			break
		}
		tx, ok := t.(map[string]any)
		if !ok {
			continue
		}
		hash := getStr(tx, "hash")
		status := getStr(tx, "status")
		nonce := getNum(tx, "nonce")
		lines += fmt.Sprintf("  [%d] %s  %s  nonce=%d\n", i, hash[:18]+"...", status, int64(nonce))
	}

	return lines
}

// Helper functions
func getStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getNum(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if n, ok := v.(float64); ok {
			return n
		}
	}
	return 0
}
