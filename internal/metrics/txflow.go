package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TxStage represents a stage in the transaction lifecycle.
type TxStage int

const (
	StageSent TxStage = iota
	StagePending
	StagePreconfirmed
	StageConfirmed
	StageDropped
	StageRequeued
	StageRevoked
	StageFailed
)

// String returns the stage name.
func (s TxStage) String() string {
	switch s {
	case StageSent:
		return "sent"
	case StagePending:
		return "pending"
	case StagePreconfirmed:
		return "preconfirmed"
	case StageConfirmed:
		return "confirmed"
	case StageDropped:
		return "dropped"
	case StageRequeued:
		return "requeued"
	case StageRevoked:
		return "revoked"
	case StageFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// StageTransition records when a TX entered a stage.
type StageTransition struct {
	Stage     TxStage
	Timestamp time.Time
}

// TxFlow tracks the journey of a single transaction through stages.
type TxFlow struct {
	Stages []StageTransition
}

// HasStage returns true if the TX passed through the given stage.
func (f *TxFlow) HasStage(stage TxStage) bool {
	for _, t := range f.Stages {
		if t.Stage == stage {
			return true
		}
	}
	return false
}

// StageCount returns the number of stages the TX passed through.
func (f *TxFlow) StageCount() int {
	return len(f.Stages)
}

// FlowStats contains aggregated flow statistics.
type FlowStats struct {
	// Flow patterns
	DirectConfirmed   int `json:"directConfirmed"`   // sent → confirmed (no preconf)
	PendingConfirmed  int `json:"pendingConfirmed"`  // sent → pending → confirmed
	PreconfConfirmed  int `json:"preconfConfirmed"`  // sent → pending → preconfirmed → confirmed
	DroppedRequeued   int `json:"droppedRequeued"`   // had dropped/requeued in flow
	RevokedFlow       int `json:"revokedFlow"`       // had revoked in flow
	FailedFlow        int `json:"failedFlow"`        // ended in failed

	// Summary
	TotalTracked  int     `json:"totalTracked"`  // Total TXs tracked
	AvgStageCount float64 `json:"avgStageCount"` // Average stages per TX

	// Stage counts (for debugging)
	StageCounts map[string]int `json:"stageCounts,omitempty"` // Count per stage
}

// TxFlowTracker tracks individual transaction journeys through stages.
// Uses a sync.Map for concurrent access and memory efficiency.
type TxFlowTracker struct {
	flows sync.Map // txHash -> *TxFlow

	// Atomic counters for fast access (avoid iteration)
	totalTracked     uint64
	directConfirmed  uint64
	pendingConfirmed uint64
	preconfConfirmed uint64
	droppedRequeued  uint64
	revokedFlow      uint64
	failedFlow       uint64
	totalStages      uint64 // Sum of all stage counts for average calculation
}

// NewTxFlowTracker creates a new flow tracker.
func NewTxFlowTracker() *TxFlowTracker {
	return &TxFlowTracker{}
}

// RecordStage records a stage transition for a transaction.
func (t *TxFlowTracker) RecordStage(txHash common.Hash, stage TxStage, timestamp time.Time) {
	transition := StageTransition{Stage: stage, Timestamp: timestamp}

	// Fast path: try Load first to avoid allocation when entry exists
	val, ok := t.flows.Load(txHash)
	if ok {
		flow := val.(*TxFlow)
		flow.Stages = append(flow.Stages, transition)
	} else {
		// Create new flow with pre-allocated capacity for typical 3-4 stages
		stages := make([]StageTransition, 1, 4)
		stages[0] = transition
		val, ok = t.flows.LoadOrStore(txHash, &TxFlow{Stages: stages})
		if ok {
			// Another goroutine won the race, append to existing
			flow := val.(*TxFlow)
			flow.Stages = append(flow.Stages, transition)
		} else {
			atomic.AddUint64(&t.totalTracked, 1)
		}
	}

	atomic.AddUint64(&t.totalStages, 1)

	// On terminal states: update counters then delete entry to bound memory.
	// Stats use atomic counters so deleting the entry is safe.
	// Terminal states: confirmed, failed, dropped, revoked.
	// (Requeued is NOT terminal - TX will be retried in a later block.)
	switch stage {
	case StageConfirmed:
		t.updateConfirmedCounters(txHash)
		t.flows.Delete(txHash)
	case StageFailed:
		atomic.AddUint64(&t.failedFlow, 1)
		t.flows.Delete(txHash)
	case StageDropped:
		t.flows.Delete(txHash)
	case StageRevoked:
		t.flows.Delete(txHash)
	}
}

// updateConfirmedCounters updates pattern counters when a TX is confirmed.
func (t *TxFlowTracker) updateConfirmedCounters(txHash common.Hash) {
	val, ok := t.flows.Load(txHash)
	if !ok {
		return
	}
	flow := val.(*TxFlow)

	// Determine flow pattern
	hasPending := flow.HasStage(StagePending)
	hasPreconf := flow.HasStage(StagePreconfirmed)
	hasDropped := flow.HasStage(StageDropped)
	hasRequeued := flow.HasStage(StageRequeued)
	hasRevoked := flow.HasStage(StageRevoked)

	if hasDropped || hasRequeued {
		atomic.AddUint64(&t.droppedRequeued, 1)
	}
	if hasRevoked {
		atomic.AddUint64(&t.revokedFlow, 1)
	}

	if hasPreconf {
		atomic.AddUint64(&t.preconfConfirmed, 1)
	} else if hasPending {
		atomic.AddUint64(&t.pendingConfirmed, 1)
	} else {
		atomic.AddUint64(&t.directConfirmed, 1)
	}
}

// GetStats returns aggregated flow statistics.
func (t *TxFlowTracker) GetStats() *FlowStats {
	totalTracked := atomic.LoadUint64(&t.totalTracked)
	totalStages := atomic.LoadUint64(&t.totalStages)

	var avgStageCount float64
	if totalTracked > 0 {
		avgStageCount = float64(totalStages) / float64(totalTracked)
	}

	stats := &FlowStats{
		DirectConfirmed:  int(atomic.LoadUint64(&t.directConfirmed)),
		PendingConfirmed: int(atomic.LoadUint64(&t.pendingConfirmed)),
		PreconfConfirmed: int(atomic.LoadUint64(&t.preconfConfirmed)),
		DroppedRequeued:  int(atomic.LoadUint64(&t.droppedRequeued)),
		RevokedFlow:      int(atomic.LoadUint64(&t.revokedFlow)),
		FailedFlow:       int(atomic.LoadUint64(&t.failedFlow)),
		TotalTracked:     int(totalTracked),
		AvgStageCount:    avgStageCount,
	}

	return stats
}

// GetDetailedStats returns stats with per-stage breakdown (more expensive).
// Note: Only includes in-flight transactions (completed flows are cleaned up).
func (t *TxFlowTracker) GetDetailedStats() *FlowStats {
	stats := t.GetStats()

	// Count stages (requires iteration)
	stageCounts := make(map[string]int)
	t.flows.Range(func(key, value any) bool {
		flow := value.(*TxFlow)
		for _, transition := range flow.Stages {
			stageCounts[transition.Stage.String()]++
		}
		return true
	})
	stats.StageCounts = stageCounts

	return stats
}

// Reset clears all tracked flows.
func (t *TxFlowTracker) Reset() {
	t.flows = sync.Map{}
	atomic.StoreUint64(&t.totalTracked, 0)
	atomic.StoreUint64(&t.directConfirmed, 0)
	atomic.StoreUint64(&t.pendingConfirmed, 0)
	atomic.StoreUint64(&t.preconfConfirmed, 0)
	atomic.StoreUint64(&t.droppedRequeued, 0)
	atomic.StoreUint64(&t.revokedFlow, 0)
	atomic.StoreUint64(&t.failedFlow, 0)
	atomic.StoreUint64(&t.totalStages, 0)
}

// GetFlow returns the flow for a specific transaction.
func (t *TxFlowTracker) GetFlow(txHash common.Hash) (*TxFlow, bool) {
	val, ok := t.flows.Load(txHash)
	if !ok {
		return nil, false
	}
	return val.(*TxFlow), true
}

// Size returns the number of tracked transactions.
func (t *TxFlowTracker) Size() int {
	return int(atomic.LoadUint64(&t.totalTracked))
}
