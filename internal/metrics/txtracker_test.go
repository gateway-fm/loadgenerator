package metrics

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestTxTracker_Basic(t *testing.T) {
	tracker := NewTxTracker()

	hash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	sentTime := time.Now()

	// Set a tx
	tracker.Set(hash, sentTime)

	// Get the tx
	got, ok := tracker.Get(hash)
	if !ok {
		t.Fatal("expected to find tx")
	}
	if !got.Equal(sentTime) {
		t.Errorf("expected time %v, got %v", sentTime, got)
	}

	if tracker.Size() != 1 {
		t.Errorf("expected size 1, got %d", tracker.Size())
	}
}

func TestTxTracker_GetAndDelete(t *testing.T) {
	tracker := NewTxTracker()

	hash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	sentTime := time.Now()

	tracker.Set(hash, sentTime)

	// GetAndDelete should return and remove
	got, ok := tracker.GetAndDelete(hash)
	if !ok {
		t.Fatal("expected to find tx")
	}
	if !got.Equal(sentTime) {
		t.Errorf("expected time %v, got %v", sentTime, got)
	}

	// Second GetAndDelete should fail
	_, ok = tracker.GetAndDelete(hash)
	if ok {
		t.Error("expected tx to be deleted")
	}

	if tracker.Size() != 0 {
		t.Errorf("expected size 0, got %d", tracker.Size())
	}
}

func TestTxTracker_NotFound(t *testing.T) {
	tracker := NewTxTracker()

	hash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	_, ok := tracker.Get(hash)
	if ok {
		t.Error("expected tx not found")
	}

	_, ok = tracker.GetAndDelete(hash)
	if ok {
		t.Error("expected tx not found")
	}
}

func TestTxTracker_Eviction(t *testing.T) {
	// Create a small tracker to test eviction
	tracker := NewTxTrackerWithSize(100, 10)

	// Add 150 txs
	for i := 0; i < 150; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i)))
		tracker.Set(hash, time.Now())
	}

	// Should have evicted some
	size := tracker.Size()
	if size >= 100 {
		t.Errorf("expected size < 100 due to eviction, got %d", size)
	}
}

func TestTxTracker_Concurrent(t *testing.T) {
	tracker := NewTxTracker()

	var wg sync.WaitGroup
	numGoroutines := 10
	txsPerGoroutine := 1000

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < txsPerGoroutine; j++ {
				// Use new big.Int each time to avoid race on shared Big1
				hash := common.BigToHash(big.NewInt(int64(id*txsPerGoroutine + j)))
				tracker.Set(hash, time.Now())
			}
		}(i)
	}

	wg.Wait()

	// All txs should be present (we're under the limit)
	expectedSize := numGoroutines * txsPerGoroutine
	if tracker.Size() != expectedSize {
		t.Errorf("expected size %d, got %d", expectedSize, tracker.Size())
	}
}

func TestTxTracker_ConcurrentReadWrite(t *testing.T) {
	tracker := NewTxTracker()

	var wg sync.WaitGroup
	numWriters := 5
	numReaders := 5
	txsPerWriter := 1000

	// Start writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < txsPerWriter; j++ {
				// Use new big.Int each time to avoid race on shared Big1
				hash := common.BigToHash(big.NewInt(int64(id*txsPerWriter + j)))
				tracker.Set(hash, time.Now())
			}
		}(i)
	}

	// Start readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < txsPerWriter; j++ {
				// Use new big.Int each time to avoid race on shared Big1
				hash := common.BigToHash(big.NewInt(int64(id*txsPerWriter + j)))
				tracker.Get(hash)
			}
		}(i)
	}

	wg.Wait()
}

func TestTxTracker_Reset(t *testing.T) {
	tracker := NewTxTracker()

	for i := 0; i < 100; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i)))
		tracker.Set(hash, time.Now())
	}

	if tracker.Size() != 100 {
		t.Errorf("expected size 100, got %d", tracker.Size())
	}

	tracker.Reset()

	if tracker.Size() != 0 {
		t.Errorf("expected size 0 after reset, got %d", tracker.Size())
	}
}

func BenchmarkTxTracker_Set(b *testing.B) {
	tracker := NewTxTracker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i)))
		tracker.Set(hash, time.Now())
	}
}

func BenchmarkTxTracker_Get(b *testing.B) {
	tracker := NewTxTracker()

	// Pre-populate
	for i := 0; i < 10000; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i)))
		tracker.Set(hash, time.Now())
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i % 10000)))
		tracker.Get(hash)
	}
}

func BenchmarkTxTracker_GetAndDelete(b *testing.B) {
	tracker := NewTxTracker()

	// Pre-populate
	for i := 0; i < b.N; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i)))
		tracker.Set(hash, time.Now())
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash := common.BigToHash(common.Big1.SetInt64(int64(i)))
		tracker.GetAndDelete(hash)
	}
}
