package loadgen

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsAlreadyKnownTx(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"b3 exact", errors.New("RPC error -32000: ALREADY_EXISTS: already known"), true},
		{"geth already known", errors.New("already known"), true},
		{"wrapped already_exists", fmt.Errorf("send failed: %w", errors.New("ALREADY_EXISTS")), true},
		{"mixed case", errors.New("Already Known"), true},
		{"nonce too low (not dup)", errors.New("nonce too low"), false},
		{"underpriced (not dup)", errors.New("replacement transaction underpriced"), false},
		{"context canceled (not dup)", context.Canceled, false},
		{"insufficient funds (not dup)", errors.New("insufficient funds for transfer"), false},
	}
	for _, c := range cases {
		if got := isAlreadyKnownTx(c.err); got != c.want {
			t.Errorf("%s: isAlreadyKnownTx(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
