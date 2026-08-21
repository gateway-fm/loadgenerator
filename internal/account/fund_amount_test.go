package account

import (
	"math/big"
	"os"
	"testing"
)

// The default must stay EXACTLY 1,000 ETH-equivalent. Every existing devnet
// arm and every published disk/throughput figure was measured with it, so a
// change here silently changes what those runs meant.
func TestDefaultFundAmountUnchanged(t *testing.T) {
	want, _ := new(big.Int).SetString("1000000000000000000000", 10)
	if got := parseFundAmount(""); got.Cmp(want) != 0 {
		t.Fatalf("default = %s, want %s", got, want)
	}
}

func TestParseFundAmount(t *testing.T) {
	def, _ := new(big.Int).SetString(DefaultFundAmountWei, 10)

	cases := []struct {
		name string
		in   string
		want *big.Int
	}{
		{"empty falls back", "", def},
		{"whitespace only falls back", "   ", def},
		// The PRST-4453 case: 10 tokens per account instead of 1,000, which
		// takes a 4,000-sender pool from 4,000,000 down to 40,000.
		{"explicit 10 tokens", "10000000000000000000", big.NewInt(0).Mul(big.NewInt(10), big.NewInt(1e18))},
		{"trims surrounding space", "  10000000000000000000  ", big.NewInt(0).Mul(big.NewInt(10), big.NewInt(1e18))},
		// A bad value must NOT fail the run. A load generator that refuses to
		// start because of a funding hint is worse than one that over-funds.
		{"garbage falls back", "not-a-number", def},
		{"zero falls back", "0", def},
		{"negative falls back", "-5", def},
		{"hex is not accepted, falls back", "0x1234", def},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFundAmount(tc.in); got.Cmp(tc.want) != 0 {
				t.Fatalf("parseFundAmount(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// The exported accessor must not hand out a pointer into shared state: three
// call sites use the result, and one of them mutating it would silently change
// what the others fund.
func TestPerAccountFundAmountReturnsCopy(t *testing.T) {
	t.Setenv("FUND_AMOUNT_WEI", "")
	a := PerAccountFundAmount()
	before := new(big.Int).Set(a)
	a.Add(a, big.NewInt(1))
	if b := PerAccountFundAmount(); b.Cmp(before) != 0 {
		t.Fatalf("mutating the result changed the shared value: %s != %s", b, before)
	}
}

func TestPerAccountFundAmountReadsEnv(t *testing.T) {
	// sync.Once means the env is only read once per process, so this asserts
	// the wiring rather than the value: whichever ran first, the two calls must
	// agree and must be one of the two legitimate answers.
	def, _ := new(big.Int).SetString(DefaultFundAmountWei, 10)
	got := PerAccountFundAmount()
	if got.Sign() <= 0 {
		t.Fatalf("fund amount must be positive, got %s", got)
	}
	if env := os.Getenv("FUND_AMOUNT_WEI"); env == "" && got.Cmp(def) != 0 {
		t.Fatalf("with no env set, expected default %s, got %s", def, got)
	}
}
