// Package integration contains end-to-end integration tests.
// This file tests that live metrics match historical metrics to prevent regression.
//
// Run with: go test -tags=integration ./internal/integration/...
//
//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// skipIfNoLoadGen skips the test if the load generator is not running.
func skipIfNoLoadGen(t *testing.T) {
	t.Helper()
	resp, err := http.Get(loadGenAPIURL + "/status")
	if err != nil {
		t.Skipf("Skipping test: Load generator not running at %s: %v", loadGenAPIURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("Skipping test: Load generator returned %d", resp.StatusCode)
	}
}

// liveMetrics captures metrics from the Go load generator WebSocket during a live test.
type liveMetrics struct {
	// Final values captured at test completion
	PeakMgasPerSec float64
	AvgMgasPerSec  float64
	AvgFillRate    float64
	BlockCount     int
	TotalGasUsed   uint64
	TxConfirmed    uint64
	TxSent         uint64
	PeakTps        float64
	AverageTps     float64

	// Time series data (for chart comparison)
	TimeSeriesPoints int
}

// historyMetrics captures metrics from the history API after test completion.
type historyMetrics struct {
	PeakMgasPerSec float64 `json:"peakMgasPerSec"`
	AvgMgasPerSec  float64 `json:"avgMgasPerSec"`
	AvgFillRate    float64 `json:"avgFillRate"`
	BlockCount     int     `json:"blockCount"`
	TotalGasUsed   uint64  `json:"totalGasUsed"`
	TxConfirmed    uint64  `json:"txConfirmed"`
	TxSent         uint64  `json:"txSent"`
	PeakTps        float64 `json:"peakTps"`
	AverageTps     float64 `json:"averageTps"`

	// Time series data
	TimeSeriesPoints int `json:"timeSeriesPoints"`
}

// wsMetricsMessage represents the Go load generator WebSocket message.
type wsMetricsMessage struct {
	Status            string  `json:"status"`
	TxSent            uint64  `json:"txSent"`
	TxConfirmed       uint64  `json:"txConfirmed"`
	CurrentTps        float64 `json:"currentTps"`
	AverageTps        float64 `json:"averageTps"`
	PeakTps           float64 `json:"peakTps"`
	TotalGasUsed      uint64  `json:"totalGasUsed"`
	BlockCount        int     `json:"blockCount"`
	PeakMgasPerSec    float64 `json:"peakMgasPerSec"`
	AvgMgasPerSec     float64 `json:"avgMgasPerSec"`
	AvgFillRate       float64 `json:"avgFillRate"`
	CurrentMgasPerSec float64 `json:"currentMgasPerSec"`
	CurrentFillRate   float64 `json:"currentFillRate"`
}

// TestE2ELiveHistoryMetricsConsistency verifies that live metrics match historical metrics.
// This test ensures we never regress on the fix where live and history charts showed different data.
func TestE2ELiveHistoryMetricsConsistency(t *testing.T) {
	skipIfNoLoadGen(t)

	loadGenWSURL := "ws://localhost:13001/ws"

	// Connect to Go load generator WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(loadGenWSURL, nil)
	if err != nil {
		t.Skipf("Cannot connect to load generator WebSocket at %s: %v", loadGenWSURL, err)
	}
	defer conn.Close()

	// Capture live metrics
	var mu sync.Mutex
	live := liveMetrics{}
	done := make(chan struct{})
	timeSeriesCount := 0

	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var m wsMetricsMessage
			if err := json.Unmarshal(msg, &m); err != nil {
				continue
			}

			mu.Lock()
			// Update metrics on each message
			live.PeakMgasPerSec = m.PeakMgasPerSec
			live.AvgMgasPerSec = m.AvgMgasPerSec
			live.AvgFillRate = m.AvgFillRate
			live.BlockCount = m.BlockCount
			live.TotalGasUsed = m.TotalGasUsed
			live.TxConfirmed = m.TxConfirmed
			live.TxSent = m.TxSent
			live.PeakTps = m.PeakTps
			live.AverageTps = m.AverageTps

			// Count time series points (messages with running status and currentMgasPerSec > 0)
			if m.Status == "running" && (m.CurrentMgasPerSec > 0 || m.CurrentTps > 0) {
				timeSeriesCount++
			}
			live.TimeSeriesPoints = timeSeriesCount

			// Stop when test completes
			if m.Status == "completed" || m.Status == "error" {
				mu.Unlock()
				return
			}
			mu.Unlock()
		}
	}()

	// Get history count before starting test
	previousTestIDs := getTestIDs(t)
	t.Logf("Previous test count: %d", len(previousTestIDs))

	// Start a load test
	testConfig := map[string]any{
		"pattern":         "constant",
		"durationSec":     15,
		"constantRate":    100,
		"numAccounts":     10,
		"transactionType": "eth-transfer",
	}

	startLoadTestLocal(t, testConfig)
	t.Log("Started load test")

	// Wait for WebSocket to capture completion
	select {
	case <-done:
		t.Log("WebSocket captured test completion")
	case <-time.After(60 * time.Second):
		t.Fatal("Timeout waiting for test completion via WebSocket")
	}

	// Capture final live values
	mu.Lock()
	liveFinal := live
	mu.Unlock()

	t.Logf("Live metrics captured: PeakMgas=%.2f, AvgMgas=%.2f, Blocks=%d, Fill=%.2f%%, TxConfirmed=%d, TimeSeriesPoints=%d",
		liveFinal.PeakMgasPerSec, liveFinal.AvgMgasPerSec, liveFinal.BlockCount,
		liveFinal.AvgFillRate, liveFinal.TxConfirmed, liveFinal.TimeSeriesPoints)

	// Wait a bit for history to be persisted
	time.Sleep(2 * time.Second)

	// Get the new test ID by comparing with previous test IDs
	testID := findNewTestID(t, previousTestIDs)
	t.Logf("Test ID from history: %s", testID)

	// Fetch history metrics
	history := fetchHistoryMetrics(t, testID)

	t.Logf("History metrics fetched: PeakMgas=%.2f, AvgMgas=%.2f, Blocks=%d, Fill=%.2f%%, TxConfirmed=%d, TimeSeriesPoints=%d",
		history.PeakMgasPerSec, history.AvgMgasPerSec, history.BlockCount,
		history.AvgFillRate, history.TxConfirmed, history.TimeSeriesPoints)

	// Compare metrics with tolerances
	// Note: Live and history metrics may differ slightly due to timing -
	// the WebSocket captures metrics at "completed" status, but history is persisted
	// at a slightly different moment. Allow for ±1 block timing difference.

	// Peak Mgas/s should match exactly (or very close)
	assertMetricClose(t, "PeakMgasPerSec", liveFinal.PeakMgasPerSec, history.PeakMgasPerSec, 0.5)

	// Block count may differ by ±1 due to timing
	blockDiff := liveFinal.BlockCount - history.BlockCount
	if blockDiff < -1 || blockDiff > 1 {
		t.Errorf("BlockCount mismatch: live=%d, history=%d (diff=%d, allowed ±1)",
			liveFinal.BlockCount, history.BlockCount, blockDiff)
	} else {
		t.Logf("BlockCount OK: live=%d, history=%d (diff=%d)", liveFinal.BlockCount, history.BlockCount, blockDiff)
	}

	// Fill rate should be close (within 1%)
	assertMetricClose(t, "AvgFillRate", liveFinal.AvgFillRate, history.AvgFillRate, 1.0)

	// TxConfirmed should match exactly
	assertMetricEqual(t, "TxConfirmed", int(liveFinal.TxConfirmed), int(history.TxConfirmed))

	// TxSent should match exactly
	assertMetricEqual(t, "TxSent", int(liveFinal.TxSent), int(history.TxSent))

	// Peak TPS should be close (within 5 TPS)
	assertMetricClose(t, "PeakTps", liveFinal.PeakTps, history.PeakTps, 5.0)

	// Average TPS should be close (within 10%)
	if liveFinal.AverageTps > 0 {
		tolerance := liveFinal.AverageTps * 0.1
		assertMetricClose(t, "AverageTps", liveFinal.AverageTps, history.AverageTps, tolerance)
	}

	// Total gas used should be close (within 10% to account for ±1 block timing difference)
	if liveFinal.TotalGasUsed > 0 {
		tolerance := float64(liveFinal.TotalGasUsed) * 0.10
		assertMetricClose(t, "TotalGasUsed", float64(liveFinal.TotalGasUsed), float64(history.TotalGasUsed), tolerance)
	}

	// Time series should have data points
	if liveFinal.TimeSeriesPoints < 10 {
		t.Errorf("Live time series has too few points: %d (expected >= 10)", liveFinal.TimeSeriesPoints)
	}
	if history.TimeSeriesPoints < 10 {
		t.Errorf("History time series has too few points: %d (expected >= 10)", history.TimeSeriesPoints)
	}

	t.Log("SUCCESS: Live and history metrics are consistent")
}

// TestE2EMetricsAPIFieldsPresent verifies all required metrics fields are present in API responses.
func TestE2EMetricsAPIFieldsPresent(t *testing.T) {
	skipIfNoLoadGen(t)

	// Get history count before starting test
	previousTestIDs := getTestIDs(t)

	// Start a quick test to generate data
	testConfig := map[string]any{
		"pattern":         "constant",
		"durationSec":     10,
		"constantRate":    50,
		"numAccounts":     5,
		"transactionType": "eth-transfer",
	}

	startLoadTestLocal(t, testConfig)
	waitForStatusIdle(t, 30*time.Second)

	// Verify status API has required metrics fields
	resp, err := http.Get(loadGenAPIURL + "/status")
	if err != nil {
		t.Fatalf("Failed to fetch status: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	// Status API fields (these are at the top level in /status response)
	statusFields := []string{
		"peakMgasPerSec",
		"avgMgasPerSec",
		"avgFillRate",
		"blockCount",
		"totalGasUsed",
	}

	for _, field := range statusFields {
		if !containsField(body, field) {
			t.Errorf("Status API missing field: %s", field)
		}
	}

	// Wait for history to be persisted
	time.Sleep(2 * time.Second)

	// Find the new test ID
	testID := findNewTestID(t, previousTestIDs)

	// Verify history detail API has required fields (inside "run" object)
	resp2, err := http.Get(loadGenAPIURL + "/history/" + testID)
	if err != nil {
		t.Fatalf("Failed to fetch history: %v", err)
	}
	defer resp2.Body.Close()

	body2, _ := io.ReadAll(resp2.Body)
	// History fields are inside "run" object, but we just check they exist in response
	historyFields := []string{
		"peakMgasPerSec",
		"avgMgasPerSec",
		"avgFillRate",
		"blockCount",
		"totalGasUsed",
		"timeSeries",
	}

	for _, field := range historyFields {
		if !containsField(body2, field) {
			t.Errorf("History API missing field: %s", field)
		}
	}

	t.Log("SUCCESS: All required metrics fields are present")
}

// TestE2ETimeSeriesDataQuality verifies time series data has sufficient granularity.
func TestE2ETimeSeriesDataQuality(t *testing.T) {
	skipIfNoLoadGen(t)

	// Get history count before starting test
	previousTestIDs := getTestIDs(t)

	// Run a 15-second test
	testConfig := map[string]any{
		"pattern":         "constant",
		"durationSec":     15,
		"constantRate":    100,
		"numAccounts":     10,
		"transactionType": "eth-transfer",
	}

	startLoadTestLocal(t, testConfig)
	waitForStatusIdle(t, 45*time.Second)

	// Wait for history to be persisted
	time.Sleep(2 * time.Second)

	// Find the new test ID
	testID := findNewTestID(t, previousTestIDs)

	// Fetch time series data
	resp, err := http.Get(loadGenAPIURL + "/history/" + testID)
	if err != nil {
		t.Fatalf("Failed to fetch history: %v", err)
	}
	defer resp.Body.Close()

	// Time series is an array of objects with timestampMs, currentTps, etc.
	var detail struct {
		TimeSeries []struct {
			TimestampMs  int64   `json:"timestampMs"`
			CurrentTps   float64 `json:"currentTps"`
			TxSent       uint64  `json:"txSent"`
			TxConfirmed  uint64  `json:"txConfirmed"`
			PendingCount int     `json:"pendingCount"`
		} `json:"timeSeries"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("Failed to decode history response: %v", err)
	}

	ts := detail.TimeSeries

	// Verify time series has data
	if len(ts) == 0 {
		t.Fatal("Time series has no data points")
	}

	t.Logf("Time series has %d points", len(ts))

	// For a 15-second test with 200ms sampling, expect at least 50 points
	minExpectedPoints := 50
	if len(ts) < minExpectedPoints {
		t.Errorf("Time series has too few points: %d (expected >= %d for 15s test at 200ms sampling)",
			len(ts), minExpectedPoints)
	}

	// Verify data has non-zero values
	nonZeroTps := 0
	for _, point := range ts {
		if point.CurrentTps > 0 {
			nonZeroTps++
		}
	}

	if nonZeroTps == 0 {
		t.Error("Time series has no non-zero TPS values")
	}

	t.Logf("Time series quality: %d/%d non-zero TPS points", nonZeroTps, len(ts))

	t.Log("SUCCESS: Time series data quality is good")
}

// fetchHistoryMetrics retrieves metrics from the history API for comparison.
func fetchHistoryMetrics(t *testing.T, testID string) historyMetrics {
	t.Helper()

	resp, err := http.Get(loadGenAPIURL + "/history/" + testID)
	if err != nil {
		t.Fatalf("Failed to fetch history: %v", err)
	}
	defer resp.Body.Close()

	// The API returns all metrics in "run" object, timeSeries is an array
	var detail struct {
		Run struct {
			TxSent         uint64  `json:"txSent"`
			TxConfirmed    uint64  `json:"txConfirmed"`
			AverageTps     float64 `json:"averageTps"`
			PeakTps        int     `json:"peakTps"`
			PeakMgasPerSec float64 `json:"peakMgasPerSec"`
			AvgMgasPerSec  float64 `json:"avgMgasPerSec"`
			AvgFillRate    float64 `json:"avgFillRate"`
			BlockCount     int     `json:"blockCount"`
			TotalGasUsed   uint64  `json:"totalGasUsed"`
		} `json:"run"`
		TimeSeries []struct {
			TimestampMs int64 `json:"timestampMs"`
		} `json:"timeSeries"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("Failed to decode history response: %v", err)
	}

	return historyMetrics{
		PeakMgasPerSec:   detail.Run.PeakMgasPerSec,
		AvgMgasPerSec:    detail.Run.AvgMgasPerSec,
		AvgFillRate:      detail.Run.AvgFillRate,
		BlockCount:       detail.Run.BlockCount,
		TotalGasUsed:     detail.Run.TotalGasUsed,
		TxConfirmed:      detail.Run.TxConfirmed,
		TxSent:           detail.Run.TxSent,
		PeakTps:          float64(detail.Run.PeakTps),
		AverageTps:       detail.Run.AverageTps,
		TimeSeriesPoints: len(detail.TimeSeries),
	}
}

// assertMetricClose checks if two float values are within tolerance.
func assertMetricClose(t *testing.T, name string, live, history, tolerance float64) {
	t.Helper()
	diff := math.Abs(live - history)
	if diff > tolerance {
		t.Errorf("%s mismatch: live=%.4f, history=%.4f, diff=%.4f (tolerance=%.4f)",
			name, live, history, diff, tolerance)
	} else {
		t.Logf("%s OK: live=%.4f, history=%.4f, diff=%.4f", name, live, history, diff)
	}
}

// assertMetricEqual checks if two int values are equal.
func assertMetricEqual(t *testing.T, name string, live, history int) {
	t.Helper()
	if live != history {
		t.Errorf("%s mismatch: live=%d, history=%d", name, live, history)
	} else {
		t.Logf("%s OK: live=%d, history=%d", name, live, history)
	}
}

// containsField checks if a JSON response contains a field name.
func containsField(body []byte, field string) bool {
	// Check for "fieldName": pattern
	pattern := fmt.Sprintf(`"%s":`, field)
	return string(body) != "" && len(body) > 0 && contains(string(body), pattern)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// waitForStatusIdle waits for the load generator status to become idle or completed.
func waitForStatusIdle(t *testing.T, timeout time.Duration) {
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

	t.Fatal("Timeout waiting for test completion")
}

// startLoadTestLocal starts a load test (local version that doesn't expect testId in response).
func startLoadTestLocal(t *testing.T, config map[string]any) {
	t.Helper()

	body, _ := json.Marshal(config)
	resp, err := http.Post(loadGenAPIURL+"/start", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to start load test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("Failed to start load test: %s - %s", resp.Status, string(respBody))
	}
}

// getTestIDs returns all test IDs from history as a map for easy lookup.
func getTestIDs(t *testing.T) map[string]bool {
	t.Helper()

	resp, err := http.Get(loadGenAPIURL + "/history")
	if err != nil {
		t.Fatalf("Failed to fetch history: %v", err)
	}
	defer resp.Body.Close()

	var history struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatalf("Failed to decode history: %v", err)
	}

	ids := make(map[string]bool)
	for _, run := range history.Runs {
		ids[run.ID] = true
	}
	return ids
}

// findNewTestID returns the first test ID that wasn't in the previous set.
func findNewTestID(t *testing.T, previousIDs map[string]bool) string {
	t.Helper()

	resp, err := http.Get(loadGenAPIURL + "/history")
	if err != nil {
		t.Fatalf("Failed to fetch history: %v", err)
	}
	defer resp.Body.Close()

	var history struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatalf("Failed to decode history: %v", err)
	}

	// Find the new test ID (one that wasn't in previousIDs)
	for _, run := range history.Runs {
		if !previousIDs[run.ID] {
			return run.ID
		}
	}

	t.Fatal("No new test found in history after running test")
	return ""
}
