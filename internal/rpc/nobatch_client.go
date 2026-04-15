// Package rpc provides JSON-RPC client implementations.
package rpc

import (
	"context"
	"sync"
)

// NoBatchClient wraps an existing Client and converts batch sends into
// individual sends. This is needed for endpoints like the privacy proxy
// that reject JSON-RPC batch requests for security reasons.
type NoBatchClient struct {
	Client
}

// NewNoBatchClient wraps a Client so that SendRawTransactionBatch sends
// each transaction individually instead of using JSON-RPC batch mode.
func NewNoBatchClient(c Client) *NoBatchClient {
	return &NoBatchClient{Client: c}
}

// SendRawTransactionBatch sends each transaction individually in parallel.
func (c *NoBatchClient) SendRawTransactionBatch(ctx context.Context, txRLPs [][]byte) []error {
	if len(txRLPs) == 0 {
		return nil
	}

	errs := make([]error, len(txRLPs))
	var wg sync.WaitGroup
	wg.Add(len(txRLPs))
	for i, rlp := range txRLPs {
		go func(idx int, data []byte) {
			defer wg.Done()
			errs[idx] = c.Client.SendRawTransaction(ctx, data)
		}(i, rlp)
	}
	wg.Wait()
	return errs
}
