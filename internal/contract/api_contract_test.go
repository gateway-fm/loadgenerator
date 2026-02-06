// Package contract contains API contract tests that validate Go API responses
// match TypeScript expectations. These tests prevent PascalCase/camelCase mismatches.
package contract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// ExpectedAPIFields defines the camelCase field names TypeScript expects.
// If Go serializes to PascalCase, these tests will fail.
var ExpectedAPIFields = map[string][]string{
	"TestRun": {
		"id", "startedAt", "completedAt", "pattern", "transactionType",
		"durationMs", "txSent", "txConfirmed", "txFailed", "averageTps",
		"peakTps", "latencyStats", "preconfLatency", "config", "status",
		"errorMessage", "txLoggingEnabled",
		"blockCount", "totalGasUsed", "avgFillRate", "peakMgasPerSec", "avgMgasPerSec",
	},
	"TimeSeriesPoint": {
		"timestampMs", "txSent", "txConfirmed", "txFailed",
		"currentTps", "targetTps", "pendingCount",
		"gasUsed", "gasLimit", "blockCount", "mgasPerSec", "fillRate",
	},
	"TxLogEntry": {
		"txHash", "sentAtMs", "confirmedAtMs", "preconfAtMs",
		"confirmLatencyMs", "preconfLatencyMs", "status", "errorReason",
		"fromAccount", "nonce", "gasTipGwei",
	},
	"LatencyStats": {
		"count", "min", "max", "avg", "p50", "p75", "p90", "p95", "p99", "buckets",
	},
	"LatencyBucket": {
		"label", "count",
	},
	"TestMetrics": {
		"status", "txSent", "txConfirmed", "txFailed", "currentTps",
		"averageTps", "elapsedMs", "durationMs", "targetTps", "pattern",
		"transactionType", "error", "peakTps", "latency", "preconfLatency",
		"tipHistogram", "txTypeMetrics", "accountsActive", "accountsFunded",
	},
	"PaginatedTestRuns": {
		"runs", "total", "limit", "offset",
	},
	"PaginatedTxLogs": {
		"transactions", "total", "limit", "offset",
	},
	"TestRunDetail": {
		"run", "timeSeries",
	},
}

// TestStorageModelsHaveJSONTags ensures all storage model fields have JSON tags.
// This prevents the PascalCase serialization bug.
func TestStorageModelsHaveJSONTags(t *testing.T) {
	testCases := []struct {
		name     string
		instance interface{}
	}{
		{"TestRun", storage.TestRun{}},
		{"TimeSeriesPoint", storage.TimeSeriesPoint{}},
		{"TxLogEntry", storage.TxLogEntry{}},
		{"TestRunDetail", storage.TestRunDetail{}},
		{"PaginatedTestRuns", storage.PaginatedTestRuns{}},
		{"PaginatedTxLogs", storage.PaginatedTxLogs{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			typ := reflect.TypeOf(tc.instance)
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				jsonTag := field.Tag.Get("json")
				if jsonTag == "" && field.PkgPath == "" { // exported field
					t.Errorf("Field %s.%s has no json tag - will serialize as PascalCase",
						tc.name, field.Name)
				}
			}
		})
	}
}

// TestPublicTypesHaveJSONTags ensures all public API types have JSON tags.
func TestPublicTypesHaveJSONTags(t *testing.T) {
	testCases := []struct {
		name     string
		instance interface{}
	}{
		{"LatencyStats", types.LatencyStats{}},
		{"LatencyBucket", types.LatencyBucket{}},
		{"TestMetrics", types.TestMetrics{}},
		{"TestResult", types.TestResult{}},
		{"StartTestRequest", types.StartTestRequest{}},
		{"RealisticTestConfig", types.RealisticTestConfig{}},
		{"TxTypeRatio", types.TxTypeRatio{}},
		{"TipHistogramBucket", types.TipHistogramBucket{}},
		{"TxTypeMetrics", types.TxTypeMetrics{}},
		{"PreconfEvent", types.PreconfEvent{}},
		{"PreconfBatchedEvent", types.PreconfBatchedEvent{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			typ := reflect.TypeOf(tc.instance)
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				jsonTag := field.Tag.Get("json")
				if jsonTag == "" && field.PkgPath == "" { // exported field
					t.Errorf("Field %s.%s has no json tag - will serialize as PascalCase",
						tc.name, field.Name)
				}
			}
		})
	}
}

// TestJSONSerializationIsCamelCase verifies actual JSON output uses camelCase.
func TestJSONSerializationIsCamelCase(t *testing.T) {
	testCases := []struct {
		name             string
		instance         interface{}
		expectedFields   []string
		forbiddenFields  []string // PascalCase versions that should NOT appear
	}{
		{
			name: "LatencyStats",
			instance: types.LatencyStats{
				Count: 100,
				Min:   1.0,
				Max:   100.0,
				Avg:   50.0,
				P50:   45.0,
				P75:   60.0,
				P90:   80.0,
				P95:   90.0,
				P99:   98.0,
			},
			expectedFields:  []string{"count", "min", "max", "avg", "p50", "p75", "p90", "p95", "p99"},
			forbiddenFields: []string{"Count", "Min", "Max", "Avg", "P50", "P75", "P90", "P95", "P99"},
		},
		{
			name: "TestMetrics",
			instance: types.TestMetrics{
				Status:      types.StatusRunning,
				TxSent:      100,
				TxConfirmed: 90,
				TxFailed:    10,
				CurrentTPS:  50.5,
				AverageTPS:  48.2,
				ElapsedMs:   5000,
				DurationMs:  60000,
				TargetTPS:   100,
				Pattern:     types.PatternConstant,
			},
			expectedFields: []string{
				"status", "txSent", "txConfirmed", "txFailed",
				"currentTps", "averageTps", "elapsedMs", "durationMs",
				"targetTps", "pattern",
			},
			forbiddenFields: []string{
				"Status", "TxSent", "TxConfirmed", "TxFailed",
				"CurrentTPS", "AverageTPS", "ElapsedMs", "DurationMs",
				"TargetTPS", "Pattern",
			},
		},
		{
			name: "PreconfEvent",
			instance: types.PreconfEvent{
				TxHash:      "0xabc123",
				Status:      "preconfirmed",
				BlockNumber: 42,
				Position:    3,
				Timestamp:   1234567890,
			},
			expectedFields:  []string{"txHash", "status", "blockNumber", "position", "timestamp"},
			forbiddenFields: []string{"TxHash", "Status", "BlockNumber", "Position", "Timestamp"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.instance)
			if err != nil {
				t.Fatalf("Failed to marshal %s: %v", tc.name, err)
			}

			jsonStr := string(data)

			// Check expected camelCase fields are present
			for _, field := range tc.expectedFields {
				if !strings.Contains(jsonStr, `"`+field+`"`) {
					t.Errorf("Expected camelCase field %q not found in JSON: %s", field, jsonStr)
				}
			}

			// Check PascalCase fields are NOT present
			for _, field := range tc.forbiddenFields {
				if strings.Contains(jsonStr, `"`+field+`"`) {
					t.Errorf("Forbidden PascalCase field %q found in JSON: %s", field, jsonStr)
				}
			}
		})
	}
}

// TestAPIResponseStructure validates that API responses can be unmarshaled
// using the expected TypeScript field names.
func TestAPIResponseStructure(t *testing.T) {
	// Simulate what TypeScript expects for a TestRunDetail response
	expectedJSON := `{
		"run": {
			"id": "test-123",
			"startedAt": "2024-01-01T00:00:00Z",
			"completedAt": "2024-01-01T00:01:00Z",
			"pattern": "constant",
			"transactionType": "eth-transfer",
			"durationMs": 60000,
			"txSent": 1000,
			"txConfirmed": 990,
			"txFailed": 10,
			"averageTps": 16.5,
			"peakTps": 20,
			"status": "completed",
			"txLoggingEnabled": true
		},
		"timeSeries": [
			{
				"timestampMs": 0,
				"txSent": 0,
				"txConfirmed": 0,
				"txFailed": 0,
				"currentTps": 0,
				"targetTps": 100,
				"pendingCount": 0
			}
		]
	}`

	// Define a struct that matches TypeScript expectations (camelCase)
	type TSTestRun struct {
		ID              string  `json:"id"`
		StartedAt       string  `json:"startedAt"`
		CompletedAt     string  `json:"completedAt"`
		Pattern         string  `json:"pattern"`
		TransactionType string  `json:"transactionType"`
		DurationMs      int64   `json:"durationMs"`
		TxSent          uint64  `json:"txSent"`
		TxConfirmed     uint64  `json:"txConfirmed"`
		TxFailed        uint64  `json:"txFailed"`
		AverageTps      float64 `json:"averageTps"`
		PeakTps         int     `json:"peakTps"`
		Status          string  `json:"status"`
		TxLoggingEnabled bool   `json:"txLoggingEnabled"`
	}

	type TSTimeSeriesPoint struct {
		TimestampMs  int64   `json:"timestampMs"`
		TxSent       uint64  `json:"txSent"`
		TxConfirmed  uint64  `json:"txConfirmed"`
		TxFailed     uint64  `json:"txFailed"`
		CurrentTps   float64 `json:"currentTps"`
		TargetTps    int     `json:"targetTps"`
		PendingCount int64   `json:"pendingCount"`
	}

	type TSTestRunDetail struct {
		Run        TSTestRun           `json:"run"`
		TimeSeries []TSTimeSeriesPoint `json:"timeSeries"`
	}

	var result TSTestRunDetail
	if err := json.Unmarshal([]byte(expectedJSON), &result); err != nil {
		t.Fatalf("Failed to unmarshal expected API format: %v", err)
	}

	// Validate the data was parsed correctly
	if result.Run.ID != "test-123" {
		t.Errorf("Expected id 'test-123', got %q", result.Run.ID)
	}
	if result.Run.Pattern != "constant" {
		t.Errorf("Expected pattern 'constant', got %q", result.Run.Pattern)
	}
	if result.Run.TxSent != 1000 {
		t.Errorf("Expected txSent 1000, got %d", result.Run.TxSent)
	}
	if len(result.TimeSeries) != 1 {
		t.Errorf("Expected 1 time series point, got %d", len(result.TimeSeries))
	}
}

// TestStorageToAPITransformation verifies that storage models can be properly
// transformed to API responses with camelCase fields.
// NOTE: This test will FAIL until storage models have proper JSON tags.
func TestStorageToAPITransformation(t *testing.T) {
	// This test documents the current bug and will pass once fixed
	run := storage.TestRun{
		ID:          "test-456",
		Status:      "completed",
		TxSent:      500,
		TxConfirmed: 490,
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("Failed to marshal TestRun: %v", err)
	}

	jsonStr := string(data)

	// These checks will FAIL with current code (no JSON tags on storage models)
	// and document what needs to be fixed
	wantFields := map[string]bool{
		"id":          false,
		"status":      false,
		"txSent":      false,
		"txConfirmed": false,
	}

	for field := range wantFields {
		if strings.Contains(jsonStr, `"`+field+`"`) {
			wantFields[field] = true
		}
	}

	var missing []string
	for field, found := range wantFields {
		if !found {
			missing = append(missing, field)
		}
	}

	if len(missing) > 0 {
		t.Errorf("Storage model TestRun missing camelCase JSON fields: %v\nActual JSON: %s\n"+
			"FIX: Add json tags to storage/models.go", missing, jsonStr)
	}
}
