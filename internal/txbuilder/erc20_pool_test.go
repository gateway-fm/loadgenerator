package txbuilder

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// decodeRecipient pulls the recipient back out of a built transfer's calldata.
// Layout is selector(4) || address left-padded to 32 || amount(32).
func decodeRecipient(t *testing.T, data []byte) common.Address {
	t.Helper()
	if len(data) != 4+32+32 {
		t.Fatalf("unexpected calldata length %d", len(data))
	}
	var a common.Address
	copy(a[:], data[4+12:4+32])
	return a
}

func buildRecipient(t *testing.T, b *ERC20TransferBuilder) common.Address {
	t.Helper()
	tx, err := b.Build(TxParams{ChainID: big.NewInt(412346), Nonce: 0})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return decodeRecipient(t, tx.Data())
}

// A bounded pool must never yield an address outside the pool, and over many
// draws must cover it. This is the property the holder:transfer ratio rests on.
func TestERC20RecipientPool_CardinalityBoundedByN(t *testing.T) {
	const (
		n     = 64
		draws = 20000
	)
	b := newERC20TransferBuilderWithPool(n, "test-seed")

	expected := make(map[common.Address]bool, n)
	for i := uint64(0); i < n; i++ {
		expected[b.poolAddress(i)] = true
	}

	seen := make(map[common.Address]bool)
	for i := 0; i < draws; i++ {
		addr := buildRecipient(t, b)
		if !expected[addr] {
			t.Fatalf("draw %d produced %s, which is outside the pool", i, addr.Hex())
		}
		seen[addr] = true
	}

	if len(seen) > n {
		t.Fatalf("cardinality %d exceeds pool size %d", len(seen), n)
	}
	// 20000 draws over 64 slots: missing any is astronomically unlikely.
	if len(seen) != n {
		t.Errorf("expected all %d pool addresses over %d draws, saw %d", n, draws, len(seen))
	}
}

// The same seed must reproduce the same holder set, or a run definition is not
// replayable.
func TestERC20RecipientPool_SameSeedSameSet(t *testing.T) {
	const n = 256

	a := newERC20TransferBuilderWithPool(n, "seed-alpha")
	b := newERC20TransferBuilderWithPool(n, "seed-alpha")
	c := newERC20TransferBuilderWithPool(n, "seed-beta")

	for i := uint64(0); i < n; i++ {
		if a.poolAddress(i) != b.poolAddress(i) {
			t.Fatalf("index %d: same seed produced different addresses %s vs %s",
				i, a.poolAddress(i).Hex(), b.poolAddress(i).Hex())
		}
		if a.poolAddress(i) == c.poolAddress(i) {
			t.Fatalf("index %d: different seeds collided on %s", i, a.poolAddress(i).Hex())
		}
	}
}

// Derivation must be exactly sha256(seed || be64(i))[:20] -- pinned so a future
// refactor cannot silently change the holder set an experiment was run against.
func TestERC20RecipientPool_DerivationIsPinned(t *testing.T) {
	const seed = "pin-me"
	b := newERC20TransferBuilderWithPool(8, seed)

	for _, i := range []uint64{0, 1, 7, 1 << 20} {
		var idx [8]byte
		binary.BigEndian.PutUint64(idx[:], i)
		sum := sha256.Sum256(append([]byte(seed), idx[:]...))

		var want common.Address
		copy(want[:], sum[:common.AddressLength])

		if got := b.poolAddress(i); got != want {
			t.Errorf("poolAddress(%d) = %s, want %s", i, got.Hex(), want.Hex())
		}
	}
}

// N=0 must preserve today's behaviour exactly: a fresh random address every
// time. Existing load-test work depends on this being the default.
func TestERC20RecipientPool_ZeroKeepsRandomBehaviour(t *testing.T) {
	const draws = 2000
	b := newERC20TransferBuilderWithPool(0, "irrelevant")

	seen := make(map[common.Address]bool, draws)
	for i := 0; i < draws; i++ {
		seen[buildRecipient(t, b)] = true
	}
	if len(seen) != draws {
		t.Errorf("expected %d distinct random recipients, got %d", draws, len(seen))
	}
}

// The zero value reached through the public constructor with no env set must
// also be the random path.
func TestERC20RecipientPool_DefaultIsRandom(t *testing.T) {
	t.Setenv(EnvERC20RecipientPool, "")
	b := NewERC20TransferBuilder(common.Address{})
	if b.poolSize != 0 {
		t.Fatalf("default poolSize = %d, want 0 (random)", b.poolSize)
	}
}

// A malformed pool size must be REJECTED, never quietly treated as 0. 0 selects
// the unbounded-random workload, so a silent fallback reports all-random gas and
// tx/s under whatever pool label the operator believed was active.
func TestParseERC20RecipientPool_RejectsMalformed(t *testing.T) {
	for _, val := range []string{
		"", "0", "-5", "-1", "garbage",
		"1_000_000", // the Go-literal form, as used in this file's benchmarks
		"1e6", "1M", "100000O", " 1000000", "1000000 ", "1.0",
		"99999999999999999999999999", // outside int64
	} {
		t.Run("reject/"+val, func(t *testing.T) {
			if got, err := ParseERC20RecipientPool(val); err == nil {
				t.Errorf("ParseERC20RecipientPool(%q) = %d, want an error", val, got)
			}
		})
	}
}

func TestParseERC20RecipientPool_AcceptsPositive(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want uint64
	}{
		{"1", 1}, {"64", 64}, {"1000000", 1000000},
	} {
		t.Run("accept/"+tc.val, func(t *testing.T) {
			got, err := ParseERC20RecipientPool(tc.val)
			if err != nil {
				t.Fatalf("ParseERC20RecipientPool(%q): %v", tc.val, err)
			}
			if got != tc.want {
				t.Errorf("ParseERC20RecipientPool(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

// Unset is the one input that legitimately yields 0, and it must not error.
func TestERC20RecipientPoolFromEnv_UnsetIsRandomWithoutError(t *testing.T) {
	t.Setenv(EnvERC20RecipientPool, "")
	t.Setenv(EnvERC20RecipientPoolSeed, "")

	poolSize, seed, err := ERC20RecipientPoolFromEnv()
	if err != nil {
		t.Fatalf("unset pool returned an error: %v", err)
	}
	if poolSize != 0 {
		t.Errorf("poolSize = %d, want 0", poolSize)
	}
	if seed != defaultERC20RecipientPoolSeed {
		t.Errorf("seed = %q, want %q", seed, defaultERC20RecipientPoolSeed)
	}
}

func TestERC20RecipientPoolFromEnv_MalformedIsAnError(t *testing.T) {
	t.Setenv(EnvERC20RecipientPool, "1_000_000")
	if _, _, err := ERC20RecipientPoolFromEnv(); err == nil {
		t.Fatal("malformed ERC20_RECIPIENT_POOL returned no error")
	}
}

func TestERC20RecipientPoolFromEnv_SeedOverride(t *testing.T) {
	t.Setenv(EnvERC20RecipientPoolSeed, "")
	if _, seed, _ := ERC20RecipientPoolFromEnv(); seed != defaultERC20RecipientPoolSeed {
		t.Errorf("default seed = %q, want %q", seed, defaultERC20RecipientPoolSeed)
	}
	t.Setenv(EnvERC20RecipientPoolSeed, "custom")
	if _, seed, _ := ERC20RecipientPoolFromEnv(); seed != "custom" {
		t.Errorf("overridden seed = %q, want %q", seed, "custom")
	}
}

// The positive direction: if the public constructor stopped reading the
// environment, every other test in this file would still pass against the
// unexported constructor while the feature was entirely inert.
func TestERC20RecipientPool_PublicConstructorReadsEnv(t *testing.T) {
	t.Setenv(EnvERC20RecipientPool, "1024")
	t.Setenv(EnvERC20RecipientPoolSeed, "constructor-seed")

	b := NewERC20TransferBuilder(common.Address{})
	if b.configErr != nil {
		t.Fatalf("configErr = %v, want nil", b.configErr)
	}
	if b.poolSize != 1024 {
		t.Errorf("poolSize = %d, want 1024 - the constructor is not reading %s",
			b.poolSize, EnvERC20RecipientPool)
	}
	if string(b.poolSeed) != "constructor-seed" {
		t.Errorf("poolSeed = %q, want %q - the constructor is not reading %s",
			b.poolSeed, "constructor-seed", EnvERC20RecipientPoolSeed)
	}

	addr := buildRecipient(t, b)
	if addr != b.poolAddress(indexOf(t, b, addr)) {
		t.Errorf("built recipient %s is not a pool address", addr.Hex())
	}
}

// indexOf finds which pool index produced addr, failing if none did.
func indexOf(t *testing.T, b *ERC20TransferBuilder, addr common.Address) uint64 {
	t.Helper()
	for i := uint64(0); i < b.poolSize; i++ {
		if b.poolAddress(i) == addr {
			return i
		}
	}
	t.Fatalf("recipient %s is outside the pool of %d", addr.Hex(), b.poolSize)
	return 0
}

// A malformed value must not be able to reach a transaction at all.
func TestERC20RecipientPool_MalformedEnvFailsBuild(t *testing.T) {
	t.Setenv(EnvERC20RecipientPool, "1_000_000")

	b := NewERC20TransferBuilder(common.Address{})
	if b.configErr == nil {
		t.Fatal("configErr = nil, want the parse error")
	}
	if _, err := b.Build(TxParams{ChainID: big.NewInt(412346), Nonce: 0}); err == nil {
		t.Fatal("Build succeeded with a malformed ERC20_RECIPIENT_POOL")
	}
}

// Index selection sits on the hot path at 500+ tx/s. These two benchmarks are
// the evidence for choosing math/rand/v2 over crypto/rand -- run them rather
// than trusting the choice.
func BenchmarkRecipient_PooledMathRandV2(b *testing.B) {
	bl := newERC20TransferBuilderWithPool(1_000_000, defaultERC20RecipientPoolSeed)
	for b.Loop() {
		if _, err := bl.recipient(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecipient_RandomCryptoRand(b *testing.B) {
	bl := newERC20TransferBuilderWithPool(0, "")
	for b.Loop() {
		if _, err := bl.recipient(); err != nil {
			b.Fatal(err)
		}
	}
}

// Concurrent Build is how the senders actually call this; with math/rand/v2's
// top-level generator it must be race-free. Run with -race.
func TestERC20RecipientPool_ConcurrentBuildIsSafe(t *testing.T) {
	b := newERC20TransferBuilderWithPool(1024, "concurrent")
	chainID := big.NewInt(412346)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 500; i++ {
				tx, err := b.Build(TxParams{ChainID: chainID, Nonce: uint64(g*500 + i)})
				if err != nil {
					t.Error(err)
					return
				}
				if len(tx.Data()) != 4+32+32 {
					t.Errorf("calldata length %d", len(tx.Data()))
					return
				}
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}
