package main

import (
	"context"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/internal/verification"
)

// startIncrementalVerification starts the background incremental verification goroutine.
// This runs every 5 minutes during long tests to avoid massive verification at the end.
func (lg *LoadGenerator) startIncrementalVerification() {
	lg.incrementalStopCh = make(chan struct{})

	// Initialize verifier if not already done
	if lg.verifier == nil {
		lg.verifier = verification.NewVerifierWithConfig(lg.l2Client, lg.logger, lg.includeDepositTx)
	}

	// Clear previous snapshots
	lg.incrementalSnapshotsMu.Lock()
	lg.incrementalSnapshots = nil
	lg.incrementalSnapshotsMu.Unlock()

	lg.wg.Add(1)
	go func() {
		defer lg.wg.Done()

		// Run every 5 minutes
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		lg.logger.Info("started incremental verification", "interval", "5m")

		for {
			select {
			case <-lg.incrementalStopCh:
				lg.logger.Info("stopped incremental verification")
				return
			case <-lg.ctx.Done():
				return
			case <-ticker.C:
				lg.runIncrementalVerification()
			}
		}
	}()
}

// runIncrementalVerification performs a single incremental verification snapshot.
func (lg *LoadGenerator) runIncrementalVerification() {
	// Get recent blocks
	lg.recentBlockNumbersMu.Lock()
	recentBlocks := make([]uint64, len(lg.recentBlockNumbers))
	copy(recentBlocks, lg.recentBlockNumbers)
	lg.recentBlockNumbers = nil // Clear after copying
	lg.recentBlockNumbersMu.Unlock()

	// Get recent confirmed TX hashes
	lg.recentConfirmedMu.Lock()
	recentHashes := make([]string, len(lg.recentConfirmedHashes))
	copy(recentHashes, lg.recentConfirmedHashes)
	lg.recentConfirmedHashes = nil // Clear after copying
	lg.recentConfirmedMu.Unlock()

	// Debug: Log what data we have
	lg.logger.Info("incremental verification data collected",
		"recentBlocks", len(recentBlocks),
		"recentHashes", len(recentHashes))

	if len(recentBlocks) == 0 && len(recentHashes) == 0 {
		lg.logger.Debug("incremental verification skipped - no recent data")
		return
	}

	// Create verification context with timeout
	ctx, cancel := context.WithTimeout(lg.ctx, 30*time.Second)
	defer cancel()

	// Run incremental verification
	snapshot := lg.verifier.VerifyIncremental(
		ctx,
		recentBlocks,
		recentHashes,
		lg.txOrdering, // "fifo", "tip_desc", or "tip_asc"
		10,            // blocks to sample for tip ordering
		10,            // receipts to sample
	)

	// Store snapshot
	lg.incrementalSnapshotsMu.Lock()
	lg.incrementalSnapshots = append(lg.incrementalSnapshots, *snapshot)
	lg.incrementalSnapshotsMu.Unlock()

	lg.logger.Info("incremental verification snapshot",
		"firstBlock", snapshot.FirstBlock,
		"lastBlock", snapshot.LastBlock,
		"blocksSampled", snapshot.BlocksSampled,
		"receiptsSampled", snapshot.ReceiptsSampled,
		"violations", snapshot.Violations,
		"receiptsReverted", snapshot.ReceiptsReverted,
		"totalSnapshots", len(lg.incrementalSnapshots))
}

// stopIncrementalVerification stops the background verification and runs a final snapshot.
func (lg *LoadGenerator) stopIncrementalVerification() {
	if lg.incrementalStopCh != nil {
		close(lg.incrementalStopCh)
		lg.incrementalStopCh = nil
	}

	// Run one final incremental verification to capture any remaining data
	lg.runIncrementalVerification()
}

// getIncrementalSnapshots returns the collected incremental verification snapshots.
func (lg *LoadGenerator) getIncrementalSnapshots() []storage.IncrementalVerificationSnapshot {
	lg.incrementalSnapshotsMu.Lock()
	defer lg.incrementalSnapshotsMu.Unlock()
	result := make([]storage.IncrementalVerificationSnapshot, len(lg.incrementalSnapshots))
	copy(result, lg.incrementalSnapshots)
	return result
}