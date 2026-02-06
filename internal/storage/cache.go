package storage

import "context"

// CacheStorage provides persistence for reusable test accounts and contracts.
// Scoped by chain ID to support multiple chains without collision.
type CacheStorage interface {
	SaveCachedAccounts(ctx context.Context, accounts []CachedAccount) error
	LoadCachedAccounts(ctx context.Context, chainID int64) ([]CachedAccount, error)
	DeleteCachedAccounts(ctx context.Context, chainID int64) error
	MarkAccountsUniswapReady(ctx context.Context, chainID int64, addresses []string) error

	SaveCachedContract(ctx context.Context, contract CachedContract) error
	LoadCachedContracts(ctx context.Context, chainID int64) ([]CachedContract, error)
	DeleteCachedContracts(ctx context.Context, chainID int64) error
}
