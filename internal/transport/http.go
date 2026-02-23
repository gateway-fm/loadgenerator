// Package transport provides HTTP API handlers.
package transport

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Input validation constants
const (
	maxDurationSec  = 3600    // Maximum test duration: 1 hour
	maxTPS          = 100000  // Maximum TPS
	maxAccounts     = 100000  // Maximum number of accounts
	maxRampSteps    = 1000    // Maximum ramp steps
	maxSpikeRate    = 100000  // Maximum spike rate
	maxSpikeDur     = 3600    // Maximum spike duration
	maxSpikeInt     = 3600    // Maximum spike interval
)

// validPatterns contains all valid load patterns
var validPatterns = map[types.LoadPattern]bool{
	types.PatternConstant:          true,
	types.PatternRamp:              true,
	types.PatternSpike:             true,
	types.PatternAdaptive:          true,
	types.PatternRealistic:         true,
	types.PatternAdaptiveRealistic: true,
}

// validTxTypes contains all valid transaction types
var validTxTypes = map[types.TransactionType]bool{
	types.TxTypeEthTransfer:   true,
	types.TxTypeERC20Transfer: true,
	types.TxTypeERC20Approve:  true,
	types.TxTypeUniswapSwap:   true,
	types.TxTypeStorageWrite:  true,
	types.TxTypeHeavyCompute:  true,
	"":                        true, // Empty is valid (default)
}

// validateStartRequest validates the start test request parameters
func validateStartRequest(req *types.StartTestRequest) error {
	// Pattern validation
	if !validPatterns[req.Pattern] {
		return fmt.Errorf("invalid pattern: %s (valid: constant, ramp, spike, adaptive, realistic, adaptive-realistic)", req.Pattern)
	}

	// Duration validation
	if req.DurationSec <= 0 {
		return fmt.Errorf("durationSec must be positive, got %d", req.DurationSec)
	}
	if req.DurationSec > maxDurationSec {
		return fmt.Errorf("durationSec exceeds maximum of %d seconds", maxDurationSec)
	}

	// NumAccounts validation (if specified)
	if req.NumAccounts < 0 {
		return fmt.Errorf("numAccounts cannot be negative, got %d", req.NumAccounts)
	}
	if req.NumAccounts > maxAccounts {
		return fmt.Errorf("numAccounts exceeds maximum of %d", maxAccounts)
	}

	// TransactionType validation
	if !validTxTypes[req.TransactionType] {
		return fmt.Errorf("invalid transactionType: %s", req.TransactionType)
	}

	// Pattern-specific validation
	switch req.Pattern {
	case types.PatternConstant:
		if req.ConstantRate <= 0 {
			return fmt.Errorf("constantRate must be positive for constant pattern, got %d", req.ConstantRate)
		}
		if req.ConstantRate > maxTPS {
			return fmt.Errorf("constantRate exceeds maximum of %d TPS", maxTPS)
		}

	case types.PatternRamp:
		if req.RampStart < 0 {
			return fmt.Errorf("rampStart cannot be negative, got %d", req.RampStart)
		}
		if req.RampEnd <= 0 {
			return fmt.Errorf("rampEnd must be positive, got %d", req.RampEnd)
		}
		if req.RampEnd > maxTPS {
			return fmt.Errorf("rampEnd exceeds maximum of %d TPS", maxTPS)
		}
		if req.RampSteps <= 0 {
			return fmt.Errorf("rampSteps must be positive, got %d", req.RampSteps)
		}
		if req.RampSteps > maxRampSteps {
			return fmt.Errorf("rampSteps exceeds maximum of %d", maxRampSteps)
		}

	case types.PatternSpike:
		if req.BaselineRate < 0 {
			return fmt.Errorf("baselineRate cannot be negative, got %d", req.BaselineRate)
		}
		if req.SpikeRate <= 0 {
			return fmt.Errorf("spikeRate must be positive, got %d", req.SpikeRate)
		}
		if req.SpikeRate > maxSpikeRate {
			return fmt.Errorf("spikeRate exceeds maximum of %d TPS", maxSpikeRate)
		}
		if req.SpikeDuration <= 0 {
			return fmt.Errorf("spikeDuration must be positive, got %d", req.SpikeDuration)
		}
		if req.SpikeDuration > maxSpikeDur {
			return fmt.Errorf("spikeDuration exceeds maximum of %d seconds", maxSpikeDur)
		}
		if req.SpikeInterval <= 0 {
			return fmt.Errorf("spikeInterval must be positive, got %d", req.SpikeInterval)
		}
		if req.SpikeInterval > maxSpikeInt {
			return fmt.Errorf("spikeInterval exceeds maximum of %d seconds", maxSpikeInt)
		}

	case types.PatternAdaptive:
		if req.AdaptiveInitialRate < 0 {
			return fmt.Errorf("adaptiveInitialRate cannot be negative, got %d", req.AdaptiveInitialRate)
		}
		if req.AdaptiveInitialRate > maxTPS {
			return fmt.Errorf("adaptiveInitialRate exceeds maximum of %d TPS", maxTPS)
		}

	case types.PatternAdaptiveRealistic:
		// Adaptive-realistic uses adaptive rate control with default realistic TX types
		if req.AdaptiveInitialRate < 0 {
			return fmt.Errorf("adaptiveInitialRate cannot be negative, got %d", req.AdaptiveInitialRate)
		}
		if req.AdaptiveInitialRate > maxTPS {
			return fmt.Errorf("adaptiveInitialRate exceeds maximum of %d TPS", maxTPS)
		}

	case types.PatternRealistic:
		if req.RealisticConfig == nil {
			return fmt.Errorf("realisticConfig is required for realistic pattern")
		}
		cfg := req.RealisticConfig
		if cfg.NumAccounts < 0 {
			return fmt.Errorf("realisticConfig.numAccounts cannot be negative, got %d", cfg.NumAccounts)
		}
		// NumAccounts == 0 is valid: triggers auto-calculation based on target TPS
		if cfg.NumAccounts > maxAccounts {
			return fmt.Errorf("realisticConfig.numAccounts exceeds maximum of %d", maxAccounts)
		}
		if cfg.TargetTPS <= 0 {
			return fmt.Errorf("realisticConfig.targetTps must be positive, got %d", cfg.TargetTPS)
		}
		if cfg.TargetTPS > maxTPS {
			return fmt.Errorf("realisticConfig.targetTps exceeds maximum of %d", maxTPS)
		}
		if cfg.MinTipGwei < 0 {
			return fmt.Errorf("realisticConfig.minTipGwei cannot be negative, got %f", cfg.MinTipGwei)
		}
		if cfg.MaxTipGwei < cfg.MinTipGwei {
			return fmt.Errorf("realisticConfig.maxTipGwei (%f) must be >= minTipGwei (%f)", cfg.MaxTipGwei, cfg.MinTipGwei)
		}
	}

	return nil
}

// LoadGeneratorAPI defines the interface for the load generator that handlers need.
type LoadGeneratorAPI interface {
	StartTest(req types.StartTestRequest) error
	StopTest()
	Reset()
	GetMetrics() types.TestMetrics
	GetHistory() []types.TestResult

	// New methods for persistent storage
	GetHistoryPaginated(limit, offset int) (*storage.PaginatedTestRuns, error)
	GetTestRunDetail(id string) (*storage.TestRunDetail, error)
	GetTestRunTransactions(id string, limit, offset int) (*storage.PaginatedTxLogs, error)
	DeleteTestRun(id string) error
	UpdateTestRunMetadata(id string, update *storage.TestRunMetadataUpdate) error

	// Fund recycling
	RecycleFunds() (int, error)
}

// HealthChecker defines the interface for health checking.
type HealthChecker interface {
	CheckL2RPC() error
	CheckBuilderRPC() error
}

// Server handles HTTP requests for the load generator.
type Server struct {
	api       LoadGeneratorAPI
	health    HealthChecker
	logger    *slog.Logger
	startTime time.Time
	wsServer  *WebSocketServer

	// CORS configuration
	corsAllowedOrigins []string // Parsed list of allowed origins
	corsAllowAll       bool     // True if "*" or empty (allow all origins)
}

// NewServer creates a new HTTP server.
func NewServer(api LoadGeneratorAPI, health HealthChecker, logger *slog.Logger, corsAllowedOrigins string) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	// Create WebSocket server for real-time metrics streaming
	wsServer := NewWebSocketServer(api, logger)
	wsServer.Start()

	s := &Server{
		api:       api,
		health:    health,
		logger:    logger,
		startTime: time.Now(),
		wsServer:  wsServer,
	}

	// Parse CORS allowed origins
	origins := strings.TrimSpace(corsAllowedOrigins)
	if origins == "" || origins == "*" {
		s.corsAllowAll = true
	} else {
		s.corsAllowedOrigins = strings.Split(origins, ",")
		for i, o := range s.corsAllowedOrigins {
			s.corsAllowedOrigins[i] = strings.TrimSpace(o)
		}
	}

	return s
}

// Handler returns an http.Handler with all routes configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Versioned API endpoints (v1)
	mux.HandleFunc("/v1/status", s.corsMiddleware(s.handleStatus))
	mux.HandleFunc("/v1/start", s.corsMiddleware(s.handleStart))
	mux.HandleFunc("/v1/stop", s.corsMiddleware(s.handleStop))
	mux.HandleFunc("/v1/reset", s.corsMiddleware(s.handleReset))
	mux.HandleFunc("/v1/recycle", s.corsMiddleware(s.handleRecycle))
	mux.HandleFunc("/v1/history", s.corsMiddleware(s.handleHistory))
	mux.HandleFunc("/v1/history/", s.corsMiddleware(s.handleHistoryDetail))
	mux.HandleFunc("/v1/ws", s.wsServer.Handler())

	// Legacy unversioned endpoints (for backwards compatibility)
	// These redirect to or mirror the v1 endpoints
	mux.HandleFunc("/status", s.corsMiddleware(s.handleStatus))
	mux.HandleFunc("/start", s.corsMiddleware(s.handleStart))
	mux.HandleFunc("/stop", s.corsMiddleware(s.handleStop))
	mux.HandleFunc("/reset", s.corsMiddleware(s.handleReset))
	mux.HandleFunc("/recycle", s.corsMiddleware(s.handleRecycle))
	mux.HandleFunc("/history", s.corsMiddleware(s.handleHistory))
	mux.HandleFunc("/history/", s.corsMiddleware(s.handleHistoryDetail))
	mux.HandleFunc("/ws", s.wsServer.Handler())

	// Health endpoints (unversioned - standard Kubernetes probes)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)

	// Prometheus metrics (unversioned - standard path)
	mux.Handle("/metrics", promhttp.Handler())

	return mux
}

// corsMiddleware adds CORS headers based on the configured allowed origins.
func (s *Server) corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if s.corsAllowAll {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			// Check if the origin is in the allowed list
			allowed := false
			for _, o := range s.corsAllowedOrigins {
				if o == origin {
					allowed = true
					break
				}
			}
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// handleStatus returns current test metrics.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	metrics := s.api.GetMetrics()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// handleStart starts a new load test.
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.StartTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSONError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate request parameters
	if err := validateStartRequest(&req); err != nil {
		s.writeJSONError(w, "Validation error: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.api.StartTest(req); err != nil {
		s.logger.Error("Failed to start test", slog.String("error", err.Error()))
		s.writeJSONError(w, "Failed to start test: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// writeJSONError writes a JSON error response
func (s *Server) writeJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// handleStop stops the current test.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.api.StopTest()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// handleReset resets the load generator.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.api.Reset()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "reset"})
}

// handleRecycle recycles funds from dynamic accounts back to faucets.
func (s *Server) handleRecycle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	count, err := s.api.RecycleFunds()
	if err != nil {
		s.logger.Error("Failed to recycle funds", slog.String("error", err.Error()))
		// Still return partial success with error message
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "partial",
			"recycled": count,
			"error":    err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "success",
		"recycled": count,
	})
}

// handleHistory returns test history with optional pagination.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse pagination parameters (always use database for complete data)
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50 // default
	offset := 0

	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	result, err := s.api.GetHistoryPaginated(limit, offset)
	if err != nil {
		s.writeJSONError(w, "Failed to get history: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleHistoryDetail handles /history/{id} and /history/{id}/transactions routes.
func (s *Server) handleHistoryDetail(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path: /history/{id} or /history/{id}/transactions
	path := strings.TrimPrefix(r.URL.Path, "/history/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		s.writeJSONError(w, "Missing test ID", http.StatusBadRequest)
		return
	}

	testID := parts[0]

	// Check if this is a transactions request
	if len(parts) > 1 && parts[1] == "transactions" {
		s.handleTestTransactions(w, r, testID)
		return
	}

	// Handle DELETE /history/{id}
	if r.Method == http.MethodDelete {
		if err := s.api.DeleteTestRun(testID); err != nil {
			s.writeJSONError(w, "Failed to delete test run: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
		return
	}

	// Handle PATCH /history/{id} - update metadata (name, favorite)
	if r.Method == http.MethodPatch {
		var update storage.TestRunMetadataUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			s.writeJSONError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := s.api.UpdateTestRunMetadata(testID, &update); err != nil {
			if strings.Contains(err.Error(), "not found") {
				s.writeJSONError(w, err.Error(), http.StatusNotFound)
				return
			}
			s.writeJSONError(w, "Failed to update test run: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Return updated test run
		detail, err := s.api.GetTestRunDetail(testID)
		if err != nil {
			s.writeJSONError(w, "Failed to get updated test run: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(detail.Run)
		return
	}

	// Handle GET /history/{id}
	if r.Method != http.MethodGet {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	detail, err := s.api.GetTestRunDetail(testID)
	if err != nil {
		s.writeJSONError(w, "Failed to get test run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil {
		s.writeJSONError(w, "Test run not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

// handleTestTransactions handles GET /history/{id}/transactions.
func (s *Server) handleTestTransactions(w http.ResponseWriter, r *http.Request, testID string) {
	if r.Method != http.MethodGet {
		s.writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse pagination parameters
	limit := 100 // default
	offset := 0

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	result, err := s.api.GetTestRunTransactions(testID, limit, offset)
	if err != nil {
		s.writeJSONError(w, "Failed to get transactions: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleHealth handles liveness probes.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "healthy",
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"uptime_seconds": time.Since(s.startTime).Seconds(),
	})
}

// ReadinessCheck represents a single readiness check result.
type ReadinessCheck struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "ok", "degraded", "failed"
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleReady handles readiness probes.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	checks := []ReadinessCheck{}
	allHealthy := true

	// Check L2 RPC
	if s.health != nil {
		start := time.Now()
		err := s.health.CheckL2RPC()
		latency := time.Since(start).Milliseconds()

		check := ReadinessCheck{
			Name:      "l2-rpc",
			LatencyMs: latency,
		}
		if err != nil {
			check.Status = "failed"
			check.Error = err.Error()
			allHealthy = false
		} else {
			check.Status = "ok"
		}
		checks = append(checks, check)

		// Check Builder RPC
		start = time.Now()
		err = s.health.CheckBuilderRPC()
		latency = time.Since(start).Milliseconds()

		check = ReadinessCheck{
			Name:      "builder-rpc",
			LatencyMs: latency,
		}
		if err != nil {
			check.Status = "failed"
			check.Error = err.Error()
			allHealthy = false
		} else {
			check.Status = "ok"
		}
		checks = append(checks, check)
	}

	response := map[string]interface{}{
		"ready":  allHealthy,
		"checks": checks,
	}

	w.Header().Set("Content-Type", "application/json")
	if allHealthy {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(response)
}
