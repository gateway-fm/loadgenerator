// Package pipeline provides transaction lifecycle management.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/metrics"
	"github.com/gateway-fm/loadgenerator/internal/sender"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
)

// Result contains the outcome of a pipeline execution.
type Result struct {
	TxHash common.Hash
	Nonce  uint64
	Queued bool  // Was the TX queued for sending?
	Error  error // Build/sign/encode error (nil if queued)
}

// Pipeline handles the complete transaction lifecycle.
type Pipeline struct {
	builder   txbuilder.Builder
	signer    types.Signer
	sender    *sender.Sender
	metrics   metrics.Collector
	chainID   *big.Int
	gasPrice  *big.Int
	useLegacy bool
	logger    *slog.Logger
}

// Config for creating a Pipeline.
type Config struct {
	Builder   txbuilder.Builder
	Signer    types.Signer
	Sender    *sender.Sender
	Metrics   metrics.Collector
	ChainID   *big.Int
	GasPrice  *big.Int
	UseLegacy bool // Use legacy (type 0) transactions instead of EIP-1559
	Logger    *slog.Logger
}

// New creates a new Pipeline.
func New(cfg Config) *Pipeline {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Pipeline{
		builder:   cfg.Builder,
		signer:    cfg.Signer,
		sender:    cfg.Sender,
		metrics:   cfg.Metrics,
		chainID:   cfg.ChainID,
		gasPrice:  cfg.GasPrice,
		useLegacy: cfg.UseLegacy,
		logger:    logger,
	}
}

// Execute builds, signs, and queues a transaction for sending.
// The async callback handles nonce commit/rollback after send completes.
func (p *Pipeline) Execute(acc *account.Account) Result {
	n := acc.ReserveNonce()
	// NO DEFER - callback owns the nonce lifecycle after successful queue

	// Build
	tx, err := p.builder.Build(txbuilder.TxParams{
		ChainID:   p.chainID,
		Nonce:     n.Value(),
		GasTipCap: p.gasPrice,
		GasFeeCap: p.gasPrice,
		UseLegacy: p.useLegacy,
	})
	if err != nil {
		n.Rollback() // Sync error: rollback immediately
		return Result{Nonce: n.Value(), Error: fmt.Errorf("build: %w", err)}
	}

	// Sign
	signed, err := types.SignTx(tx, p.signer, acc.PrivateKey)
	if err != nil {
		n.Rollback() // Sync error: rollback immediately
		return Result{Nonce: n.Value(), Error: fmt.Errorf("sign: %w", err)}
	}

	// Encode
	data, err := signed.MarshalBinary()
	if err != nil {
		n.Rollback() // Sync error: rollback immediately
		return Result{Nonce: n.Value(), Error: fmt.Errorf("encode: %w", err)}
	}

	// Queue for async send
	txHash := signed.Hash()
	sentTime := time.Now()

	// Capture nonce for the callback closure - callback now OWNS the nonce
	nonce := n

	queued := p.sender.SendAsync(context.Background(), data, func(sendErr error) {
		if sendErr != nil {
			nonce.Rollback() // Async failure: callback rolls back
			if p.metrics != nil {
				p.metrics.RecordTxFailed("send")
			}
		} else {
			nonce.Commit() // Async success: callback commits
		}
	})

	if !queued {
		n.Rollback() // Queue full: rollback immediately
		return Result{TxHash: txHash, Nonce: n.Value(), Queued: false}
	}

	// Record metrics (TX is now in flight)
	if p.metrics != nil {
		p.metrics.RecordTxSent(txHash, sentTime)
		p.metrics.IncTxSent()
	}

	// SUCCESS: Don't commit OR rollback here - callback will handle it
	return Result{TxHash: txHash, Nonce: n.Value(), Queued: true}
}

// ExecuteWithGas is like Execute but allows overriding the gas price.
// The async callback handles nonce commit/rollback after send completes.
func (p *Pipeline) ExecuteWithGas(acc *account.Account, gasPrice *big.Int) Result {
	n := acc.ReserveNonce()
	// NO DEFER - callback owns the nonce lifecycle after successful queue

	tx, err := p.builder.Build(txbuilder.TxParams{
		ChainID:   p.chainID,
		Nonce:     n.Value(),
		GasTipCap: gasPrice,
		GasFeeCap: gasPrice,
		UseLegacy: p.useLegacy,
	})
	if err != nil {
		n.Rollback() // Sync error: rollback immediately
		return Result{Nonce: n.Value(), Error: fmt.Errorf("build: %w", err)}
	}

	signed, err := types.SignTx(tx, p.signer, acc.PrivateKey)
	if err != nil {
		n.Rollback() // Sync error: rollback immediately
		return Result{Nonce: n.Value(), Error: fmt.Errorf("sign: %w", err)}
	}

	data, err := signed.MarshalBinary()
	if err != nil {
		n.Rollback() // Sync error: rollback immediately
		return Result{Nonce: n.Value(), Error: fmt.Errorf("encode: %w", err)}
	}

	txHash := signed.Hash()
	sentTime := time.Now()

	// Capture nonce for the callback closure - callback now OWNS the nonce
	nonce := n

	queued := p.sender.SendAsync(context.Background(), data, func(sendErr error) {
		if sendErr != nil {
			nonce.Rollback() // Async failure: callback rolls back
			if p.metrics != nil {
				p.metrics.RecordTxFailed("send")
			}
		} else {
			nonce.Commit() // Async success: callback commits
		}
	})

	if !queued {
		n.Rollback() // Queue full: rollback immediately
		return Result{TxHash: txHash, Nonce: n.Value(), Queued: false}
	}

	if p.metrics != nil {
		p.metrics.RecordTxSent(txHash, sentTime)
		p.metrics.IncTxSent()
	}

	// SUCCESS: Don't commit OR rollback here - callback will handle it
	return Result{TxHash: txHash, Nonce: n.Value(), Queued: true}
}
