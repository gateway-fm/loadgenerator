package metrics

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// DiscardTx exists so a submission accounted for as failed cannot ALSO be counted as
// confirmed when its hash still lands on chain. That happens for "nonce too low", where
// the cause may be this transaction's own retried submit having already landed: the
// confirmation scan gates on GetTxSentTime, so leaving the tracker entry counts one
// transaction in both txFailed and txConfirmed and decrements pendingCount twice —
// corrupting the headline numbers and the adaptive controller's pending signal at once
// (PRST-4262 review).
func TestDiscardTx_PreventsLaterConfirmationBeingCounted(t *testing.T) {
	c := NewInMemoryCollector(true)
	hash := common.HexToHash("0xdeadbeef")

	c.RecordTxSent(hash, time.Now())
	if _, tracked := c.GetTxSentTime(hash); !tracked {
		t.Fatal("expected the tx to be tracked after RecordTxSent")
	}

	// The submission is accounted for as failed, then discarded.
	c.RecordTxFailed("send")
	c.DiscardTx(hash)

	// The confirmation scan's gate must now refuse it.
	if _, tracked := c.GetTxSentTime(hash); tracked {
		t.Error("tx still tracked after DiscardTx; a later block scan would count it as confirmed too")
	}

	// Simulate the hash appearing on chain anyway. Callers gate on GetTxSentTime, but
	// assert directly that the confirmed counter does not move even if it is called.
	before := c.GetTxConfirmed()
	c.RecordTxConfirmed(hash, time.Now())
	if after := c.GetTxConfirmed(); after != before {
		t.Errorf("txConfirmed moved from %d to %d for a discarded tx", before, after)
	}

	if got := c.GetTxFailed(); got != 1 {
		t.Errorf("txFailed = %d, want exactly 1 (not double counted)", got)
	}
}

func TestDiscardTx_IsIdempotentAndSafeForUnknownHash(t *testing.T) {
	c := NewInMemoryCollector(true)
	hash := common.HexToHash("0xabc123")

	// Never tracked: must not panic.
	c.DiscardTx(hash)

	c.RecordTxSent(hash, time.Now())
	c.DiscardTx(hash)
	c.DiscardTx(hash) // second call is a no-op

	if _, tracked := c.GetTxSentTime(hash); tracked {
		t.Error("tx still tracked after DiscardTx")
	}
}
