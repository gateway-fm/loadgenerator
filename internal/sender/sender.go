// Package sender provides async transaction sending with backpressure.
package sender

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// ErrAtCapacity is returned when the sender cannot accept more transactions.
var ErrAtCapacity = errors.New("sender at capacity")

// Sender handles async transaction sending with semaphore-based backpressure.
type Sender struct {
	client    rpc.Client
	semaphore chan struct{}
	logger    *slog.Logger
}

// Config for creating a Sender.
type Config struct {
	Client      rpc.Client
	Concurrency int // Max concurrent sends (default: 500)
	Logger      *slog.Logger
}

// New creates a new Sender.
func New(cfg Config) *Sender {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 500
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Sender{
		client:    cfg.Client,
		semaphore: make(chan struct{}, concurrency),
		logger:    logger,
	}
}

// SendAsync sends a transaction asynchronously.
// Returns true if the send was queued, false if at capacity.
// The callback is called with the error result (on a goroutine).
func (s *Sender) SendAsync(ctx context.Context, txData []byte, callback func(error)) bool {
	select {
	case s.semaphore <- struct{}{}: // Acquired semaphore
		go func() {
			defer func() { <-s.semaphore }() // Release semaphore

			err := s.client.SendRawTransaction(ctx, txData)
			if callback != nil {
				callback(err)
			}
		}()
		return true

	default:
		return false // At capacity
	}
}

// TrySend attempts to send a transaction.
// Returns ErrAtCapacity if the sender cannot accept more transactions.
// Otherwise returns nil immediately (actual send result comes via callback).
func (s *Sender) TrySend(ctx context.Context, txData []byte, callback func(error)) error {
	if s.SendAsync(ctx, txData, callback) {
		return nil
	}
	return ErrAtCapacity
}

// SendBatchAsync sends multiple transactions in a single HTTP request asynchronously.
// Uses JSON-RPC batch mode to amortize HTTP overhead across all TXs in the batch.
// Each callback[i] is called with the per-TX error for txDataSlice[i].
// Returns true if the batch was queued, false if at capacity.
// Only one semaphore slot is consumed for the entire batch.
func (s *Sender) SendBatchAsync(ctx context.Context, txDataSlice [][]byte, callbacks []func(error)) bool {
	if len(txDataSlice) == 0 {
		return true
	}

	select {
	case s.semaphore <- struct{}{}: // Acquired semaphore
		go func() {
			defer func() { <-s.semaphore }() // Release semaphore

			errs := s.client.SendRawTransactionBatch(ctx, txDataSlice)
			for i, cb := range callbacks {
				if cb != nil {
					var err error
					if i < len(errs) {
						err = errs[i]
					}
					cb(err)
				}
			}
		}()
		return true

	default:
		return false // At capacity
	}
}

// Available returns the number of available send slots.
func (s *Sender) Available() int {
	return cap(s.semaphore) - len(s.semaphore)
}

// Capacity returns the total send capacity.
func (s *Sender) Capacity() int {
	return cap(s.semaphore)
}

// InFlight returns the number of transactions currently being sent.
func (s *Sender) InFlight() int {
	return len(s.semaphore)
}
