package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusMetrics holds all Prometheus metrics for the load generator.
type PrometheusMetrics struct {
	// Transaction counters
	TxTotal *prometheus.CounterVec

	// Gauges
	CurrentTPS   prometheus.Gauge
	TargetTPS    prometheus.Gauge
	PendingTxs   prometheus.Gauge
	TestStatus   *prometheus.GaugeVec

	// Histograms
	ConfirmLatency *prometheus.HistogramVec
	PreconfLatency prometheus.Histogram
	RPCLatency     *prometheus.HistogramVec
	GasTipGwei     prometheus.Histogram

	// Error tracking
	ErrorsTotal *prometheus.CounterVec
}

// NewPrometheusMetrics creates and registers all Prometheus metrics.
func NewPrometheusMetrics(reg prometheus.Registerer) *PrometheusMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	factory := promauto.With(reg)

	return &PrometheusMetrics{
		TxTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadgen_transactions_total",
				Help: "Total transactions by status and type",
			},
			[]string{"status", "tx_type"},
		),

		CurrentTPS: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "loadgen_current_tps",
				Help: "Current transactions per second",
			},
		),

		TargetTPS: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "loadgen_target_tps",
				Help: "Target transactions per second",
			},
		),

		PendingTxs: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "loadgen_pending_transactions",
				Help: "Estimated pending transactions",
			},
		),

		TestStatus: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "loadgen_test_status",
				Help: "Current test status (1 if active, 0 otherwise)",
			},
			[]string{"status"},
		),

		ConfirmLatency: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadgen_confirmation_latency_seconds",
				Help:    "Transaction confirmation latency in seconds",
				Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
			},
			[]string{"tx_type"},
		),

		PreconfLatency: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "loadgen_preconf_latency_seconds",
				Help:    "Preconfirmation latency in seconds",
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
			},
		),

		RPCLatency: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadgen_rpc_latency_seconds",
				Help:    "RPC call latency by method",
				Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
			},
			[]string{"method", "status"},
		),

		GasTipGwei: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "loadgen_gas_tip_gwei",
				Help:    "Gas tip distribution in Gwei",
				Buckets: []float64{0, 0.1, 0.5, 1, 2, 5, 10, 20, 50, 100},
			},
		),

		ErrorsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadgen_errors_total",
				Help: "Errors by category and tx_type",
			},
			[]string{"category", "tx_type"},
		),
	}
}

// RecordTxSent records a sent transaction.
func (m *PrometheusMetrics) RecordTxSent(txType string) {
	m.TxTotal.WithLabelValues("sent", txType).Inc()
}

// RecordTxConfirmed records a confirmed transaction.
func (m *PrometheusMetrics) RecordTxConfirmed(txType string) {
	m.TxTotal.WithLabelValues("confirmed", txType).Inc()
}

// RecordTxFailed records a failed transaction.
func (m *PrometheusMetrics) RecordTxFailed(txType string) {
	m.TxTotal.WithLabelValues("failed", txType).Inc()
}

// RecordConfirmLatency records confirmation latency.
func (m *PrometheusMetrics) RecordConfirmLatency(txType string, latencySeconds float64) {
	m.ConfirmLatency.WithLabelValues(txType).Observe(latencySeconds)
}

// RecordPreconfLatency records preconfirmation latency.
func (m *PrometheusMetrics) RecordPreconfLatency(latencySeconds float64) {
	m.PreconfLatency.Observe(latencySeconds)
}

// knownRPCMethods is a fixed set of known RPC methods to prevent cardinality explosion
var knownRPCMethods = map[string]bool{
	"eth_sendRawTransaction":   true,
	"eth_getPendingNonce":      true,
	"eth_getTransactionCount":  true,
	"eth_blockNumber":          true,
	"eth_getBlockByNumber":     true,
	"eth_getCode":              true,
	"eth_gasPrice":             true,
	"eth_getBalance":           true,
	"eth_getTransactionReceipt": true,
	"eth_call":                 true,
}

// RecordRPCLatency records RPC call latency.
func (m *PrometheusMetrics) RecordRPCLatency(method string, success bool, latencySeconds float64) {
	// Bucket unknown methods into 'other' to prevent cardinality explosion
	bucketedMethod := method
	if !knownRPCMethods[method] {
		bucketedMethod = "other"
	}

	status := "success"
	if !success {
		status = "error"
	}
	m.RPCLatency.WithLabelValues(bucketedMethod, status).Observe(latencySeconds)
}

// RecordGasTip records a gas tip value.
func (m *PrometheusMetrics) RecordGasTip(tipGwei float64) {
	m.GasTipGwei.Observe(tipGwei)
}

// RecordError records an error.
func (m *PrometheusMetrics) RecordError(category, txType string) {
	m.ErrorsTotal.WithLabelValues(category, txType).Inc()
}

// SetCurrentTPS updates the current TPS gauge.
func (m *PrometheusMetrics) SetCurrentTPS(tps float64) {
	m.CurrentTPS.Set(tps)
}

// SetTargetTPS updates the target TPS gauge.
func (m *PrometheusMetrics) SetTargetTPS(tps float64) {
	m.TargetTPS.Set(tps)
}

// SetPendingTxs updates the pending transactions gauge.
func (m *PrometheusMetrics) SetPendingTxs(count int64) {
	m.PendingTxs.Set(float64(count))
}

// SetTestStatus updates the test status gauges.
func (m *PrometheusMetrics) SetTestStatus(status string) {
	// Reset all statuses
	for _, s := range []string{"idle", "running", "verifying", "completed", "error"} {
		if s == status {
			m.TestStatus.WithLabelValues(s).Set(1)
		} else {
			m.TestStatus.WithLabelValues(s).Set(0)
		}
	}
}

// Reset resets all metrics.
// Note: Prometheus histograms don't have a Reset method - they are cumulative by design.
// Only CounterVec and GaugeVec support Reset(). For histograms, the buckets accumulate
// across test runs, which is the expected Prometheus behavior.
func (m *PrometheusMetrics) Reset() {
	m.TxTotal.Reset()
	m.ConfirmLatency.Reset()
	m.RPCLatency.Reset()
	m.CurrentTPS.Set(0)
	m.TargetTPS.Set(0)
	m.PendingTxs.Set(0)
	m.SetTestStatus("idle")
	m.ErrorsTotal.Reset()
}
