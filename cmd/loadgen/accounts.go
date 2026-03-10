package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/storage"
	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// tryWarmStartAccounts attempts to load cached accounts for warm start.
// Returns true if warm start succeeded (accounts are ready to use).
func (lg *LoadGenerator) tryWarmStartAccounts(dynamicCount int, chainID int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cached, err := lg.cacheStorage.LoadCachedAccounts(ctx, chainID)
	if err != nil {
		lg.logger.Warn("failed to load cached accounts", "error", err)
		return false
	}
	if len(cached) == 0 {
		lg.logger.Info("no cached accounts found, cold start required")
		return false
	}

	lg.logger.Info("loaded cached accounts, validating balances...",
		slog.Int("cached", len(cached)),
		slog.Int("needed", dynamicCount),
	)

	// Reconstruct Account objects from cached hex keys
	cachedAccounts := make([]*account.Account, 0, len(cached))
	for _, ca := range cached {
		acc, err := account.NewAccountFromHex(ca.PrivateKeyHex)
		if err != nil {
			lg.logger.Warn("invalid cached key, discarding cache", "error", err)
			lg.cacheStorage.DeleteCachedAccounts(ctx, chainID)
			return false
		}
		cachedAccounts = append(cachedAccounts, acc)
	}

	// Validate balances (1 ETH minimum)
	minBalance, _ := new(big.Int).SetString("1000000000000000000", 10) // 1 ETH
	lg.initPhase = types.InitPhaseGeneratingAccts
	lg.initProgress = "Validating cached account balances..."

	funded, unfunded := lg.accountMgr.ValidateBalances(ctx, lg.l2Client, cachedAccounts, minBalance)

	// If ALL accounts have zero balance, assume re-genesis
	if len(funded) == 0 {
		lg.logger.Info("all cached accounts have zero balance (re-genesis detected), wiping cache")
		lg.cacheStorage.DeleteCachedAccounts(ctx, chainID)
		lg.cacheStorage.DeleteCachedContracts(ctx, chainID)
		return false
	}

	lg.logger.Info("cached account balance validation complete",
		slog.Int("funded", len(funded)),
		slog.Int("unfunded", len(unfunded)),
		slog.Int("needed", dynamicCount),
	)

	// Determine which accounts to use
	var allAccounts []*account.Account
	needNew := 0

	if len(funded) >= dynamicCount {
		// Have enough funded accounts — use first N
		allAccounts = funded[:dynamicCount]
	} else {
		// Start with all funded accounts
		allAccounts = funded

		// Re-fund the unfunded subset
		if len(unfunded) > 0 {
			toRefund := unfunded
			if len(funded)+len(toRefund) > dynamicCount {
				toRefund = toRefund[:dynamicCount-len(funded)]
			}

			if len(toRefund) > 0 {
				lg.initPhase = types.InitPhaseFundingAccts
				lg.initProgress = fmt.Sprintf("Re-funding %d cached accounts...", len(toRefund))

				if err := lg.resetBuilderNonces(); err != nil {
					lg.logger.Warn("failed to reset builder nonces for re-funding", "error", err)
					return false
				}

				if err := lg.accountMgr.FundAccounts(ctx, lg.builderClient, lg.l2Client, toRefund); err != nil {
					lg.logger.Warn("failed to re-fund cached accounts", "error", err)
				} else {
					allAccounts = append(allAccounts, toRefund...)
				}
			}
		}

		// Generate + fund additional accounts if still short
		if len(allAccounts) < dynamicCount {
			needNew = dynamicCount - len(allAccounts)
			lg.initPhase = types.InitPhaseGeneratingAccts
			lg.initProgress = fmt.Sprintf("Generating %d additional accounts...", needNew)

			if err := lg.accountMgr.GenerateDynamicAccounts(needNew); err != nil {
				lg.logger.Warn("failed to generate additional accounts", "error", err)
				// Continue with what we have
			} else {
				newAccounts := lg.accountMgr.GetDynamicAccounts()

				lg.initPhase = types.InitPhaseFundingAccts
				lg.initProgress = fmt.Sprintf("Funding %d new accounts...", needNew)

				if err := lg.resetBuilderNonces(); err != nil {
					lg.logger.Warn("failed to reset builder nonces", "error", err)
				}

				if err := lg.accountMgr.FundDynamicAccounts(ctx, lg.builderClient, lg.l2Client); err != nil {
					lg.logger.Warn("failed to fund new accounts", "error", err)
				}

				allAccounts = append(allAccounts, newAccounts...)
			}
		}
	}

	// Set the accounts on the manager
	lg.accountMgr.SetDynamicAccounts(allAccounts)
	lg.initAccountsGen = len(allAccounts)
	lg.initFundingSent = len(allAccounts)

	// Init nonces for cached accounts
	lg.initPhase = types.InitPhaseInitNonces
	lg.initProgress = "Initializing nonces for cached accounts..."
	if err := lg.accountMgr.InitializeDynamicNonces(ctx, lg.builderClient); err != nil {
		lg.logger.Warn("failed to initialize cached account nonces", "error", err)
	}

	// Persist any newly generated accounts back to cache
	if needNew > 0 {
		lg.saveDynamicAccountsToCache(chainID)
	}

	lg.logger.Info("warm start complete",
		slog.Int("totalAccounts", len(allAccounts)),
		slog.Int("fromCache", len(allAccounts)-needNew),
		slog.Int("newlyGenerated", needNew),
	)
	return true
}

// saveDynamicAccountsToCache persists current dynamic accounts to the cache.
func (lg *LoadGenerator) saveDynamicAccountsToCache(chainID int64) {
	if lg.cacheStorage == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pairs := lg.accountMgr.ExportDynamicAccountKeys()
	cached := make([]storage.CachedAccount, len(pairs))
	now := time.Now()
	for i, p := range pairs {
		cached[i] = storage.CachedAccount{
			Address:       p.Address,
			PrivateKeyHex: p.PrivateKeyHex,
			ChainID:       chainID,
			CreatedAt:     now,
		}
	}

	if err := lg.cacheStorage.SaveCachedAccounts(ctx, cached); err != nil {
		lg.logger.Warn("failed to save accounts to cache", "error", err)
	} else {
		lg.logger.Info("saved accounts to cache", slog.Int("count", len(cached)))
	}
}