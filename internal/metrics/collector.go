package metrics

import (
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// Snapshot contains a point-in-time view of metrics.
type Snapshot struct {
	TxSent      uint64
	TxConfirmed uint64
	TxFailed    uint64
}

// Collector is the interface for metrics collection.
type Collector interface {
	// Transaction lifecycle
	RecordTxSent(txHash common.Hash, sentTime time.Time)
	RecordTxConfirmed(txHash common.Hash, confirmTime time.Time)
	RecordTxConfirmedFlowOnly(txHash common.Hash, confirmTime time.Time) // Flow tracking only (no counter/latency)
	RecordTxFailed(category string)

	// Preconfirmation lifecycle (Flashblocks-compliant)
	RecordPending(txHash common.Hash, pendingTime time.Time)       // TX queued by sequencer
	RecordPreconfirmed(txHash common.Hash, preconfTime time.Time)  // TX selected for block
	RecordRevoked(txHash common.Hash, revokedTime time.Time)       // Preconf broken
	RecordDropped(txHash common.Hash, droppedTime time.Time)       // TX permanently dropped
	RecordRequeued(txHash common.Hash, requeuedTime time.Time)     // TX requeued

	// Realistic test metrics
	RecordTip(tipWei *big.Int)
	RecordTipWithConfig(tipWei *big.Int, minGwei, maxGwei float64)
	RecordTxType(txType types.TransactionType, tipWei *big.Int, success bool)
	InitRealisticMetrics()

	// Rate tracking
	SetPendingCount(count int64)
	AddPendingCount(delta int64)
	SubPendingCount(delta int64)
	SetPeakRate(rate int)

	// Counters
	IncTxSent() uint64
	IncTxFailed() uint64
	GetTxSent() uint64
	GetTxConfirmed() uint64
	GetTxFailed() uint64
	GetPendingCount() int64

	// Preconfirmation counters
	GetTxPending() uint64
	GetTxPreconfirmed() uint64
	GetTxRevoked() uint64
	GetTxDropped() uint64
	GetTxRequeued() uint64

	// Statistics
	GetSnapshot() Snapshot
	GetTxSentTime(txHash common.Hash) (time.Time, bool)
	GetLatencyStats() *types.LatencyStats
	GetPreconfLatencyStats() *types.LatencyStats
	GetPendingLatencyStats() *types.LatencyStats
	GetTipHistogram(cfg *types.RealisticTestConfig) []types.TipHistogramBucket
	GetTxTypeMetrics() []types.TxTypeMetrics
	GetFlowStats() *FlowStats

	// Lifecycle
	Reset()
}

// MemoryCollector is an in-memory implementation of Collector.
type MemoryCollector struct {
	// Transaction tracking
	txTracker       *TxTracker
	flowTracker     *TxFlowTracker // TX stage flow tracking
	latencyStats    *StreamingLatencyStats
	preconfLatency  *StreamingLatencyStats
	pendingLatency  *StreamingLatencyStats

	// Counters
	txSent      uint64 // atomic
	txConfirmed uint64 // atomic
	txFailed    uint64 // atomic

	// Preconfirmation stage counters (Flashblocks-compliant)
	txPending      uint64 // atomic - TX queued by sequencer
	txPreconfirmed uint64 // atomic - TX selected for block (commitment)
	txRevoked      uint64 // atomic - Preconfirmation broken
	txDropped      uint64 // atomic - TX permanently dropped
	txRequeued     uint64 // atomic - TX requeued for later

	// Rate tracking
	pendingCount int64 // atomic
	peakRate     int64 // atomic

	// Realistic test tracking
	tipHistogram     []int64 // atomic counts per bucket
	tipHistogramMu   sync.Mutex
	txTypeTrackers   map[types.TransactionType]*txTypeTracker
	txTypeTrackersMu sync.RWMutex
}

// txTypeTracker tracks per-type statistics.
type txTypeTracker struct {
	sent   uint64 // atomic
	failed uint64 // atomic
	tipSum uint64 // atomic - sum of tips in wei for averaging
}

// NewMemoryCollector creates a new in-memory metrics collector.
func NewMemoryCollector() *MemoryCollector {
	return &MemoryCollector{
		txTracker:      NewTxTracker(),
		flowTracker:    NewTxFlowTracker(),
		latencyStats:   NewStreamingLatencyStats(),
		preconfLatency: NewStreamingLatencyStats(),
		pendingLatency: NewStreamingLatencyStats(),
		tipHistogram:   make([]int64, 20), // 20 buckets
		txTypeTrackers: make(map[types.TransactionType]*txTypeTracker),
	}
}

// NewInMemoryCollector creates a collector, optionally with Prometheus metrics.
// The bool parameter indicates whether to enable Prometheus registration (currently unused).
func NewInMemoryCollector(enablePrometheus bool) *MemoryCollector {
	// Prometheus metrics are registered separately via the PrometheusMetrics struct
	// and the /metrics endpoint. The MemoryCollector tracks in-memory stats.
	return NewMemoryCollector()
}

// RecordTxSent records when a transaction was sent.
func (c *MemoryCollector) RecordTxSent(txHash common.Hash, sentTime time.Time) {
	c.txTracker.Set(txHash, sentTime)
	c.flowTracker.RecordStage(txHash, StageSent, sentTime)
}

// RecordTxConfirmed records when a transaction was confirmed.
func (c *MemoryCollector) RecordTxConfirmed(txHash common.Hash, confirmTime time.Time) {
	sentTime, ok := c.txTracker.GetAndDelete(txHash)
	if !ok {
		// Still record the stage transition even if we don't have sent time
		c.flowTracker.RecordStage(txHash, StageConfirmed, confirmTime)
		return
	}

	latencyMs := float64(confirmTime.Sub(sentTime).Milliseconds())
	c.latencyStats.Add(latencyMs)
	atomic.AddUint64(&c.txConfirmed, 1)
	c.flowTracker.RecordStage(txHash, StageConfirmed, confirmTime)
}

// RecordTxConfirmedFlowOnly records the confirmed flow stage without incrementing
// the confirmed counter or recording latency. Used for TXs confirmed in blocks
// beyond the test end boundary (grace period arrivals) to keep the flow tracker
// accurate and prevent memory leaks from unfinished flow entries.
func (c *MemoryCollector) RecordTxConfirmedFlowOnly(txHash common.Hash, confirmTime time.Time) {
	c.txTracker.GetAndDelete(txHash) // Clean up sent tracker
	c.flowTracker.RecordStage(txHash, StageConfirmed, confirmTime)
}

// RecordTxFailed records a failed transaction.
func (c *MemoryCollector) RecordTxFailed(category string) {
	atomic.AddUint64(&c.txFailed, 1)
	// Note: We don't have txHash here, so we can't track flow for failed TXs
	// TODO: Track by category for error breakdown
}

// RecordPending records when a transaction was acknowledged as pending by the sequencer.
func (c *MemoryCollector) RecordPending(txHash common.Hash, pendingTime time.Time) {
	atomic.AddUint64(&c.txPending, 1)
	c.flowTracker.RecordStage(txHash, StagePending, pendingTime)

	sentTime, ok := c.txTracker.Get(txHash)
	if !ok {
		return
	}

	latencyMs := float64(pendingTime.Sub(sentTime).Milliseconds())
	c.pendingLatency.Add(latencyMs)
}

// RecordPreconfirmed records when a transaction was preconfirmed (selected for block).
func (c *MemoryCollector) RecordPreconfirmed(txHash common.Hash, preconfTime time.Time) {
	atomic.AddUint64(&c.txPreconfirmed, 1)
	c.flowTracker.RecordStage(txHash, StagePreconfirmed, preconfTime)

	sentTime, ok := c.txTracker.Get(txHash)
	if !ok {
		return
	}

	latencyMs := float64(preconfTime.Sub(sentTime).Milliseconds())
	c.preconfLatency.Add(latencyMs)
}

// RecordRevoked records when a preconfirmation was revoked (execution layer rejected TX).
func (c *MemoryCollector) RecordRevoked(txHash common.Hash, revokedTime time.Time) {
	atomic.AddUint64(&c.txRevoked, 1)
	c.flowTracker.RecordStage(txHash, StageRevoked, revokedTime)
}

// RecordDropped records when a transaction was permanently dropped.
func (c *MemoryCollector) RecordDropped(txHash common.Hash, droppedTime time.Time) {
	atomic.AddUint64(&c.txDropped, 1)
	c.flowTracker.RecordStage(txHash, StageDropped, droppedTime)
}

// RecordRequeued records when a transaction was requeued for a later block.
func (c *MemoryCollector) RecordRequeued(txHash common.Hash, requeuedTime time.Time) {
	atomic.AddUint64(&c.txRequeued, 1)
	c.flowTracker.RecordStage(txHash, StageRequeued, requeuedTime)
}

// RecordTip records a tip value for histogram tracking.
func (c *MemoryCollector) RecordTip(tipWei *big.Int) {
	// This will be configured per-test with proper bucket bounds
	// For now, just store in first bucket as placeholder
	if len(c.tipHistogram) > 0 {
		atomic.AddInt64(&c.tipHistogram[0], 1)
	}
}

// RecordTipWithConfig records a tip value with proper bucketing.
func (c *MemoryCollector) RecordTipWithConfig(tipWei *big.Int, minGwei, maxGwei float64) {
	if len(c.tipHistogram) == 0 {
		return
	}

	// Use Uint64() to avoid overflow for large tips > 2^63-1 wei
	// Tips > 18 ETH (max uint64) would overflow, but that's unrealistic
	tipGwei := float64(tipWei.Uint64()) / 1e9
	numBuckets := len(c.tipHistogram)
	bucketSize := (maxGwei - minGwei) / float64(numBuckets)
	if bucketSize <= 0 {
		bucketSize = 1
	}

	bucketIdx := int((tipGwei - minGwei) / bucketSize)
	if bucketIdx < 0 {
		bucketIdx = 0
	}
	if bucketIdx >= numBuckets {
		bucketIdx = numBuckets - 1
	}

	atomic.AddInt64(&c.tipHistogram[bucketIdx], 1)
}

// RecordTxType records a transaction for per-type tracking.
func (c *MemoryCollector) RecordTxType(txType types.TransactionType, tipWei *big.Int, success bool) {
	c.txTypeTrackersMu.RLock()
	tracker, ok := c.txTypeTrackers[txType]
	c.txTypeTrackersMu.RUnlock()

	if !ok {
		c.txTypeTrackersMu.Lock()
		tracker, ok = c.txTypeTrackers[txType]
		if !ok {
			tracker = &txTypeTracker{}
			c.txTypeTrackers[txType] = tracker
		}
		c.txTypeTrackersMu.Unlock()
	}

	if success {
		atomic.AddUint64(&tracker.sent, 1)
		if tipWei != nil {
			atomic.AddUint64(&tracker.tipSum, uint64(tipWei.Int64()))
		}
	} else {
		atomic.AddUint64(&tracker.failed, 1)
	}
}

// SetPendingCount sets the pending transaction count.
func (c *MemoryCollector) SetPendingCount(count int64) {
	atomic.StoreInt64(&c.pendingCount, count)
}

// AddPendingCount adds to the pending count.
func (c *MemoryCollector) AddPendingCount(delta int64) {
	atomic.AddInt64(&c.pendingCount, delta)
}

// SubPendingCount subtracts from pending count, saturating at 0.
func (c *MemoryCollector) SubPendingCount(delta int64) {
	AtomicSubSaturating(&c.pendingCount, delta)
}

// SetPeakRate sets the peak rate if higher than current.
func (c *MemoryCollector) SetPeakRate(rate int) {
	AtomicMax(&c.peakRate, int64(rate))
}

// IncTxSent increments sent counter.
func (c *MemoryCollector) IncTxSent() uint64 {
	return atomic.AddUint64(&c.txSent, 1)
}

// IncTxFailed increments failed counter.
func (c *MemoryCollector) IncTxFailed() uint64 {
	return atomic.AddUint64(&c.txFailed, 1)
}

// GetTxSent returns total sent.
func (c *MemoryCollector) GetTxSent() uint64 {
	return atomic.LoadUint64(&c.txSent)
}

// GetTxConfirmed returns total confirmed.
func (c *MemoryCollector) GetTxConfirmed() uint64 {
	return atomic.LoadUint64(&c.txConfirmed)
}

// GetTxFailed returns total failed.
func (c *MemoryCollector) GetTxFailed() uint64 {
	return atomic.LoadUint64(&c.txFailed)
}

// GetPendingCount returns pending count.
func (c *MemoryCollector) GetPendingCount() int64 {
	return atomic.LoadInt64(&c.pendingCount)
}

// GetTxPending returns total pending acknowledgments received.
func (c *MemoryCollector) GetTxPending() uint64 {
	return atomic.LoadUint64(&c.txPending)
}

// GetTxPreconfirmed returns total preconfirmations received.
func (c *MemoryCollector) GetTxPreconfirmed() uint64 {
	return atomic.LoadUint64(&c.txPreconfirmed)
}

// GetTxRevoked returns total revocations received.
func (c *MemoryCollector) GetTxRevoked() uint64 {
	return atomic.LoadUint64(&c.txRevoked)
}

// GetTxDropped returns total dropped acknowledgments received.
func (c *MemoryCollector) GetTxDropped() uint64 {
	return atomic.LoadUint64(&c.txDropped)
}

// GetTxRequeued returns total requeue acknowledgments received.
func (c *MemoryCollector) GetTxRequeued() uint64 {
	return atomic.LoadUint64(&c.txRequeued)
}

// GetSnapshot returns a point-in-time snapshot of metrics.
func (c *MemoryCollector) GetSnapshot() Snapshot {
	return Snapshot{
		TxSent:      atomic.LoadUint64(&c.txSent),
		TxConfirmed: atomic.LoadUint64(&c.txConfirmed),
		TxFailed:    atomic.LoadUint64(&c.txFailed),
	}
}

// GetTxSentTime returns the time a transaction was sent.
func (c *MemoryCollector) GetTxSentTime(txHash common.Hash) (time.Time, bool) {
	return c.txTracker.Get(txHash)
}

// GetLatencyStats returns confirmation latency statistics.
func (c *MemoryCollector) GetLatencyStats() *types.LatencyStats {
	return c.latencyStats.GetStats()
}

// GetPreconfLatencyStats returns preconfirmation latency statistics.
func (c *MemoryCollector) GetPreconfLatencyStats() *types.LatencyStats {
	return c.preconfLatency.GetStats()
}

// GetPendingLatencyStats returns pending acknowledgment latency statistics.
func (c *MemoryCollector) GetPendingLatencyStats() *types.LatencyStats {
	return c.pendingLatency.GetStats()
}

// GetTipHistogram returns the tip histogram buckets.
func (c *MemoryCollector) GetTipHistogram(cfg *types.RealisticTestConfig) []types.TipHistogramBucket {
	if cfg == nil || c.tipHistogram == nil {
		return nil
	}

	numBuckets := len(c.tipHistogram)
	bucketSize := (cfg.MaxTipGwei - cfg.MinTipGwei) / float64(numBuckets)
	if bucketSize <= 0 {
		bucketSize = 1
	}

	buckets := make([]types.TipHistogramBucket, numBuckets)
	for i := 0; i < numBuckets; i++ {
		buckets[i] = types.TipHistogramBucket{
			MinGwei: cfg.MinTipGwei + float64(i)*bucketSize,
			MaxGwei: cfg.MinTipGwei + float64(i+1)*bucketSize,
			Count:   int(atomic.LoadInt64(&c.tipHistogram[i])),
		}
	}
	return buckets
}

// GetTxTypeMetrics returns per-type metrics.
func (c *MemoryCollector) GetTxTypeMetrics() []types.TxTypeMetrics {
	c.txTypeTrackersMu.RLock()
	defer c.txTypeTrackersMu.RUnlock()

	if len(c.txTypeTrackers) == 0 {
		return nil
	}

	var metrics []types.TxTypeMetrics
	for txType, tracker := range c.txTypeTrackers {
		sent := atomic.LoadUint64(&tracker.sent)
		tipSum := atomic.LoadUint64(&tracker.tipSum)
		failed := atomic.LoadUint64(&tracker.failed)

		var avgTipGwei float64
		if sent > 0 {
			avgTipGwei = float64(tipSum) / float64(sent) / 1e9
		}

		if sent > 0 || failed > 0 {
			metrics = append(metrics, types.TxTypeMetrics{
				Type:       txType,
				Sent:       sent,
				Confirmed:  sent, // Approximate
				Failed:     failed,
				AvgTipGwei: avgTipGwei,
			})
		}
	}
	return metrics
}

// GetFlowStats returns TX flow statistics.
func (c *MemoryCollector) GetFlowStats() *FlowStats {
	return c.flowTracker.GetStats()
}

// Reset clears all metrics.
func (c *MemoryCollector) Reset() {
	c.txTracker.Reset()
	c.flowTracker.Reset()
	c.latencyStats.Reset()
	c.preconfLatency.Reset()
	c.pendingLatency.Reset()

	atomic.StoreUint64(&c.txSent, 0)
	atomic.StoreUint64(&c.txConfirmed, 0)
	atomic.StoreUint64(&c.txFailed, 0)
	atomic.StoreInt64(&c.pendingCount, 0)
	atomic.StoreInt64(&c.peakRate, 0)

	// Reset preconfirmation stage counters
	atomic.StoreUint64(&c.txPending, 0)
	atomic.StoreUint64(&c.txPreconfirmed, 0)
	atomic.StoreUint64(&c.txRevoked, 0)
	atomic.StoreUint64(&c.txDropped, 0)
	atomic.StoreUint64(&c.txRequeued, 0)

	c.tipHistogramMu.Lock()
	for i := range c.tipHistogram {
		atomic.StoreInt64(&c.tipHistogram[i], 0)
	}
	c.tipHistogramMu.Unlock()

	c.txTypeTrackersMu.Lock()
	c.txTypeTrackers = make(map[types.TransactionType]*txTypeTracker)
	c.txTypeTrackersMu.Unlock()
}

// InitRealisticMetrics initializes tracking for realistic test.
func (c *MemoryCollector) InitRealisticMetrics() {
	c.tipHistogramMu.Lock()
	c.tipHistogram = make([]int64, 20)
	c.tipHistogramMu.Unlock()

	c.txTypeTrackersMu.Lock()
	c.txTypeTrackers = make(map[types.TransactionType]*txTypeTracker)
	for _, txType := range []types.TransactionType{
		types.TxTypeEthTransfer, types.TxTypeERC20Transfer, types.TxTypeERC20Approve,
		types.TxTypeUniswapSwap, types.TxTypeStorageWrite, types.TxTypeHeavyCompute,
	} {
		c.txTypeTrackers[txType] = &txTypeTracker{}
	}
	c.txTypeTrackersMu.Unlock()
}
