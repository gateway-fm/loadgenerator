package loadgen

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestProcessPreconfEvent_Pending(t *testing.T) {
	lg := newTestLoadGenerator(t)
	col := lg.metricsCol.(*metrics.MemoryCollector)

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash: "0xaaa1",
		Status: types.PreconfStagePending,
	})

	if col.GetTxPending() != 1 {
		t.Errorf("expected 1 pending, got %d", col.GetTxPending())
	}
}

func TestProcessPreconfEvent_Preconfirmed_RecordsLatency(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	txHash := common.HexToHash("0xbbb1")
	sentTime := time.Now().Add(-50 * time.Millisecond)
	col.RecordTxSent(txHash, sentTime)

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash: "0xbbb1",
		Status: types.PreconfStagePreconfirmed,
	})

	if col.GetTxPreconfirmed() != 1 {
		t.Errorf("expected 1 preconfirmed, got %d", col.GetTxPreconfirmed())
	}

	stats := lg.preconfLatencies.GetStats()
	if stats == nil || stats.Count == 0 {
		t.Error("expected preconf latency to be recorded in streaming stats")
	}
}

func TestProcessPreconfEvent_Preconfirmed_NoSentTime(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash: "0xccc1",
		Status: types.PreconfStagePreconfirmed,
	})

	if col.GetTxPreconfirmed() != 1 {
		t.Errorf("expected 1 preconfirmed, got %d", col.GetTxPreconfirmed())
	}
	stats := lg.preconfLatencies.GetStats()
	if stats != nil {
		t.Errorf("expected nil latency stats without sent time, got count=%d", stats.Count)
	}
}

func TestProcessPreconfEvent_Confirmed(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))
	atomic.StoreInt64(&lg.pendingCount, 5)

	txHash := common.HexToHash("0xddd1")
	col.RecordTxSent(txHash, time.Now().Add(-100*time.Millisecond))

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash:      "0xddd1",
		Status:      types.PreconfStageConfirmed,
		BlockNumber: 10,
	})

	if col.GetTxConfirmed() != 1 {
		t.Errorf("expected 1 confirmed, got %d", col.GetTxConfirmed())
	}
	if atomic.LoadInt64(&lg.pendingCount) != 4 {
		t.Errorf("expected pendingCount 4, got %d", atomic.LoadInt64(&lg.pendingCount))
	}

	lg.recentConfirmedMu.Lock()
	if len(lg.recentConfirmedHashes) != 1 || lg.recentConfirmedHashes[0] != "0xddd1" {
		t.Errorf("expected recentConfirmedHashes to contain tx hash")
	}
	lg.recentConfirmedMu.Unlock()

	lg.recentBlockNumbersMu.Lock()
	if len(lg.recentBlockNumbers) != 1 || lg.recentBlockNumbers[0] != 10 {
		t.Errorf("expected recentBlockNumbers to contain block 10")
	}
	lg.recentBlockNumbersMu.Unlock()
}

func TestProcessPreconfEvent_Confirmed_BeyondTestEnd(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	lg.blockMetricsMu.Lock()
	lg.testEndBlockNumber = 5
	lg.blockMetricsMu.Unlock()

	txHash := common.HexToHash("0xeee1")
	col.RecordTxSent(txHash, time.Now().Add(-100*time.Millisecond))

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash:      "0xeee1",
		Status:      types.PreconfStageConfirmed,
		BlockNumber: 10,
	})

	if col.GetTxConfirmed() != 0 {
		t.Errorf("expected 0 confirmed (beyond test end), got %d", col.GetTxConfirmed())
	}

	lg.recentConfirmedMu.Lock()
	if len(lg.recentConfirmedHashes) != 0 {
		t.Errorf("expected no recentConfirmedHashes for beyond-test-end block")
	}
	lg.recentConfirmedMu.Unlock()
}

func TestProcessPreconfEvent_Revoked(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash: "0xfff1",
		Status: types.PreconfStageRevoked,
	})

	if col.GetTxRevoked() != 1 {
		t.Errorf("expected 1 revoked, got %d", col.GetTxRevoked())
	}
	if atomic.LoadInt64(&lg.recentRevocations) != 1 {
		t.Errorf("expected 1 recentRevocations, got %d", atomic.LoadInt64(&lg.recentRevocations))
	}
}

func TestProcessPreconfEvent_Dropped(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))
	atomic.StoreInt64(&lg.pendingCount, 3)

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash: "0x1111",
		Status: types.PreconfStageDropped,
	})

	if col.GetTxDropped() != 1 {
		t.Errorf("expected 1 dropped, got %d", col.GetTxDropped())
	}
	if atomic.LoadInt64(&lg.pendingCount) != 2 {
		t.Errorf("expected pendingCount 2, got %d", atomic.LoadInt64(&lg.pendingCount))
	}
}

func TestProcessPreconfEvent_Requeued(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	lg.processPreconfEvent(&types.PreconfEvent{
		TxHash: "0x2222",
		Status: types.PreconfStageRequeued,
	})

	if col.GetTxRequeued() != 1 {
		t.Errorf("expected 1 requeued, got %d", col.GetTxRequeued())
	}
}

func TestProcessPreconfEvent_NilAndEmpty(t *testing.T) {
	lg := newTestLoadGenerator(t)
	col := lg.metricsCol.(*metrics.MemoryCollector)

	lg.processPreconfEvent(nil)
	lg.processPreconfEvent(&types.PreconfEvent{TxHash: "", Status: types.PreconfStagePending})

	if col.GetTxPending() != 0 {
		t.Errorf("expected 0 pending for nil/empty events, got %d", col.GetTxPending())
	}
}

func TestPreconfSequenceGapDetection(t *testing.T) {
	lg := newTestLoadGenerator(t)

	atomic.StoreUint64(&lg.lastPreconfSeqNum, 0)

	// First message seq=1 - no gap
	msg1 := types.PreconfMessage{SeqNum: 1, TxHash: "0xa1", Status: types.PreconfStagePending}
	if msg1.SeqNum > 0 {
		expected := atomic.LoadUint64(&lg.lastPreconfSeqNum) + 1
		if expected > 1 && msg1.SeqNum > expected {
			t.Error("unexpected gap on first message")
		}
		atomic.StoreUint64(&lg.lastPreconfSeqNum, msg1.SeqNum)
	}

	// Second message seq=2 - no gap
	atomic.StoreUint64(&lg.lastPreconfSeqNum, 2)

	// Third message seq=5 - gap of 2 (missing 3,4)
	msg3SeqNum := uint64(5)
	expected := atomic.LoadUint64(&lg.lastPreconfSeqNum) + 1
	if expected > 1 && msg3SeqNum > expected {
		gapSize := msg3SeqNum - expected
		atomic.AddUint64(&lg.preconfGaps, gapSize)
	}
	atomic.StoreUint64(&lg.lastPreconfSeqNum, msg3SeqNum)

	if atomic.LoadUint64(&lg.preconfGaps) != 2 {
		t.Errorf("expected 2 gaps, got %d", atomic.LoadUint64(&lg.preconfGaps))
	}
	if atomic.LoadUint64(&lg.lastPreconfSeqNum) != 5 {
		t.Errorf("expected lastPreconfSeqNum=5, got %d", atomic.LoadUint64(&lg.lastPreconfSeqNum))
	}
}

func TestProcessBuilderBlockMetrics_BasicTracking(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	// Set lastBlockTime so blockMetrics gets appended
	lg.blockMetricsMu.Lock()
	lg.lastBlockTime = time.Now().Add(-1 * time.Second)
	lg.blockMetricsMu.Unlock()

	event := &types.BuilderBlockMetrics{
		BlockNumber:          100,
		GasUsed:              21000000,
		GasLimit:             30000000,
		TxCount:              150,
		FilterDurationMs:     5,
		EngineApiDurationMs:  10,
		TotalBuildDurationMs: 20,
	}
	lg.processBuilderBlockMetrics(event)

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if lg.cumulativeGasUsed != 21000000 {
		t.Errorf("expected cumulativeGasUsed 21000000, got %d", lg.cumulativeGasUsed)
	}
	if lg.cumulativeGasLimit != 30000000 {
		t.Errorf("expected cumulativeGasLimit 30000000, got %d", lg.cumulativeGasLimit)
	}
	if lg.totalBlockCount != 1 {
		t.Errorf("expected totalBlockCount 1, got %d", lg.totalBlockCount)
	}
	if lg.firstBlockNumber != 100 {
		t.Errorf("expected firstBlockNumber 100, got %d", lg.firstBlockNumber)
	}
	if lg.lastRecordedBlock != 100 {
		t.Errorf("expected lastRecordedBlock 100, got %d", lg.lastRecordedBlock)
	}
	if len(lg.blockMetrics) != 1 {
		t.Fatalf("expected 1 blockMetrics point, got %d", len(lg.blockMetrics))
	}
	bm := lg.blockMetrics[0]
	if bm.gasUsed != 21000000 || bm.txCount != 150 || bm.filterDurationMs != 5 {
		t.Errorf("unexpected blockMetrics values: gasUsed=%d txCount=%d filterMs=%d", bm.gasUsed, bm.txCount, bm.filterDurationMs)
	}
	if len(lg.rollingGasWindow) != 1 || lg.rollingGasWindow[0].gasUsed != 21000000 {
		t.Errorf("expected rolling gas window entry")
	}
	if len(lg.rollingTxWindow) != 1 || lg.rollingTxWindow[0].txCount != 150 {
		t.Errorf("expected rolling tx window entry")
	}
}

func TestProcessBuilderBlockMetrics_SkipsWhenNotRunning(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.processBuilderBlockMetrics(&types.BuilderBlockMetrics{
		BlockNumber: 100,
		GasUsed:     21000000,
	})

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.cumulativeGasUsed != 0 {
		t.Errorf("expected no gas accumulation when not running, got %d", lg.cumulativeGasUsed)
	}
}

func TestProcessBuilderBlockMetrics_SkipsDuplicate(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	lg.blockMetricsMu.Lock()
	lg.lastBlockTime = time.Now().Add(-1 * time.Second)
	lg.lastRecordedBlock = 100
	lg.blockMetricsMu.Unlock()

	lg.processBuilderBlockMetrics(&types.BuilderBlockMetrics{
		BlockNumber: 100,
		GasUsed:     21000000,
	})

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.cumulativeGasUsed != 0 {
		t.Errorf("expected no gas for duplicate block, got %d", lg.cumulativeGasUsed)
	}
}

func TestProcessBuilderBlockMetrics_SkipsBeyondTestEnd(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	lg.blockMetricsMu.Lock()
	lg.testEndBlockNumber = 50
	lg.lastBlockTime = time.Now().Add(-1 * time.Second)
	lg.blockMetricsMu.Unlock()

	lg.processBuilderBlockMetrics(&types.BuilderBlockMetrics{
		BlockNumber: 60,
		GasUsed:     21000000,
	})

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.cumulativeGasUsed != 0 {
		t.Errorf("expected no gas for block beyond test end, got %d", lg.cumulativeGasUsed)
	}
}

func TestProcessBuilderBlockMetrics_Nil(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.processBuilderBlockMetrics(nil)
}

func TestProcessBuilderBlockMetrics_MultipleBlocks(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	lg.blockMetricsMu.Lock()
	lg.lastBlockTime = time.Now().Add(-2 * time.Second)
	lg.blockMetricsMu.Unlock()

	lg.processBuilderBlockMetrics(&types.BuilderBlockMetrics{
		BlockNumber: 10, GasUsed: 1000, GasLimit: 5000, TxCount: 5,
	})
	lg.processBuilderBlockMetrics(&types.BuilderBlockMetrics{
		BlockNumber: 11, GasUsed: 2000, GasLimit: 5000, TxCount: 10,
	})

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if lg.cumulativeGasUsed != 3000 {
		t.Errorf("expected cumulativeGasUsed 3000, got %d", lg.cumulativeGasUsed)
	}
	if lg.totalBlockCount != 2 {
		t.Errorf("expected 2 blocks, got %d", lg.totalBlockCount)
	}
	if lg.firstBlockNumber != 10 {
		t.Errorf("expected firstBlockNumber 10, got %d", lg.firstBlockNumber)
	}
}

func TestProcessNewHead_BasicTracking(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.processNewHead("0xa", "0x5208", "0x1c9c380")

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if lg.firstBlockNumber != 10 {
		t.Errorf("expected firstBlockNumber 10, got %d", lg.firstBlockNumber)
	}
	if lg.lastBlockNumber != 10 {
		t.Errorf("expected lastBlockNumber 10, got %d", lg.lastBlockNumber)
	}
	if lg.lastRecordedBlock != 10 {
		t.Errorf("expected lastRecordedBlock 10, got %d", lg.lastRecordedBlock)
	}
	if len(lg.blockMetrics) != 1 {
		t.Errorf("expected 1 block metric, got %d", len(lg.blockMetrics))
	}
	if lg.blockMetrics[0].gasUsed != 21000 {
		t.Errorf("expected gasUsed 21000, got %d", lg.blockMetrics[0].gasUsed)
	}
	if lg.blockMetrics[0].gasLimit != 30000000 {
		t.Errorf("expected gasLimit 30000000, got %d", lg.blockMetrics[0].gasLimit)
	}
}

func TestProcessNewHead_SkipsDuplicate(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.blockMetricsMu.Lock()
	lg.lastRecordedBlock = 10
	lg.blockMetricsMu.Unlock()

	lg.processNewHead("0xa", "0x5208", "0x1c9c380")

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if len(lg.blockMetrics) != 0 {
		t.Errorf("expected no block metrics for duplicate, got %d", len(lg.blockMetrics))
	}
}

func TestProcessNewHead_BlockTimeCalculation(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.blockMetricsMu.Lock()
	lg.lastBlockTime = time.Now().Add(-500 * time.Millisecond)
	lg.lastBlockNumber = 9
	lg.blockMetricsMu.Unlock()

	lg.processNewHead("0xa", "0x5208", "0x1c9c380")

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()

	if len(lg.blockMetrics) != 1 {
		t.Fatalf("expected 1 block metric, got %d", len(lg.blockMetrics))
	}
	bt := lg.blockMetrics[0].blockTime
	if bt < 0.4 || bt > 1.0 {
		t.Errorf("expected blockTime ~0.5s, got %f", bt)
	}
}

func TestProcessNewHead_TracksRecentBlocks(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.processNewHead("0x64", "0x0", "0x0")

	lg.recentBlockNumbersMu.Lock()
	defer lg.recentBlockNumbersMu.Unlock()
	if len(lg.recentBlockNumbers) != 1 || lg.recentBlockNumbers[0] != 100 {
		t.Errorf("expected recentBlockNumbers=[100], got %v", lg.recentBlockNumbers)
	}
}

func TestProcessPreconfEvent_AllStatusTypes(t *testing.T) {
	statuses := []struct {
		status  string
		checkFn func(*metrics.MemoryCollector) uint64
	}{
		{types.PreconfStagePending, (*metrics.MemoryCollector).GetTxPending},
		{types.PreconfStagePreconfirmed, (*metrics.MemoryCollector).GetTxPreconfirmed},
		{types.PreconfStageRevoked, (*metrics.MemoryCollector).GetTxRevoked},
		{types.PreconfStageDropped, (*metrics.MemoryCollector).GetTxDropped},
		{types.PreconfStageRequeued, (*metrics.MemoryCollector).GetTxRequeued},
	}

	for _, s := range statuses {
		t.Run(s.status, func(t *testing.T) {
			col := metrics.NewInMemoryCollector(true)
			lg := newTestLoadGenerator(t, WithMetricsCollector(col))

			lg.processPreconfEvent(&types.PreconfEvent{
				TxHash:      "0xabc123",
				Status:      s.status,
				BlockNumber: 42,
			})

			if s.checkFn(col) != 1 {
				t.Errorf("expected 1 for status %s, got %d", s.status, s.checkFn(col))
			}
		})
	}
}

func TestProcessPreconfEvent_Confirmed_TracksBlockNumbers(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	for i := uint64(10); i <= 12; i++ {
		lg.processPreconfEvent(&types.PreconfEvent{
			TxHash:      fmt.Sprintf("0xblock%d", i),
			Status:      types.PreconfStageConfirmed,
			BlockNumber: i,
		})
	}

	lg.recentBlockNumbersMu.Lock()
	defer lg.recentBlockNumbersMu.Unlock()

	if len(lg.recentBlockNumbers) != 3 {
		t.Errorf("expected 3 block numbers, got %d", len(lg.recentBlockNumbers))
	}
}

func TestProcessPreconfEvent_Confirmed_DeduplicatesBlockNumbers(t *testing.T) {
	col := metrics.NewInMemoryCollector(true)
	lg := newTestLoadGenerator(t, WithMetricsCollector(col))

	for i := 0; i < 3; i++ {
		lg.processPreconfEvent(&types.PreconfEvent{
			TxHash:      fmt.Sprintf("0xdedup%d", i),
			Status:      types.PreconfStageConfirmed,
			BlockNumber: 42,
		})
	}

	lg.recentBlockNumbersMu.Lock()
	defer lg.recentBlockNumbersMu.Unlock()

	if len(lg.recentBlockNumbers) != 1 {
		t.Errorf("expected 1 deduplicated block number, got %d: %v", len(lg.recentBlockNumbers), lg.recentBlockNumbers)
	}
}

func TestProcessBuilderBlockMetrics_UpdatesRPCFallback(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.statusMu.Lock()
	lg.status = types.StatusRunning
	lg.statusMu.Unlock()

	lg.blockMetricsMu.Lock()
	lg.lastBlockTime = time.Now().Add(-1 * time.Second)
	lg.rpcLastBlockNumber = 50
	lg.blockMetricsMu.Unlock()

	lg.processBuilderBlockMetrics(&types.BuilderBlockMetrics{
		BlockNumber: 100, GasUsed: 1000, GasLimit: 5000,
	})

	lg.blockMetricsMu.Lock()
	defer lg.blockMetricsMu.Unlock()
	if lg.rpcLastBlockNumber != 100 {
		t.Errorf("expected rpcLastBlockNumber updated to 100, got %d", lg.rpcLastBlockNumber)
	}
}
