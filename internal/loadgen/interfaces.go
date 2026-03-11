package loadgen

import "context"

// TxSender sends transactions asynchronously with backpressure.
type TxSender interface {
	SendBatchAsync(ctx context.Context, txDataSlice [][]byte, callbacks []func(error)) bool
}