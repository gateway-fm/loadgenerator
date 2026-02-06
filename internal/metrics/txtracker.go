package metrics

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TxTracker tracks transaction send times with bounded memory.
// Uses a ring buffer with LRU eviction to prevent unbounded growth.
type TxTracker struct {
	mu sync.RWMutex

	// Map from tx hash to send time
	times map[common.Hash]time.Time

	// Ring buffer tracking insertion order for LRU eviction
	insertOrder []common.Hash
	head        int // Next position to write

	// Configuration
	maxSize      int
	evictBatch   int
}

const (
	// DefaultMaxTrackedTxs is the maximum number of transactions to track.
	DefaultMaxTrackedTxs = 100000

	// DefaultEvictBatch is how many to evict when limit is reached.
	DefaultEvictBatch = 10000
)

// NewTxTracker creates a new bounded transaction tracker.
func NewTxTracker() *TxTracker {
	return NewTxTrackerWithSize(DefaultMaxTrackedTxs, DefaultEvictBatch)
}

// NewTxTrackerWithSize creates a tracker with custom size limits.
func NewTxTrackerWithSize(maxSize, evictBatch int) *TxTracker {
	return &TxTracker{
		times:       make(map[common.Hash]time.Time, maxSize),
		insertOrder: make([]common.Hash, maxSize),
		maxSize:     maxSize,
		evictBatch:  evictBatch,
	}
}

// Set records when a transaction was sent.
// If the tracker is at capacity, older entries are evicted.
func (t *TxTracker) Set(hash common.Hash, sentTime time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if we need to evict
	if len(t.times) >= t.maxSize {
		t.evictOldest()
	}

	// Store the time
	t.times[hash] = sentTime

	// Track in ring buffer for LRU
	// First, remove old entry at this position from map if it exists
	if oldHash := t.insertOrder[t.head]; oldHash != (common.Hash{}) {
		// Only delete if it wasn't already removed by GetAndDelete
		if _, exists := t.times[oldHash]; exists {
			delete(t.times, oldHash)
		}
	}
	t.insertOrder[t.head] = hash
	t.head = (t.head + 1) % t.maxSize
}

// evictOldest removes the oldest entries to make room.
// Called with lock held.
func (t *TxTracker) evictOldest() {
	evicted := 0
	for evicted < t.evictBatch {
		// Find oldest entry by scanning ring buffer from head
		oldestIdx := (t.head + evicted) % t.maxSize
		oldHash := t.insertOrder[oldestIdx]
		if oldHash != (common.Hash{}) {
			delete(t.times, oldHash)
			t.insertOrder[oldestIdx] = common.Hash{}
		}
		evicted++
	}
}

// Get returns the send time for a transaction.
func (t *TxTracker) Get(hash common.Hash) (time.Time, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	sentTime, ok := t.times[hash]
	return sentTime, ok
}

// GetAndDelete returns the send time and removes the entry.
// This is the typical usage pattern - get time when confirmed, then delete.
func (t *TxTracker) GetAndDelete(hash common.Hash) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	sentTime, ok := t.times[hash]
	if ok {
		delete(t.times, hash)
		// Note: We don't remove from insertOrder here to avoid O(n) search.
		// The entry will be cleaned up naturally when the ring buffer wraps.
	}
	return sentTime, ok
}

// Size returns the current number of tracked transactions.
func (t *TxTracker) Size() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.times)
}

// Reset clears all tracked transactions.
func (t *TxTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.times = make(map[common.Hash]time.Time, t.maxSize)
	for i := range t.insertOrder {
		t.insertOrder[i] = common.Hash{}
	}
	t.head = 0
}
