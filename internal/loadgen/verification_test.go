package loadgen

import (
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/storage"
)

func TestStopIncrementalVerification_NilChannel(t *testing.T) {
	lg := newTestLoadGenerator(t)
	lg.incrementalStopCh = nil

	// Should not panic when stop channel is nil
	lg.stopIncrementalVerification()
}

func TestGetIncrementalSnapshots_Empty(t *testing.T) {
	lg := newTestLoadGenerator(t)

	snapshots := lg.getIncrementalSnapshots()
	if len(snapshots) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(snapshots))
	}
}

func TestGetIncrementalSnapshots_ReturnsCopy(t *testing.T) {
	lg := newTestLoadGenerator(t)

	lg.incrementalSnapshots = []storage.IncrementalVerificationSnapshot{
		{FirstBlock: 100, LastBlock: 200, BlocksSampled: 10},
		{FirstBlock: 200, LastBlock: 300, BlocksSampled: 5},
	}

	snapshots := lg.getIncrementalSnapshots()
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}
	if snapshots[0].FirstBlock != 100 {
		t.Errorf("expected FirstBlock=100, got %d", snapshots[0].FirstBlock)
	}

	// Modify returned slice — should not affect internal state
	snapshots[0].FirstBlock = 999
	original := lg.getIncrementalSnapshots()
	if original[0].FirstBlock != 100 {
		t.Error("getIncrementalSnapshots did not return a copy")
	}
}

func TestRunIncrementalVerification_SkipsWhenNoData(t *testing.T) {
	lg := newTestLoadGenerator(t)

	// No recent blocks or hashes — should skip without panic
	lg.runIncrementalVerification()

	snapshots := lg.getIncrementalSnapshots()
	if len(snapshots) != 0 {
		t.Errorf("expected 0 snapshots when no data, got %d", len(snapshots))
	}
}
