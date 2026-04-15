// Package noncehealer detects and fills nonce gaps during load tests.
// It polls the block builder's txpool_content for queued transactions,
// compares against on-chain nonces, and sends no-op self-transfers
// to fill any gaps — unblocking queued transactions.
package noncehealer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// Config configures the nonce gap healer.
type Config struct {
	BuilderClient rpc.Client         // For txpool_content + sending gap-fill TXs
	L2Client      rpc.Client         // For on-chain nonce lookups
	Accounts      []*account.Account // All accounts (built-in + dynamic)
	ChainID       *big.Int
	GasTipCap     *big.Int
	GasFeeCap     *big.Int
	Logger        *slog.Logger
	PollInterval  time.Duration // How often to check for gaps (default: 2s)
	MaxGapSize    int           // Skip accounts with gaps larger than this (default: 2000)
	BatchSize     int           // How many gap-fill TXs to send per batch (default: 200)
}

// Healer monitors the builder txpool and fills nonce gaps with no-op TXs.
type Healer struct {
	cfg      Config
	addrToAccount map[common.Address]*account.Account
	signer   types.Signer
	totalHealed uint64
}

// New creates a new Healer.
func New(cfg Config) *Healer {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.MaxGapSize == 0 {
		cfg.MaxGapSize = 2000
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 200
	}

	// Build address -> account lookup
	lookup := make(map[common.Address]*account.Account, len(cfg.Accounts))
	for _, acc := range cfg.Accounts {
		lookup[acc.Address] = acc
	}

	return &Healer{
		cfg:           cfg,
		addrToAccount: lookup,
		signer:        types.LatestSignerForChainID(cfg.ChainID),
	}
}

// TotalHealed returns the number of gap-fill TXs sent so far.
func (h *Healer) TotalHealed() uint64 {
	return h.totalHealed
}

// Run starts the healer loop. Blocks until ctx is cancelled.
func (h *Healer) Run(ctx context.Context) {
	h.cfg.Logger.Info("nonce gap healer started",
		"accounts", len(h.addrToAccount),
		"pollInterval", h.cfg.PollInterval,
		"maxGapSize", h.cfg.MaxGapSize)

	ticker := time.NewTicker(h.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.cfg.Logger.Info("nonce gap healer stopped", "totalHealed", h.totalHealed)
			return
		case <-ticker.C:
			h.healOnce(ctx)
		}
	}
}

// txpoolContent represents the result of txpool_content.
type txpoolContent struct {
	Queued map[string]map[string]json.RawMessage `json:"queued"`
}

// gapInfo describes a nonce gap for one account.
type gapInfo struct {
	addr       common.Address
	acc        *account.Account
	onChain    uint64
	lowestQ    uint64
	gapSize    int
}

func (h *Healer) healOnce(ctx context.Context) {
	gaps := h.detectGaps(ctx)
	if len(gaps) == 0 {
		return
	}

	totalGap := 0
	for _, g := range gaps {
		totalGap += g.gapSize
	}
	h.cfg.Logger.Info("nonce gaps detected",
		"accounts", len(gaps),
		"totalMissing", totalGap)

	for _, g := range gaps {
		if ctx.Err() != nil {
			return
		}
		if g.gapSize > h.cfg.MaxGapSize {
			h.cfg.Logger.Warn("skipping large gap",
				"address", g.addr.Hex()[:10],
				"gap", g.gapSize)
			continue
		}
		sent := h.fillGap(ctx, g)
		if sent > 0 {
			h.totalHealed += uint64(sent)
			h.cfg.Logger.Info("gap filled",
				"address", g.addr.Hex()[:10],
				"gap", g.gapSize,
				"sent", sent,
				"totalHealed", h.totalHealed)
		}
	}
}

func (h *Healer) detectGaps(ctx context.Context) []gapInfo {
	// Call txpool_content on the builder
	raw, err := h.cfg.BuilderClient.Call(ctx, "txpool_content", nil)
	if err != nil {
		// txpool_content may not be supported — log once and return
		if !strings.Contains(err.Error(), "already logged") {
			h.cfg.Logger.Debug("txpool_content call failed", "error", err)
		}
		return nil
	}

	var content txpoolContent
	if err := json.Unmarshal(raw, &content); err != nil {
		h.cfg.Logger.Debug("failed to parse txpool_content", "error", err)
		return nil
	}

	if len(content.Queued) == 0 {
		return nil
	}

	// Find accounts we control that have queued TXs
	type candidate struct {
		addr    common.Address
		acc     *account.Account
		lowestQ uint64
	}
	var candidates []candidate

	for addrHex, nonces := range content.Queued {
		addr := common.HexToAddress(addrHex)
		acc, known := h.addrToAccount[addr]
		if !known {
			continue
		}
		// Find lowest queued nonce
		var lowest uint64
		first := true
		for nonceStr := range nonces {
			n, err := strconv.ParseUint(nonceStr, 10, 64)
			if err != nil {
				continue
			}
			if first || n < lowest {
				lowest = n
				first = false
			}
		}
		if first {
			continue
		}
		candidates = append(candidates, candidate{addr: addr, acc: acc, lowestQ: lowest})
	}

	if len(candidates) == 0 {
		return nil
	}

	// Batch-fetch on-chain nonces
	var gaps []gapInfo
	for _, c := range candidates {
		if ctx.Err() != nil {
			return nil
		}
		onChain, err := h.cfg.L2Client.GetConfirmedNonce(ctx, c.addr.Hex())
		if err != nil {
			continue
		}
		if c.lowestQ > onChain {
			gap := int(c.lowestQ - onChain)
			gaps = append(gaps, gapInfo{
				addr:    c.addr,
				acc:     c.acc,
				onChain: onChain,
				lowestQ: c.lowestQ,
				gapSize: gap,
			})
		}
	}

	return gaps
}

func (h *Healer) fillGap(ctx context.Context, g gapInfo) int {
	count := g.gapSize
	if count > h.cfg.MaxGapSize {
		count = h.cfg.MaxGapSize
	}

	// Sign all gap-fill TXs
	var rawTXs [][]byte
	for i := 0; i < count; i++ {
		nonce := g.onChain + uint64(i)
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   h.cfg.ChainID,
			Nonce:     nonce,
			GasTipCap: h.cfg.GasTipCap,
			GasFeeCap: h.cfg.GasFeeCap,
			Gas:       21000,
			To:        &g.addr, // self-transfer
			Value:     big.NewInt(0),
		})

		signed, err := types.SignTx(tx, h.signer, g.acc.PrivateKey)
		if err != nil {
			h.cfg.Logger.Warn("failed to sign gap-fill TX",
				"address", g.addr.Hex()[:10],
				"nonce", nonce,
				"error", err)
			continue
		}

		rlp, err := signed.MarshalBinary()
		if err != nil {
			continue
		}
		rawTXs = append(rawTXs, rlp)
	}

	if len(rawTXs) == 0 {
		return 0
	}

	// Send in batches
	sent := 0
	for i := 0; i < len(rawTXs); i += h.cfg.BatchSize {
		end := i + h.cfg.BatchSize
		if end > len(rawTXs) {
			end = len(rawTXs)
		}
		batch := rawTXs[i:end]

		errs := h.cfg.BuilderClient.SendRawTransactionBatch(ctx, batch)
		for _, err := range errs {
			if err == nil {
				sent++
			}
		}
	}

	return sent
}

// FormatAddress returns a short address for logging.
func FormatAddress(addr common.Address) string {
	hex := addr.Hex()
	if len(hex) > 10 {
		return hex[:10]
	}
	return hex
}

// Ensure crypto import is used (needed for account.PrivateKey type).
var _ = crypto.PubkeyToAddress
var _ = fmt.Sprintf
