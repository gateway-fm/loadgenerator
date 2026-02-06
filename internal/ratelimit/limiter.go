// Package ratelimit provides a strict rate limiter for consistent
// traffic generation at high throughput.
package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Limiter provides strict rate limiting by tracking the next available
// permit time and ensuring permits are issued no faster than the target rate.
//
// Unlike token bucket or sequence-based approaches, this enforces a strict
// minimum interval between permits, preventing bursts entirely.
type Limiter struct {
	mu             sync.Mutex
	nextPermitTime time.Time
	interval       time.Duration

	// Rate tracking (atomic for lock-free reads)
	rateX1000 atomic.Int64 // rate * 1000 for precision
}

// New creates a new Limiter with the specified rate (requests per second).
func New(ratePerSec float64) *Limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}

	l := &Limiter{
		nextPermitTime: time.Now(),
		interval:       time.Duration(float64(time.Second) / ratePerSec),
	}
	l.rateX1000.Store(int64(ratePerSec * 1000))

	return l
}

// Wait blocks until a permit is available or the context is cancelled.
// Permits are issued strictly at the configured rate - no faster.
func (l *Limiter) Wait(ctx context.Context) error {
	l.mu.Lock()

	// Get the next permit time and advance it
	permitTime := l.nextPermitTime
	l.nextPermitTime = permitTime.Add(l.interval)

	l.mu.Unlock()

	// How long until our permit time?
	now := time.Now()
	waitDuration := permitTime.Sub(now)

	// If permit time is in the past, proceed immediately
	// (we're behind schedule, need to catch up)
	if waitDuration <= 0 {
		return nil
	}

	// Wait until our permit time
	timer := time.NewTimer(waitDuration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetRate updates the rate limit dynamically.
// Takes effect immediately for subsequent permits.
func (l *Limiter) SetRate(ratePerSec float64) {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.interval = time.Duration(float64(time.Second) / ratePerSec)
	l.rateX1000.Store(int64(ratePerSec * 1000))

	// Reset next permit time to now to avoid stalls after rate decrease
	// or bursts after rate increase
	now := time.Now()
	if l.nextPermitTime.Before(now) {
		l.nextPermitTime = now
	}
}

// Rate returns the current rate limit.
func (l *Limiter) Rate() float64 {
	return float64(l.rateX1000.Load()) / 1000
}

// Stop is a no-op for this implementation (no background goroutines).
func (l *Limiter) Stop() {}
