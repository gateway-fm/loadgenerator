// Package integration contains end-to-end integration tests.
// These tests require a running Docker Compose stack.
//
// Run with: go test -tags=integration ./internal/integration/...
//
//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/websocket"
)

// E2E test configuration from environment
var (
	builderRPCURL  = getEnv("BUILDER_RPC_URL", "http://localhost:13000")
	loadGenAPIURL  = getEnv("LOADGEN_API_URL", "http://localhost:13001")
	l2RPCURL       = getEnv("L2_RPC_URL", "http://localhost:13000")
	preconfWSURL   = getEnv("PRECONF_WS_URL", "ws://localhost:13002/ws/preconfirmations")
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// skipIfNoStack skips the test if Docker Compose stack is not running.
func skipIfNoStack(t *testing.T) {
	resp, err := http.Get(builderRPCURL + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skip("Skipping E2E test: Docker Compose stack not running")
	}
	resp.Body.Close()
}

// TestE2ENonceOrdering verifies that transactions from the same account
// are included in blocks in correct nonce order.
func TestE2ENonceOrdering(t *testing.T) {
	skipIfNoStack(t)

	// Start a short load test
	testConfig := map[string]any{
		"pattern":         "constant",
		"durationSec":     10,
		"constantRate":    50, // 50 TPS for 10 seconds = 500 txs
		"numAccounts":     5,  // 5 accounts
		"transactionType": "eth-transfer",
	}

	// Start test
	testID := startLoadTest(t, testConfig)
	t.Logf("Started load test: %s", testID)

	// Wait for completion
	waitForTestCompletion(t, testID, 30*time.Second)

	// Fetch all transaction logs
	txLogs := fetchAllTxLogs(t, testID)
	t.Logf("Fetched %d transaction logs", len(txLogs))

	if len(txLogs) == 0 {
		t.Fatal("No transactions logged")
	}

	// Group transactions by account
	byAccount := make(map[int][]txLogEntry)
	for _, tx := range txLogs {
		byAccount[tx.FromAccount] = append(byAccount[tx.FromAccount], tx)
	}

	// Verify nonce ordering per account
	var errors []string
	for account, txs := range byAccount {
		// Sort by sent time
		sort.Slice(txs, func(i, j int) bool {
			return txs[i].SentAtMs < txs[j].SentAtMs
		})

		// Verify nonces are sequential
		for i := 1; i < len(txs); i++ {
			if txs[i].Nonce != txs[i-1].Nonce+1 {
				errors = append(errors, fmt.Sprintf(
					"Account %d: nonce gap between tx %d (nonce %d) and tx %d (nonce %d)",
					account, i-1, txs[i-1].Nonce, i, txs[i].Nonce,
				))
			}
		}

		// Verify confirmed transactions are in nonce order
		var confirmed []txLogEntry
		for _, tx := range txs {
			if tx.Status == "confirmed" {
				confirmed = append(confirmed, tx)
			}
		}

		// Sort by confirmation time
		sort.Slice(confirmed, func(i, j int) bool {
			return confirmed[i].ConfirmedAtMs < confirmed[j].ConfirmedAtMs
		})

		// Check nonces are non-decreasing when confirmed
		for i := 1; i < len(confirmed); i++ {
			if confirmed[i].Nonce < confirmed[i-1].Nonce {
				errors = append(errors, fmt.Sprintf(
					"Account %d: confirmed out of nonce order - tx nonce %d confirmed before nonce %d",
					account, confirmed[i].Nonce, confirmed[i-1].Nonce,
				))
			}
		}
	}

	if len(errors) > 0 {
		t.Errorf("Nonce ordering violations:\n%s", strings.Join(errors, "\n"))
	}
}

// TestE2EMultiBlockNonceContinuity verifies nonce sequences continue correctly
// across multiple blocks.
func TestE2EMultiBlockNonceContinuity(t *testing.T) {
	skipIfNoStack(t)

	// Run a longer test to span multiple blocks
	testConfig := map[string]any{
		"pattern":         "constant",
		"durationSec":     15,
		"constantRate":    100, // 100 TPS for 15 seconds
		"numAccounts":     3,
		"transactionType": "eth-transfer",
	}

	testID := startLoadTest(t, testConfig)
	t.Logf("Started multi-block test: %s", testID)

	waitForTestCompletion(t, testID, 45*time.Second)

	txLogs := fetchAllTxLogs(t, testID)
	t.Logf("Fetched %d transactions", len(txLogs))

	// Get test result to verify multiple blocks
	result := fetchTestResult(t, testID)
	t.Logf("Test result: sent=%d, confirmed=%d, failed=%d, avgTPS=%.1f",
		result.TxSent, result.TxConfirmed, result.TxFailed, result.AverageTps)

	// Group by account and verify complete nonce sequences
	byAccount := make(map[int][]uint64)
	for _, tx := range txLogs {
		if tx.Status == "confirmed" {
			byAccount[tx.FromAccount] = append(byAccount[tx.FromAccount], tx.Nonce)
		}
	}

	for account, nonces := range byAccount {
		sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })

		if len(nonces) < 2 {
			continue
		}

		// Check for gaps
		gaps := 0
		for i := 1; i < len(nonces); i++ {
			if nonces[i] != nonces[i-1]+1 {
				gaps++
				t.Errorf("Account %d: nonce gap between %d and %d", account, nonces[i-1], nonces[i])
			}
		}

		t.Logf("Account %d: %d confirmed txs, nonces %d-%d, gaps=%d",
			account, len(nonces), nonces[0], nonces[len(nonces)-1], gaps)
	}

	// Verify success rate is reasonable (> 90%)
	successRate := float64(result.TxConfirmed) / float64(result.TxSent) * 100
	if successRate < 90 {
		t.Errorf("Success rate too low: %.1f%% (expected > 90%%)", successRate)
	}
}

// TestE2EPreconfirmationEvents verifies that preconfirmation WebSocket events
// are received and match actual block contents.
func TestE2EPreconfirmationEvents(t *testing.T) {
	skipIfNoStack(t)

	// Connect to preconf WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(preconfWSURL, nil)
	if err != nil {
		t.Skipf("Cannot connect to preconf WebSocket: %v", err)
	}
	defer conn.Close()

	// Collect preconf events
	var preconfMu sync.Mutex
	preconfEvents := make(map[string]preconfEvent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var event preconfEvent
			if err := json.Unmarshal(msg, &event); err != nil {
				continue
			}

			preconfMu.Lock()
			preconfEvents[event.TxHash] = event
			preconfMu.Unlock()
		}
	}()

	// Run a short test
	testConfig := map[string]any{
		"pattern":         "constant",
		"durationSec":     5,
		"constantRate":    20,
		"numAccounts":     2,
		"transactionType": "eth-transfer",
	}

	testID := startLoadTest(t, testConfig)
	waitForTestCompletion(t, testID, 20*time.Second)

	// Give time for WebSocket events to arrive
	time.Sleep(2 * time.Second)
	cancel()

	// Fetch actual transaction logs
	txLogs := fetchAllTxLogs(t, testID)

	preconfMu.Lock()
	eventCount := len(preconfEvents)
	preconfMu.Unlock()

	t.Logf("Received %d preconf events, %d transactions logged", eventCount, len(txLogs))

	// Verify preconf events match logged transactions
	var matched, missing int
	for _, tx := range txLogs {
		if tx.Status != "confirmed" {
			continue
		}

		preconfMu.Lock()
		_, found := preconfEvents[tx.TxHash]
		preconfMu.Unlock()

		if found {
			matched++
		} else {
			missing++
			if missing <= 5 {
				t.Logf("Missing preconf event for confirmed tx: %s", tx.TxHash)
			}
		}
	}

	// Calculate match rate
	if len(txLogs) > 0 {
		matchRate := float64(matched) / float64(len(txLogs)) * 100
		t.Logf("Preconf match rate: %.1f%% (%d matched, %d missing)",
			matchRate, matched, missing)

		// Preconf events should be available for most transactions
		// Allow some tolerance for timing issues
		if matchRate < 50 {
			t.Errorf("Preconf match rate too low: %.1f%% (expected > 50%%)", matchRate)
		}
	}
}

// TestE2EAPIContractValidation verifies API responses use camelCase.
func TestE2EAPIContractValidation(t *testing.T) {
	skipIfNoStack(t)

	// Fetch history list and check field names
	resp, err := http.Get(loadGenAPIURL + "/history")
	if err != nil {
		t.Fatalf("Failed to fetch history: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Check for PascalCase fields that indicate the bug
	pascalCaseFields := []string{
		`"ID"`, `"StartedAt"`, `"CompletedAt"`, `"Pattern"`,
		`"TransactionType"`, `"DurationMs"`, `"TxSent"`,
		`"TxConfirmed"`, `"TxFailed"`, `"AverageTPS"`,
		`"TimeSeries"`, `"Run"`, `"Runs"`,
	}

	for _, field := range pascalCaseFields {
		if strings.Contains(bodyStr, field) {
			t.Errorf("API response contains PascalCase field %s (should be camelCase)", field)
		}
	}

	// Check for expected camelCase fields
	camelCaseFields := []string{
		`"runs"`, `"total"`, `"limit"`, `"offset"`,
	}

	for _, field := range camelCaseFields {
		if !strings.Contains(bodyStr, field) {
			t.Logf("Warning: Expected camelCase field %s not found (may be empty response)", field)
		}
	}
}

// Helper types
type txLogEntry struct {
	TxHash        string `json:"txHash"`
	SentAtMs      int64  `json:"sentAtMs"`
	ConfirmedAtMs int64  `json:"confirmedAtMs"`
	Status        string `json:"status"`
	FromAccount   int    `json:"fromAccount"`
	Nonce         uint64 `json:"nonce"`
}

type preconfEvent struct {
	TxHash      string `json:"txHash"`
	Status      string `json:"status"`
	BlockNumber uint64 `json:"blockNumber"`
	Timestamp   int64  `json:"timestamp"`
}

type testResult struct {
	ID          string  `json:"id"`
	TxSent      uint64  `json:"txSent"`
	TxConfirmed uint64  `json:"txConfirmed"`
	TxFailed    uint64  `json:"txFailed"`
	AverageTps  float64 `json:"averageTps"`
	Status      string  `json:"status"`
}

// Helper functions
func startLoadTest(t *testing.T, config map[string]any) string {
	t.Helper()

	body, _ := json.Marshal(config)
	resp, err := http.Post(loadGenAPIURL+"/start", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to start load test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Failed to start load test: %s - %s", resp.Status, string(body))
	}

	var result struct {
		TestID string `json:"testId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode start response: %v", err)
	}

	return result.TestID
}

func waitForTestCompletion(t *testing.T, testID string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(loadGenAPIURL + "/status")
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var status struct {
			Status string `json:"status"`
		}
		json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()

		if status.Status == "completed" || status.Status == "idle" || status.Status == "error" {
			return
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("Test did not complete within %v", timeout)
}

func fetchAllTxLogs(t *testing.T, testID string) []txLogEntry {
	t.Helper()

	var allLogs []txLogEntry
	offset := 0
	limit := 1000

	for {
		url := fmt.Sprintf("%s/api/history/%s/transactions?limit=%d&offset=%d",
			loadGenAPIURL, testID, limit, offset)

		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Failed to fetch tx logs: %v", err)
		}

		var result struct {
			Transactions []txLogEntry `json:"transactions"`
			Total        int          `json:"total"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		// Also try PascalCase in case fix not deployed
		if len(result.Transactions) == 0 {
			resp, _ = http.Get(url)
			var pascalResult struct {
				Transactions []txLogEntry `json:"Transactions"`
				Total        int          `json:"Total"`
			}
			json.NewDecoder(resp.Body).Decode(&pascalResult)
			resp.Body.Close()
			result.Transactions = pascalResult.Transactions
			result.Total = pascalResult.Total
		}

		allLogs = append(allLogs, result.Transactions...)

		if len(allLogs) >= result.Total || len(result.Transactions) == 0 {
			break
		}

		offset += limit
	}

	return allLogs
}

func fetchTestResult(t *testing.T, testID string) testResult {
	t.Helper()

	resp, err := http.Get(loadGenAPIURL + "/history/" + testID)
	if err != nil {
		t.Fatalf("Failed to fetch test result: %v", err)
	}
	defer resp.Body.Close()

	var detail struct {
		Run testResult `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		// Try PascalCase
		resp, _ = http.Get(loadGenAPIURL + "/history/" + testID)
		var pascalDetail struct {
			Run testResult `json:"Run"`
		}
		json.NewDecoder(resp.Body).Decode(&pascalDetail)
		resp.Body.Close()
		return pascalDetail.Run
	}

	return detail.Run
}

// Ensure common.Address is imported for future use
var _ = common.Address{}
