package metrics

import "sync/atomic"

// AtomicSubSaturating atomically subtracts delta from *addr, saturating at 0.
// This fixes the race condition in the original code where check-then-set
// could result in negative values.
//
// Original buggy code:
//
//	atomic.AddInt64(&pendingCount, -delta)
//	if atomic.LoadInt64(&pendingCount) < 0 {
//	    atomic.StoreInt64(&pendingCount, 0)
//	}
//
// The problem: another goroutine can modify pendingCount between Load and Store.
// Solution: Use Compare-And-Swap in a loop.
func AtomicSubSaturating(addr *int64, delta int64) int64 {
	for {
		current := atomic.LoadInt64(addr)
		newVal := current - delta
		if newVal < 0 {
			newVal = 0
		}
		if atomic.CompareAndSwapInt64(addr, current, newVal) {
			return newVal
		}
		// CAS failed, retry with updated value
	}
}

// AtomicAddSaturating atomically adds delta to *addr, saturating at maxVal.
func AtomicAddSaturating(addr *int64, delta int64, maxVal int64) int64 {
	for {
		current := atomic.LoadInt64(addr)
		newVal := current + delta
		if newVal > maxVal {
			newVal = maxVal
		}
		if atomic.CompareAndSwapInt64(addr, current, newVal) {
			return newVal
		}
	}
}

// AtomicMax atomically sets *addr to max(*addr, val) and returns the new value.
func AtomicMax(addr *int64, val int64) int64 {
	for {
		current := atomic.LoadInt64(addr)
		if val <= current {
			return current
		}
		if atomic.CompareAndSwapInt64(addr, current, val) {
			return val
		}
	}
}

// Counter is a simple atomic counter with convenience methods.
type Counter struct {
	value int64
}

// Add adds delta to the counter and returns the new value.
func (c *Counter) Add(delta int64) int64 {
	return atomic.AddInt64(&c.value, delta)
}

// Inc increments the counter by 1.
func (c *Counter) Inc() int64 {
	return atomic.AddInt64(&c.value, 1)
}

// Load returns the current value.
func (c *Counter) Load() int64 {
	return atomic.LoadInt64(&c.value)
}

// Store sets the value.
func (c *Counter) Store(val int64) {
	atomic.StoreInt64(&c.value, val)
}

// Reset sets the counter to 0.
func (c *Counter) Reset() {
	atomic.StoreInt64(&c.value, 0)
}

// SubSaturating subtracts delta, saturating at 0.
func (c *Counter) SubSaturating(delta int64) int64 {
	return AtomicSubSaturating(&c.value, delta)
}

// UCounter is an unsigned atomic counter.
type UCounter struct {
	value uint64
}

// Add adds delta to the counter.
func (c *UCounter) Add(delta uint64) uint64 {
	return atomic.AddUint64(&c.value, delta)
}

// Inc increments by 1.
func (c *UCounter) Inc() uint64 {
	return atomic.AddUint64(&c.value, 1)
}

// Load returns the current value.
func (c *UCounter) Load() uint64 {
	return atomic.LoadUint64(&c.value)
}

// Store sets the value.
func (c *UCounter) Store(val uint64) {
	atomic.StoreUint64(&c.value, val)
}

// Reset sets to 0.
func (c *UCounter) Reset() {
	atomic.StoreUint64(&c.value, 0)
}
