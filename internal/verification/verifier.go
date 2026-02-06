// Package verification provides post-test chain verification functionality.
package verification

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// VerificationProgress holds the current state of verification progress.
type VerificationProgress struct {
	Phase           types.VerifyPhase
	Message         string
	BlocksTotal     int
	BlocksVerified  int
	ReceiptsTotal   int
	ReceiptsSampled int
}

// ProgressCallback is called during verification to report progress.
type ProgressCallback func(VerificationProgress)

// Verifier performs post-test verification against the chain.
type Verifier struct {
	l2Client         rpc.Client
	logger           *slog.Logger
	includeDepositTx bool // Whether deposit TXs are included in blocks
}

// NewVerifier creates a new verification handler.
func NewVerifier(l2Client rpc.Client, logger *slog.Logger) *Verifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &Verifier{
		l2Client:         l2Client,
		logger:           logger,
		includeDepositTx: false, // Default: no deposit TXs (legacy behavior)
	}
}

// NewVerifierWithConfig creates a new verification handler with configuration options.
func NewVerifierWithConfig(l2Client rpc.Client, logger *slog.Logger, includeDepositTx bool) *Verifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &Verifier{
		l2Client:         l2Client,
		logger:           logger,
		includeDepositTx: includeDepositTx,
	}
}

// VerifyTestResults performs comprehensive verification after a test completes.
func (v *Verifier) VerifyTestResults(
	ctx context.Context,
	onChainTxCount uint64,
	confirmedCount uint64,
	firstBlock uint64,
	lastBlock uint64,
	txOrdering string,
	confirmedTxHashes []string,
) *storage.VerificationResult {
	return v.VerifyTestResultsWithProgress(ctx, onChainTxCount, confirmedCount, firstBlock, lastBlock, txOrdering, confirmedTxHashes, nil)
}

// VerifyTestResultsWithProgress performs comprehensive verification with progress reporting.
func (v *Verifier) VerifyTestResultsWithProgress(
	ctx context.Context,
	onChainTxCount uint64,
	confirmedCount uint64,
	firstBlock uint64,
	lastBlock uint64,
	txOrdering string,
	confirmedTxHashes []string,
	progressCb ProgressCallback,
) *storage.VerificationResult {
	result := &storage.VerificationResult{}

	// Helper to report progress
	reportProgress := func(p VerificationProgress) {
		if progressCb != nil {
			progressCb(p)
		}
	}

	// 1. Compare on-chain TX count vs confirmed count
	reportProgress(VerificationProgress{
		Phase:   types.VerifyPhaseTxCount,
		Message: "Comparing on-chain TX count...",
	})
	result.TxCountDelta = int64(onChainTxCount) - int64(confirmedCount)
	result.MetricsMatch = result.TxCountDelta == 0

	v.logger.Info("verifying test results",
		"onChainTxCount", onChainTxCount,
		"confirmedCount", confirmedCount,
		"txCountDelta", result.TxCountDelta,
		"firstBlock", firstBlock,
		"lastBlock", lastBlock,
		"txOrdering", txOrdering)

	// 2. Verify tip ordering if enabled (sample every 10th block)
	if txOrdering == "tip_desc" || txOrdering == "tip_asc" {
		result.TipOrdering = v.verifyTipOrderingWithProgress(ctx, firstBlock, lastBlock, txOrdering, 10, reportProgress)
	}

	// 3. Sample TX receipts (~100 random confirmed TXs)
	if len(confirmedTxHashes) > 0 {
		result.TxReceipts = v.sampleTxReceiptsWithProgress(ctx, confirmedTxHashes, 100, reportProgress)
	}

	// 4. Generate warnings
	if result.TxCountDelta > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("On-chain has %d more TXs than confirmed - possible missed confirmations", result.TxCountDelta))
	} else if result.TxCountDelta < 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Confirmed %d more TXs than on-chain - possible double counting", -result.TxCountDelta))
	}
	if result.TxReceipts != nil && result.TxReceipts.RevertCount > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d of %d sampled TXs reverted", result.TxReceipts.RevertCount, result.TxReceipts.SampleSize))
	}

	result.AllChecksPass = result.MetricsMatch &&
		(result.TipOrdering == nil || result.TipOrdering.Verified) &&
		(result.TxReceipts == nil || result.TxReceipts.RevertCount == 0)

	v.logger.Info("verification complete",
		"allChecksPass", result.AllChecksPass,
		"metricsMatch", result.MetricsMatch,
		"warnings", len(result.Warnings))

	return result
}

// verifyTipOrdering checks that transactions in blocks are ordered by tip.
// sampleInterval controls how many blocks to skip (e.g., 10 = every 10th block).
func (v *Verifier) verifyTipOrdering(
	ctx context.Context,
	firstBlock uint64,
	lastBlock uint64,
	ordering string, // "tip_desc" or "tip_asc"
	sampleInterval int,
) *storage.TipOrderingResult {
	return v.verifyTipOrderingWithProgress(ctx, firstBlock, lastBlock, ordering, sampleInterval, nil)
}

// verifyTipOrderingWithProgress checks tip ordering with progress reporting.
func (v *Verifier) verifyTipOrderingWithProgress(
	ctx context.Context,
	firstBlock uint64,
	lastBlock uint64,
	ordering string, // "tip_desc" or "tip_asc"
	sampleInterval int,
	reportProgress func(VerificationProgress),
) *storage.TipOrderingResult {
	result := &storage.TipOrderingResult{
		TotalBlocks: int(lastBlock - firstBlock + 1),
	}

	if firstBlock == 0 || lastBlock == 0 || lastBlock < firstBlock {
		v.logger.Warn("invalid block range for tip ordering verification")
		result.Verified = true // No blocks to verify
		return result
	}

	// Build list of block numbers to sample
	var blockNums []uint64
	for blockNum := firstBlock; blockNum <= lastBlock; blockNum += uint64(sampleInterval) {
		blockNums = append(blockNums, blockNum)
	}

	v.logger.Info("verifying tip ordering",
		"firstBlock", firstBlock,
		"lastBlock", lastBlock,
		"ordering", ordering,
		"sampleInterval", sampleInterval,
		"blocksToSample", len(blockNums))

	// Report initial progress
	if reportProgress != nil {
		reportProgress(VerificationProgress{
			Phase:          types.VerifyPhaseTipOrder,
			Message:        fmt.Sprintf("Checking tip ordering (0/%d blocks)...", len(blockNums)),
			BlocksTotal:    len(blockNums),
			BlocksVerified: 0,
		})
	}

	// Fetch blocks in batches using batch RPC
	const batchSize = 50
	blocksVerified := 0
	for batchStart := 0; batchStart < len(blockNums); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(blockNums) {
			batchEnd = len(blockNums)
		}
		batchBlockNums := blockNums[batchStart:batchEnd]

		blocks, err := v.l2Client.GetBlocksByNumberFullBatch(ctx, batchBlockNums)
		if err != nil {
			v.logger.Warn("batch block fetch failed for tip verification, falling back to individual",
				"error", err)
			// Fallback to individual requests
			for _, blockNum := range batchBlockNums {
				block, err := v.l2Client.GetBlockByNumberFull(ctx, blockNum)
				if err != nil {
					v.logger.Debug("failed to fetch block for tip verification", "block", blockNum, "error", err)
					continue
				}
				v.processBlockForTipOrdering(block, blockNum, ordering, result)
			}
		} else {
			// Process batch results
			for i, block := range blocks {
				v.processBlockForTipOrdering(block, batchBlockNums[i], ordering, result)
			}
		}

		// Report progress after each batch
		blocksVerified = batchEnd
		if reportProgress != nil {
			reportProgress(VerificationProgress{
				Phase:          types.VerifyPhaseTipOrder,
				Message:        fmt.Sprintf("Checking tip ordering (%d/%d blocks)...", blocksVerified, len(blockNums)),
				BlocksTotal:    len(blockNums),
				BlocksVerified: blocksVerified,
			})
		}
	}

	// Verification passes if all sampled blocks are correctly ordered
	result.Verified = result.BlocksSampled == 0 || result.CorrectlyOrdered == result.BlocksSampled
	result.ViolationCount = len(result.OrderingViolations)

	v.logger.Info("tip ordering verification complete",
		"verified", result.Verified,
		"blocksSampled", result.BlocksSampled,
		"correctlyOrdered", result.CorrectlyOrdered,
		"violations", result.ViolationCount)

	return result
}

// processBlockForTipOrdering processes a single block for tip ordering verification.
// IMPORTANT: Tip ordering is SENDER-BASED, not individual TX-based.
// The block builder orders senders by their max tip, then includes all TXs from each
// sender in nonce order. This is correct because Ethereum requires nonce ordering per sender.
func (v *Verifier) processBlockForTipOrdering(block *rpc.BlockFull, blockNum uint64, ordering string, result *storage.TipOrderingResult) {
	if block == nil || block.TxCount == 0 {
		return
	}

	result.BlocksSampled++

	// Extract sender tips in order of first appearance
	// If deposit TXs are enabled, extractSenderTips will skip the L1→L2 deposit transaction
	// at index 0 (which is a system transaction with no tip that must always be first).
	senderMaxTips, senderOrder := v.extractSenderTips(block)

	// Need at least 2 senders to verify ordering
	if len(senderOrder) <= 1 {
		result.CorrectlyOrdered++
		return
	}

	// Build list of max tips in sender order for verification
	senderTips := make([]uint64, len(senderOrder))
	for i, sender := range senderOrder {
		senderTips[i] = senderMaxTips[sender]
	}

	isOrdered := v.checkSenderTipOrdering(senderTips, ordering)

	// Calculate user TX count (exclude deposit TX if enabled)
	userTxCount := block.TxCount
	if v.includeDepositTx {
		userTxCount = block.TxCount - 1
	}

	analysis := storage.BlockTipAnalysis{
		BlockNumber: blockNum,
		TxCount:     userTxCount,
		IsOrdered:   isOrdered,
	}

	// Include sender tips for sample (to keep response size reasonable)
	if len(senderTips) <= 10 {
		analysis.Tips = senderTips
	} else {
		analysis.Tips = senderTips[:10]
	}

	if isOrdered {
		result.CorrectlyOrdered++
	} else {
		// Record violations at sender boundaries
		violations := v.findSenderOrderingViolations(blockNum, senderTips, ordering)
		result.OrderingViolations = append(result.OrderingViolations, violations...)
	}

	// Keep sample of first 5 blocks for inspection
	if len(result.SampleBlocks) < 5 {
		result.SampleBlocks = append(result.SampleBlocks, analysis)
	}
}

// extractSenderTips extracts max tip per sender and the order senders appear in the block.
// Returns: map of sender->maxTip, and slice of senders in order of first appearance.
func (v *Verifier) extractSenderTips(block *rpc.BlockFull) (map[string]uint64, []string) {
	senderMaxTips := make(map[string]uint64)
	var senderOrder []string
	seenSender := make(map[string]bool)

	for i, tx := range block.Transactions {
		// Skip deposit transaction at index 0 only if deposit TXs are enabled
		// Deposit TXs are L1 attributes system TXs (type=126) with no tip
		if v.includeDepositTx && i == 0 {
			continue
		}

		sender := tx.From
		tip := tx.MaxPriorityFeePerGas
		if tip == 0 && tx.GasPrice > 0 {
			// Legacy transaction - use gasPrice as effective tip
			tip = tx.GasPrice
		}

		// Track sender order (first appearance)
		if !seenSender[sender] {
			seenSender[sender] = true
			senderOrder = append(senderOrder, sender)
		}

		// Track max tip per sender
		if tip > senderMaxTips[sender] {
			senderMaxTips[sender] = tip
		}
	}

	return senderMaxTips, senderOrder
}

// checkSenderTipOrdering returns true if sender tips are correctly ordered.
// This checks that senders appear in order of their max tip (descending for tip_desc).
func (*Verifier) checkSenderTipOrdering(senderTips []uint64, ordering string) bool {
	if len(senderTips) <= 1 {
		return true
	}

	for i := 1; i < len(senderTips); i++ {
		switch ordering {
		case "tip_desc":
			// Each sender's max tip should be <= the previous sender's max tip
			if senderTips[i] > senderTips[i-1] {
				return false
			}
		case "tip_asc":
			// Each sender's max tip should be >= the previous sender's max tip
			if senderTips[i] < senderTips[i-1] {
				return false
			}
		}
	}
	return true
}

// findSenderOrderingViolations returns specific violations at sender boundaries in a block.
// The index refers to the sender position (not individual TX index).
func (*Verifier) findSenderOrderingViolations(blockNum uint64, senderTips []uint64, ordering string) []storage.OrderingViolation {
	var violations []storage.OrderingViolation

	for i := 1; i < len(senderTips); i++ {
		var isViolation bool
		switch ordering {
		case "tip_desc":
			isViolation = senderTips[i] > senderTips[i-1]
		case "tip_asc":
			isViolation = senderTips[i] < senderTips[i-1]
		}

		if isViolation {
			violations = append(violations, storage.OrderingViolation{
				BlockNumber: blockNum,
				TxIndex:     i, // Sender index (not TX index)
				ExpectedTip: senderTips[i-1],
				ActualTip:   senderTips[i],
			})
		}
	}

	// Limit to first 10 violations per block to keep response size reasonable
	if len(violations) > 10 {
		violations = violations[:10]
	}

	return violations
}

// sampleTxReceipts fetches receipts for a random sample of confirmed transactions.
func (v *Verifier) sampleTxReceipts(
	ctx context.Context,
	confirmedTxHashes []string,
	sampleSize int,
) *storage.TxReceiptVerification {
	return v.sampleTxReceiptsWithProgress(ctx, confirmedTxHashes, sampleSize, nil)
}

// sampleTxReceiptsWithProgress fetches receipts with progress reporting.
func (v *Verifier) sampleTxReceiptsWithProgress(
	ctx context.Context,
	confirmedTxHashes []string,
	sampleSize int,
	reportProgress func(VerificationProgress),
) *storage.TxReceiptVerification {
	result := &storage.TxReceiptVerification{
		MinGasUsed: ^uint64(0), // Max uint64
	}

	if len(confirmedTxHashes) == 0 {
		return result
	}

	// Select random sample
	if sampleSize > len(confirmedTxHashes) {
		sampleSize = len(confirmedTxHashes)
	}

	// Shuffle and take first N
	indices := make([]int, len(confirmedTxHashes))
	for i := range indices {
		indices[i] = i
	}
	rand.Shuffle(len(indices), func(i, j int) {
		indices[i], indices[j] = indices[j], indices[i]
	})

	sampleIndices := indices[:sampleSize]
	sort.Ints(sampleIndices) // Sort for consistent ordering

	// Build list of tx hashes to fetch
	sampleHashes := make([]string, len(sampleIndices))
	for i, idx := range sampleIndices {
		sampleHashes[i] = confirmedTxHashes[idx]
	}

	v.logger.Info("sampling TX receipts",
		"totalConfirmed", len(confirmedTxHashes),
		"sampleSize", sampleSize)

	result.SampleSize = sampleSize

	// Report initial progress
	if reportProgress != nil {
		reportProgress(VerificationProgress{
			Phase:           types.VerifyPhaseReceipts,
			Message:         fmt.Sprintf("Sampling receipts (0/%d)...", sampleSize),
			ReceiptsTotal:   sampleSize,
			ReceiptsSampled: 0,
		})
	}

	// Fetch receipts in batches using batch RPC
	const batchSize = 50
	var totalGasUsed uint64
	sampleIdx := 0
	receiptsSampled := 0

	for batchStart := 0; batchStart < len(sampleHashes); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(sampleHashes) {
			batchEnd = len(sampleHashes)
		}
		batchHashes := sampleHashes[batchStart:batchEnd]

		receipts, err := v.l2Client.GetTransactionReceiptsBatch(ctx, batchHashes)
		if err != nil {
			v.logger.Warn("batch receipt fetch failed, falling back to individual",
				"error", err)
			// Fallback to individual requests
			for i, txHash := range batchHashes {
				receipt, err := v.l2Client.GetTransactionReceipt(ctx, txHash)
				if err != nil {
					v.logger.Debug("failed to fetch receipt", "txHash", txHash, "error", err)
					continue
				}
				v.processReceipt(receipt, txHash, sampleIdx+i, result, &totalGasUsed)
			}
		} else {
			// Process batch results
			for i, receipt := range receipts {
				v.processReceipt(receipt, batchHashes[i], sampleIdx+i, result, &totalGasUsed)
			}
		}
		sampleIdx += len(batchHashes)

		// Report progress after each batch
		receiptsSampled = sampleIdx
		if reportProgress != nil {
			reportProgress(VerificationProgress{
				Phase:           types.VerifyPhaseReceipts,
				Message:         fmt.Sprintf("Sampling receipts (%d/%d)...", receiptsSampled, sampleSize),
				ReceiptsTotal:   sampleSize,
				ReceiptsSampled: receiptsSampled,
			})
		}
	}

	// Calculate average
	if result.SuccessCount+result.RevertCount > 0 {
		result.AvgGasUsed = totalGasUsed / uint64(result.SuccessCount+result.RevertCount)
	}

	// Reset MinGasUsed if no receipts found
	if result.MinGasUsed == ^uint64(0) {
		result.MinGasUsed = 0
	}

	v.logger.Info("TX receipt sampling complete",
		"successCount", result.SuccessCount,
		"revertCount", result.RevertCount,
		"avgGasUsed", result.AvgGasUsed)

	return result
}

// processReceipt processes a single receipt and updates the verification result.
func (v *Verifier) processReceipt(receipt *rpc.TransactionReceipt, txHash string, idx int, result *storage.TxReceiptVerification, totalGasUsed *uint64) {
	if receipt == nil {
		v.logger.Debug("receipt not found", "txHash", txHash)
		return
	}

	sample := storage.TxReceiptSample{
		TxHash:            txHash,
		BlockNumber:       receipt.BlockNumber,
		GasUsed:           receipt.GasUsed,
		Status:            receipt.Status,
		EffectiveGasPrice: receipt.EffectiveGasPrice,
	}

	if receipt.Status == 1 {
		result.SuccessCount++
	} else {
		result.RevertCount++
		// Track all reverted TXs (up to 100)
		if len(result.RevertedTxs) < 100 {
			result.RevertedTxs = append(result.RevertedTxs, sample)
		}
	}

	*totalGasUsed += receipt.GasUsed
	result.TotalGasVerified += receipt.GasUsed

	if receipt.GasUsed < result.MinGasUsed {
		result.MinGasUsed = receipt.GasUsed
	}
	if receipt.GasUsed > result.MaxGasUsed {
		result.MaxGasUsed = receipt.GasUsed
	}

	// Keep first 10 samples for inspection
	if idx < 10 {
		result.Samples = append(result.Samples, sample)
	}
}

// VerifyIncremental performs a quick verification snapshot of recent blocks and receipts.
// This is called periodically during long-running tests to avoid huge verification at the end.
// Parameters:
//   - recentBlocks: list of recent block numbers to check for tip ordering
//   - recentTxHashes: list of recent confirmed TX hashes to sample receipts from
//   - txOrdering: "tip_desc", "tip_asc", or "" (empty means skip tip ordering check)
//   - blocksToSample: how many blocks to check for tip ordering (default 10)
//   - receiptsToSample: how many receipts to sample (default 10)
func (v *Verifier) VerifyIncremental(
	ctx context.Context,
	recentBlocks []uint64,
	recentTxHashes []string,
	txOrdering string,
	blocksToSample int,
	receiptsToSample int,
) *storage.IncrementalVerificationSnapshot {
	snapshot := &storage.IncrementalVerificationSnapshot{
		Timestamp: time.Now(),
	}

	if len(recentBlocks) == 0 {
		return snapshot
	}

	// Set block range
	snapshot.FirstBlock = recentBlocks[0]
	snapshot.LastBlock = recentBlocks[len(recentBlocks)-1]

	// Sample blocks for tip ordering (if enabled)
	if (txOrdering == "tip_desc" || txOrdering == "tip_asc") && len(recentBlocks) > 0 {
		v.verifyBlocksIncremental(ctx, recentBlocks, txOrdering, blocksToSample, snapshot)
	}

	// Sample receipts
	if len(recentTxHashes) > 0 {
		v.sampleReceiptsIncremental(ctx, recentTxHashes, receiptsToSample, snapshot)
	}

	v.logger.Debug("incremental verification snapshot",
		"firstBlock", snapshot.FirstBlock,
		"lastBlock", snapshot.LastBlock,
		"blocksSampled", snapshot.BlocksSampled,
		"blocksOrdered", snapshot.BlocksOrdered,
		"violations", snapshot.Violations,
		"receiptsSampled", snapshot.ReceiptsSampled,
		"receiptsSuccess", snapshot.ReceiptsSuccess,
		"receiptsReverted", snapshot.ReceiptsReverted)

	return snapshot
}

// verifyBlocksIncremental checks a sample of blocks for tip ordering.
func (v *Verifier) verifyBlocksIncremental(
	ctx context.Context,
	blockNums []uint64,
	ordering string,
	maxBlocks int,
	snapshot *storage.IncrementalVerificationSnapshot,
) {
	// Sample evenly distributed blocks
	step := 1
	if len(blockNums) > maxBlocks {
		step = len(blockNums) / maxBlocks
	}

	var blocksToFetch []uint64
	for i := 0; i < len(blockNums) && len(blocksToFetch) < maxBlocks; i += step {
		blocksToFetch = append(blocksToFetch, blockNums[i])
	}

	if len(blocksToFetch) == 0 {
		return
	}

	// Batch fetch blocks
	blocks, err := v.l2Client.GetBlocksByNumberFullBatch(ctx, blocksToFetch)
	if err != nil {
		v.logger.Debug("incremental batch block fetch failed", "error", err)
		return
	}

	for i, block := range blocks {
		if block == nil || block.TxCount <= 1 {
			continue
		}

		snapshot.BlocksSampled++

		// Extract sender tips (sender-based ordering, not individual TX ordering)
		senderMaxTips, senderOrder := v.extractSenderTips(block)

		// Need at least 2 senders to verify ordering
		if len(senderOrder) <= 1 {
			snapshot.BlocksOrdered++
			continue
		}

		// Build list of max tips in sender order
		senderTips := make([]uint64, len(senderOrder))
		for j, sender := range senderOrder {
			senderTips[j] = senderMaxTips[sender]
		}

		if v.checkSenderTipOrdering(senderTips, ordering) {
			snapshot.BlocksOrdered++
		} else {
			snapshot.Violations++
			v.logger.Debug("tip ordering violation in incremental check",
				"block", blocksToFetch[i],
				"senderCount", len(senderOrder))
		}
	}
}

// sampleReceiptsIncremental samples receipts from recent confirmations.
func (v *Verifier) sampleReceiptsIncremental(
	ctx context.Context,
	txHashes []string,
	maxReceipts int,
	snapshot *storage.IncrementalVerificationSnapshot,
) {
	// Sample randomly from recent hashes
	sampleSize := maxReceipts
	if sampleSize > len(txHashes) {
		sampleSize = len(txHashes)
	}

	// Pick evenly distributed samples
	step := 1
	if len(txHashes) > sampleSize {
		step = len(txHashes) / sampleSize
	}

	var hashesToFetch []string
	for i := 0; i < len(txHashes) && len(hashesToFetch) < sampleSize; i += step {
		hashesToFetch = append(hashesToFetch, txHashes[i])
	}

	if len(hashesToFetch) == 0 {
		return
	}

	// Batch fetch receipts
	receipts, err := v.l2Client.GetTransactionReceiptsBatch(ctx, hashesToFetch)
	if err != nil {
		v.logger.Debug("incremental batch receipt fetch failed", "error", err)
		return
	}

	// Initialize min to max uint64 so first receipt sets the actual min
	snapshot.MinGasUsed = ^uint64(0)

	for i, receipt := range receipts {
		if receipt == nil {
			continue
		}

		snapshot.ReceiptsSampled++
		snapshot.TotalGasUsed += receipt.GasUsed

		// Track min/max gas
		if receipt.GasUsed < snapshot.MinGasUsed {
			snapshot.MinGasUsed = receipt.GasUsed
		}
		if receipt.GasUsed > snapshot.MaxGasUsed {
			snapshot.MaxGasUsed = receipt.GasUsed
		}

		if receipt.Status == 1 {
			snapshot.ReceiptsSuccess++
		} else {
			snapshot.ReceiptsReverted++
			// Track reverted TXs (up to 10 per snapshot)
			if len(snapshot.RevertedTxs) < 10 {
				snapshot.RevertedTxs = append(snapshot.RevertedTxs, storage.TxReceiptSample{
					TxHash:            hashesToFetch[i],
					BlockNumber:       receipt.BlockNumber,
					GasUsed:           receipt.GasUsed,
					Status:            receipt.Status,
					EffectiveGasPrice: receipt.EffectiveGasPrice,
				})
			}
			v.logger.Warn("REVERTED TX detected in verification sampling",
				"txHash", hashesToFetch[i],
				"blockNumber", receipt.BlockNumber,
				"gasUsed", receipt.GasUsed,
				"status", receipt.Status)
		}
	}

	// Reset min if no receipts found
	if snapshot.MinGasUsed == ^uint64(0) {
		snapshot.MinGasUsed = 0
	}
}

// AggregateSnapshots combines incremental verification snapshots into a final result.
// This is called at test end to produce the final VerificationResult.
func (v *Verifier) AggregateSnapshots(
	snapshots []storage.IncrementalVerificationSnapshot,
	onChainTxCount uint64,
	confirmedCount uint64,
	txOrdering string,
) *storage.VerificationResult {
	result := &storage.VerificationResult{
		IncrementalMode: true,
		SnapshotCount:   len(snapshots),
	}

	// Keep last 100 snapshots for inspection
	if len(snapshots) > 100 {
		result.Snapshots = snapshots[len(snapshots)-100:]
	} else {
		result.Snapshots = snapshots
	}

	// Compare on-chain TX count vs confirmed
	result.TxCountDelta = int64(onChainTxCount) - int64(confirmedCount)
	result.MetricsMatch = result.TxCountDelta == 0

	// Aggregate tip ordering results
	if txOrdering == "tip_desc" || txOrdering == "tip_asc" {
		tipResult := &storage.TipOrderingResult{}
		for _, snap := range snapshots {
			tipResult.BlocksSampled += snap.BlocksSampled
			tipResult.CorrectlyOrdered += snap.BlocksOrdered
			tipResult.ViolationCount += snap.Violations
		}
		tipResult.TotalBlocks = tipResult.BlocksSampled // For incremental, sampled = total checked
		tipResult.Verified = tipResult.BlocksSampled == 0 || tipResult.CorrectlyOrdered == tipResult.BlocksSampled
		result.TipOrdering = tipResult
	}

	// Aggregate receipt results
	receiptResult := &storage.TxReceiptVerification{
		MinGasUsed: ^uint64(0), // Initialize to max so first snapshot sets actual min
	}
	for _, snap := range snapshots {
		receiptResult.SampleSize += snap.ReceiptsSampled
		receiptResult.SuccessCount += snap.ReceiptsSuccess
		receiptResult.RevertCount += snap.ReceiptsReverted
		receiptResult.TotalGasVerified += snap.TotalGasUsed

		// Aggregate min/max (only consider snapshots with actual receipts)
		if snap.ReceiptsSampled > 0 {
			if snap.MinGasUsed < receiptResult.MinGasUsed {
				receiptResult.MinGasUsed = snap.MinGasUsed
			}
			if snap.MaxGasUsed > receiptResult.MaxGasUsed {
				receiptResult.MaxGasUsed = snap.MaxGasUsed
			}
		}

		// Aggregate reverted TXs (up to 100 total)
		for _, revertedTx := range snap.RevertedTxs {
			if len(receiptResult.RevertedTxs) < 100 {
				receiptResult.RevertedTxs = append(receiptResult.RevertedTxs, revertedTx)
			}
		}
	}
	if receiptResult.SampleSize > 0 {
		receiptResult.AvgGasUsed = receiptResult.TotalGasVerified / uint64(receiptResult.SampleSize)
	}
	// Reset min if no receipts found
	if receiptResult.MinGasUsed == ^uint64(0) {
		receiptResult.MinGasUsed = 0
	}
	result.TxReceipts = receiptResult

	// Generate warnings
	if result.TxCountDelta > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("On-chain has %d more TXs than confirmed - possible missed confirmations", result.TxCountDelta))
	} else if result.TxCountDelta < 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Confirmed %d more TXs than on-chain - possible double counting", -result.TxCountDelta))
	}
	if receiptResult.RevertCount > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d of %d sampled TXs reverted across %d snapshots",
				receiptResult.RevertCount, receiptResult.SampleSize, len(snapshots)))
	}

	// Overall pass/fail
	result.AllChecksPass = result.MetricsMatch &&
		(result.TipOrdering == nil || result.TipOrdering.Verified) &&
		receiptResult.RevertCount == 0

	totalBlocksSampled := 0
	if result.TipOrdering != nil {
		totalBlocksSampled = result.TipOrdering.BlocksSampled
	}
	v.logger.Info("aggregated incremental verification",
		"snapshots", len(snapshots),
		"metricsMatch", result.MetricsMatch,
		"allChecksPass", result.AllChecksPass,
		"totalBlocksSampled", totalBlocksSampled,
		"totalReceiptsSampled", receiptResult.SampleSize)

	return result
}
