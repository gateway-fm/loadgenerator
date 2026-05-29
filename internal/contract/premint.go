package contract

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/txbuilder"
)

// preMintBatchSize is how many signed transactions are sent per HTTP request.
const preMintBatchSize = 200

// PreMintProgress reports progress through the pre-mint phase.
// minted = mints successfully queued; total = requested count.
type PreMintProgress func(minted, total int)

// PreMintNFTs mints `count` ERC-721 tokens from `minter` against `nftAddr`.
// Token IDs are sequential 0..count-1; each goes to a random recipient.
//
// All transactions are signed locally with sequential nonces and dispatched
// via SendRawTransactionBatch in chunks. After the last batch the method
// polls eth_getTransactionCount until the deployer's on-chain nonce reaches
// startNonce+count (or ctx/deadline expires).
func (d *Deployer) PreMintNFTs(ctx context.Context, minter *account.Account, nftAddr common.Address, count int, onProgress PreMintProgress) error {
	if count <= 0 {
		return nil
	}

	startNonce, err := d.client.GetNonce(ctx, minter.Address.Hex())
	if err != nil {
		return fmt.Errorf("fetch starting nonce: %w", err)
	}

	signer := types.LatestSignerForChainID(d.chainID)
	queued := 0

	for queued < count {
		batchN := min(count-queued, preMintBatchSize)

		rlps := make([][]byte, 0, batchN)
		for i := range batchN {
			nonce := startNonce + uint64(queued+i)
			tokenID := big.NewInt(int64(queued + i))

			var recipient common.Address
			rand.Read(recipient[:])

			tx := txbuilder.BuildMintTx(d.chainID, nonce, nftAddr, recipient, tokenID, big.NewInt(0), d.gasPrice, d.useLegacy)
			signed, err := types.SignTx(tx, signer, minter.PrivateKey)
			if err != nil {
				return fmt.Errorf("sign mint tx (nonce %d): %w", nonce, err)
			}
			rlp, err := signed.MarshalBinary()
			if err != nil {
				return fmt.Errorf("marshal mint tx (nonce %d): %w", nonce, err)
			}
			rlps = append(rlps, rlp)
		}

		sendErrs := d.client.SendRawTransactionBatch(ctx, rlps)
		for i, sendErr := range sendErrs {
			if sendErr != nil {
				return fmt.Errorf("send mint tx (nonce %d): %w", startNonce+uint64(queued+i), sendErr)
			}
		}

		queued += batchN
		if onProgress != nil {
			onProgress(queued, count)
		}
		d.logger.Info("pre-mint batch sent",
			slog.Int("batch", batchN),
			slog.Int("queued", queued),
			slog.Int("total", count),
		)
	}

	// Wait for the deployer's nonce to advance to startNonce + count.
	targetNonce := startNonce + uint64(count)
	deadline := time.Now().Add(2 * time.Minute)
	backoff := 200 * time.Millisecond
	maxBackoff := 2 * time.Second

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		n, err := d.client.GetNonce(ctx, minter.Address.Hex())
		if err == nil && n >= targetNonce {
			d.logger.Info("pre-mint complete", slog.Int("count", count), slog.Uint64("startNonce", startNonce), slog.Uint64("endNonce", n))
			return nil
		}

		backoff = min(backoff*2, maxBackoff)
	}

	return fmt.Errorf("timeout waiting for pre-mint confirmation (target nonce %d)", targetNonce)
}
