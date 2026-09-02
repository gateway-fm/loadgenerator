package loadgen

import (
	"errors"
	"testing"
)

// isNonceTooLow and isNonceError decide OPPOSITE handling for the two nonce failures —
// a too-high nonce was never consumed and must be recycled, a too-low one is spent and
// must be committed so the account moves past it. Getting either wrong is
// self-sustaining: recycling a spent nonce hands it straight back out and the resend
// fails identically (12,844 such rejections in 60s), while discarding a too-high nonce
// strands the account. They are pure string predicates over node error text, so pin them
// down (PRST-4262 review).
func TestIsNonceTooLow(t *testing.T) {
	tooLow := []string{
		"nonce too low: address 0xabc, tx: 1130 state: 1131",
		"Nonce too low",
		"invalid transaction: nonce has already been used",
	}
	notTooLow := []string{
		"nonce too high: address 0xabc, tx: 148 state: 69",
		"invalid nonce",
		"already known",
		"insufficient funds for gas * price + value",
		"connection refused",
		"",
	}

	for _, s := range tooLow {
		if !isNonceTooLow(errors.New(s)) {
			t.Errorf("isNonceTooLow(%q) = false, want true", s)
		}
	}
	for _, s := range notTooLow {
		if isNonceTooLow(errors.New(s)) {
			t.Errorf("isNonceTooLow(%q) = true, want false", s)
		}
	}
	if isNonceTooLow(nil) {
		t.Error("isNonceTooLow(nil) must be false")
	}
}

func TestIsNonceError(t *testing.T) {
	nonceErrors := []string{
		"nonce too low: tx: 1130 state: 1131",
		"nonce too high: tx: 148 state: 69",
		"invalid nonce",
		"nonce has already been used",
		"NONCE TOO HIGH",
	}
	others := []string{
		"insufficient funds for gas * price + value",
		"already known",
		"intrinsic gas too low",
		"connection refused",
		"context deadline exceeded",
		"",
	}

	for _, s := range nonceErrors {
		if !isNonceError(errors.New(s)) {
			t.Errorf("isNonceError(%q) = false, want true", s)
		}
	}
	for _, s := range others {
		if isNonceError(errors.New(s)) {
			t.Errorf("isNonceError(%q) = true, want false", s)
		}
	}
	if isNonceError(nil) {
		t.Error("isNonceError(nil) must be false")
	}
}

// Every too-low error must also register as a nonce error, since the generic predicate
// is what triggers the recovery resync.
func TestNonceTooLowImpliesNonceError(t *testing.T) {
	for _, s := range []string{
		"nonce too low: tx: 1130 state: 1131",
		"nonce has already been used",
	} {
		err := errors.New(s)
		if isNonceTooLow(err) && !isNonceError(err) {
			t.Errorf("%q is too-low but not classified as a nonce error, so no resync would fire", s)
		}
	}
}
