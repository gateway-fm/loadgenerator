package verification

import (
	"testing"

	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// TestCheckSenderTipOrdering verifies that tip ordering checks work correctly
// for SENDER-based ordering (not individual TX ordering).
func TestCheckSenderTipOrdering(t *testing.T) {
	v := &Verifier{}

	tests := []struct {
		name       string
		senderTips []uint64
		ordering   string
		expected   bool
	}{
		// tip_desc: senders should appear in descending order by max tip
		{
			name:       "tip_desc: correctly ordered (strict descending)",
			senderTips: []uint64{100, 80, 50, 20},
			ordering:   "tip_desc",
			expected:   true,
		},
		{
			name:       "tip_desc: correctly ordered (with equal tips)",
			senderTips: []uint64{100, 100, 50, 50},
			ordering:   "tip_desc",
			expected:   true,
		},
		{
			name:       "tip_desc: incorrectly ordered (ascending)",
			senderTips: []uint64{20, 50, 80, 100},
			ordering:   "tip_desc",
			expected:   false,
		},
		{
			name:       "tip_desc: single violation in middle",
			senderTips: []uint64{100, 50, 80, 20}, // 80 > 50 is violation
			ordering:   "tip_desc",
			expected:   false,
		},

		// tip_asc: senders should appear in ascending order by max tip
		{
			name:       "tip_asc: correctly ordered (strict ascending)",
			senderTips: []uint64{20, 50, 80, 100},
			ordering:   "tip_asc",
			expected:   true,
		},
		{
			name:       "tip_asc: correctly ordered (with equal tips)",
			senderTips: []uint64{20, 20, 50, 50},
			ordering:   "tip_asc",
			expected:   true,
		},
		{
			name:       "tip_asc: incorrectly ordered (descending)",
			senderTips: []uint64{100, 80, 50, 20},
			ordering:   "tip_asc",
			expected:   false,
		},

		// Edge cases
		{
			name:       "single sender (always valid)",
			senderTips: []uint64{100},
			ordering:   "tip_desc",
			expected:   true,
		},
		{
			name:       "empty sender list (always valid)",
			senderTips: []uint64{},
			ordering:   "tip_desc",
			expected:   true,
		},
		{
			name:       "all same tips (always valid for any ordering)",
			senderTips: []uint64{50, 50, 50, 50},
			ordering:   "tip_desc",
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := v.checkSenderTipOrdering(tt.senderTips, tt.ordering)
			if result != tt.expected {
				t.Errorf("checkSenderTipOrdering(%v, %q) = %v, want %v",
					tt.senderTips, tt.ordering, result, tt.expected)
			}
		})
	}
}

// TestExtractSenderTips verifies that sender tips are correctly extracted
// from a block, tracking max tip per sender and sender order.
func TestExtractSenderTips(t *testing.T) {
	// Use includeDepositTx: true since tests include deposit TXs at index 0
	v := NewVerifierWithConfig(nil, nil, true)

	tests := []struct {
		name               string
		transactions       []rpc.Transaction
		expectedMaxTips    map[string]uint64
		expectedSenderOrder []string
	}{
		{
			name: "multiple senders with varying tips",
			transactions: []rpc.Transaction{
				{From: "0xdeposit", Type: 126}, // deposit tx - should be skipped
				{From: "0xsenderA", MaxPriorityFeePerGas: 100},
				{From: "0xsenderA", MaxPriorityFeePerGas: 50}, // lower tip from same sender
				{From: "0xsenderB", MaxPriorityFeePerGas: 80},
				{From: "0xsenderC", MaxPriorityFeePerGas: 30},
			},
			expectedMaxTips: map[string]uint64{
				"0xsenderA": 100, // max of 100, 50
				"0xsenderB": 80,
				"0xsenderC": 30,
			},
			expectedSenderOrder: []string{"0xsenderA", "0xsenderB", "0xsenderC"},
		},
		{
			name: "sender order is by first appearance, not by tip",
			transactions: []rpc.Transaction{
				{From: "0xdeposit", Type: 126},
				{From: "0xsenderLowTip", MaxPriorityFeePerGas: 10},
				{From: "0xsenderHighTip", MaxPriorityFeePerGas: 100},
			},
			expectedMaxTips: map[string]uint64{
				"0xsenderLowTip":  10,
				"0xsenderHighTip": 100,
			},
			// Order is first-seen, not by tip value
			expectedSenderOrder: []string{"0xsenderLowTip", "0xsenderHighTip"},
		},
		{
			name: "legacy transactions use gasPrice as tip",
			transactions: []rpc.Transaction{
				{From: "0xdeposit", Type: 126},
				{From: "0xlegacy", MaxPriorityFeePerGas: 0, GasPrice: 50},
				{From: "0xeip1559", MaxPriorityFeePerGas: 30},
			},
			expectedMaxTips: map[string]uint64{
				"0xlegacy":  50, // uses GasPrice
				"0xeip1559": 30,
			},
			expectedSenderOrder: []string{"0xlegacy", "0xeip1559"},
		},
		{
			name: "only deposit transaction",
			transactions: []rpc.Transaction{
				{From: "0xdeposit", Type: 126},
			},
			expectedMaxTips:     map[string]uint64{},
			expectedSenderOrder: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &rpc.BlockFull{
				Transactions: tt.transactions,
				TxCount:      len(tt.transactions),
			}

			maxTips, senderOrder := v.extractSenderTips(block)

			// Check max tips
			if len(maxTips) != len(tt.expectedMaxTips) {
				t.Errorf("maxTips length = %d, want %d", len(maxTips), len(tt.expectedMaxTips))
			}
			for sender, expectedTip := range tt.expectedMaxTips {
				if actualTip, ok := maxTips[sender]; !ok {
					t.Errorf("missing sender %s in maxTips", sender)
				} else if actualTip != expectedTip {
					t.Errorf("maxTips[%s] = %d, want %d", sender, actualTip, expectedTip)
				}
			}

			// Check sender order
			if len(senderOrder) != len(tt.expectedSenderOrder) {
				t.Errorf("senderOrder length = %d, want %d", len(senderOrder), len(tt.expectedSenderOrder))
			}
			for i, expectedSender := range tt.expectedSenderOrder {
				if i < len(senderOrder) && senderOrder[i] != expectedSender {
					t.Errorf("senderOrder[%d] = %s, want %s", i, senderOrder[i], expectedSender)
				}
			}
		})
	}
}

// TestSenderBasedTipOrdering_RegressionPrevention is a regression test to ensure
// we never go back to per-TX ordering verification.
//
// The block builder orders SENDERS by max tip, not individual TXs.
// Within a sender, TXs must be in nonce order (Ethereum requirement).
//
// Example of CORRECT block structure:
//   Sender A (max tip = 100): tx1 (tip=100), tx2 (tip=50)
//   Sender B (max tip = 80):  tx1 (tip=80)
//   Sender C (max tip = 30):  tx1 (tip=30)
//
// Block order: [A.tx1, A.tx2, B.tx1, C.tx1] = tips [100, 50, 80, 30]
//
// Old (WRONG) verification: 50 < 80 would be flagged as violation
// New (CORRECT) verification: Sender order [100, 80, 30] is correctly descending
func TestSenderBasedTipOrdering_RegressionPrevention(t *testing.T) {
	// Use includeDepositTx: true since test includes deposit TX at index 0
	v := NewVerifierWithConfig(nil, nil, true)

	// This is the exact scenario that caused the bug:
	// Multiple TXs from same sender cause individual TX tips to be non-monotonic
	// even when SENDER ordering is correct.
	block := &rpc.BlockFull{
		Transactions: []rpc.Transaction{
			{From: "0xdeposit", Type: 126}, // Deposit TX (skipped)
			// Sender A - highest max tip (100), but second TX has lower tip (50)
			{From: "0xsenderA", MaxPriorityFeePerGas: 100, Nonce: 0},
			{From: "0xsenderA", MaxPriorityFeePerGas: 50, Nonce: 1},
			// Sender B - max tip 80 (less than 100, but more than 50)
			{From: "0xsenderB", MaxPriorityFeePerGas: 80, Nonce: 0},
			{From: "0xsenderB", MaxPriorityFeePerGas: 60, Nonce: 1},
			// Sender C - lowest max tip (30)
			{From: "0xsenderC", MaxPriorityFeePerGas: 30, Nonce: 0},
		},
		TxCount: 6,
	}

	// Extract sender tips
	senderMaxTips, senderOrder := v.extractSenderTips(block)

	// Verify sender order is correct (A, B, C - order of appearance)
	expectedOrder := []string{"0xsenderA", "0xsenderB", "0xsenderC"}
	if len(senderOrder) != len(expectedOrder) {
		t.Fatalf("senderOrder length = %d, want %d", len(senderOrder), len(expectedOrder))
	}
	for i, expected := range expectedOrder {
		if senderOrder[i] != expected {
			t.Errorf("senderOrder[%d] = %s, want %s", i, senderOrder[i], expected)
		}
	}

	// Verify max tips are correct
	if senderMaxTips["0xsenderA"] != 100 {
		t.Errorf("senderA max tip = %d, want 100", senderMaxTips["0xsenderA"])
	}
	if senderMaxTips["0xsenderB"] != 80 {
		t.Errorf("senderB max tip = %d, want 80", senderMaxTips["0xsenderB"])
	}
	if senderMaxTips["0xsenderC"] != 30 {
		t.Errorf("senderC max tip = %d, want 30", senderMaxTips["0xsenderC"])
	}

	// Build sender tips in order
	senderTips := make([]uint64, len(senderOrder))
	for i, sender := range senderOrder {
		senderTips[i] = senderMaxTips[sender]
	}

	// THIS IS THE KEY TEST: Sender-based ordering [100, 80, 30] should be valid for tip_desc
	// The old buggy code would have seen per-TX tips [100, 50, 80, 60, 30] and failed on 50->80
	isOrdered := v.checkSenderTipOrdering(senderTips, "tip_desc")
	if !isOrdered {
		t.Errorf("REGRESSION: Sender-based tip ordering [100, 80, 30] should be valid for tip_desc")
		t.Errorf("senderTips = %v", senderTips)
		t.Errorf("This was the bug: per-TX ordering would fail because 50 < 80")
	}
}

// TestFindSenderOrderingViolations tests that violations are correctly identified.
func TestFindSenderOrderingViolations(t *testing.T) {
	v := &Verifier{}

	tests := []struct {
		name              string
		senderTips        []uint64
		ordering          string
		expectedViolations int
	}{
		{
			name:              "no violations - perfect descending",
			senderTips:        []uint64{100, 80, 50, 20},
			ordering:          "tip_desc",
			expectedViolations: 0,
		},
		{
			name:              "one violation",
			senderTips:        []uint64{100, 50, 80, 20}, // 80 > 50
			ordering:          "tip_desc",
			expectedViolations: 1,
		},
		{
			name:              "multiple violations",
			senderTips:        []uint64{20, 50, 80, 100}, // all wrong for desc
			ordering:          "tip_desc",
			expectedViolations: 3,
		},
		{
			name:              "no violations for ascending",
			senderTips:        []uint64{20, 50, 80, 100},
			ordering:          "tip_asc",
			expectedViolations: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := v.findSenderOrderingViolations(12345, tt.senderTips, tt.ordering)
			if len(violations) != tt.expectedViolations {
				t.Errorf("got %d violations, want %d", len(violations), tt.expectedViolations)
				for _, v := range violations {
					t.Logf("  violation: index=%d expected=%d actual=%d",
						v.TxIndex, v.ExpectedTip, v.ActualTip)
				}
			}
		})
	}
}

// TestDepositTxConfiguration verifies that the includeDepositTx setting correctly
// controls whether deposit transactions at index 0 are skipped during verification.
func TestDepositTxConfiguration(t *testing.T) {
	// Block with deposit TX at index 0 followed by user TXs
	blockWithDeposit := &rpc.BlockFull{
		Transactions: []rpc.Transaction{
			{From: "0xdeposit", Type: 126, MaxPriorityFeePerGas: 0}, // deposit TX
			{From: "0xsenderA", Type: 2, MaxPriorityFeePerGas: 100},
			{From: "0xsenderB", Type: 2, MaxPriorityFeePerGas: 80},
		},
		TxCount: 3,
	}

	// Block without deposit TX (all user TXs)
	blockWithoutDeposit := &rpc.BlockFull{
		Transactions: []rpc.Transaction{
			{From: "0xsenderA", Type: 2, MaxPriorityFeePerGas: 100},
			{From: "0xsenderB", Type: 2, MaxPriorityFeePerGas: 80},
		},
		TxCount: 2,
	}

	t.Run("includeDepositTx=true skips deposit at index 0", func(t *testing.T) {
		v := NewVerifierWithConfig(nil, nil, true)
		maxTips, senderOrder := v.extractSenderTips(blockWithDeposit)

		// Should have 2 senders (deposit skipped)
		if len(senderOrder) != 2 {
			t.Errorf("senderOrder length = %d, want 2", len(senderOrder))
		}
		if len(maxTips) != 2 {
			t.Errorf("maxTips length = %d, want 2", len(maxTips))
		}
		// Verify deposit was skipped
		if _, ok := maxTips["0xdeposit"]; ok {
			t.Error("deposit sender should be skipped when includeDepositTx=true")
		}
	})

	t.Run("includeDepositTx=false processes all TXs including index 0", func(t *testing.T) {
		v := NewVerifierWithConfig(nil, nil, false)
		_, senderOrder := v.extractSenderTips(blockWithDeposit)

		// Should have 3 senders (deposit NOT skipped)
		if len(senderOrder) != 3 {
			t.Errorf("senderOrder length = %d, want 3 (deposit NOT skipped)", len(senderOrder))
		}
		// Verify deposit sender is first in order (not skipped)
		if len(senderOrder) > 0 && senderOrder[0] != "0xdeposit" {
			t.Errorf("first sender should be deposit when includeDepositTx=false, got %s", senderOrder[0])
		}
	})

	t.Run("includeDepositTx=false with no deposit block", func(t *testing.T) {
		v := NewVerifierWithConfig(nil, nil, false)
		maxTips, senderOrder := v.extractSenderTips(blockWithoutDeposit)

		// Should have 2 senders (no deposit to skip)
		if len(senderOrder) != 2 {
			t.Errorf("senderOrder length = %d, want 2", len(senderOrder))
		}
		if len(maxTips) != 2 {
			t.Errorf("maxTips length = %d, want 2", len(maxTips))
		}
	})

	t.Run("NewVerifier defaults to includeDepositTx=false", func(t *testing.T) {
		v := NewVerifier(nil, nil)
		maxTips, senderOrder := v.extractSenderTips(blockWithoutDeposit)

		// Default verifier with no-deposit block should process all TXs
		if len(senderOrder) != 2 {
			t.Errorf("senderOrder length = %d, want 2", len(senderOrder))
		}
		if len(maxTips) != 2 {
			t.Errorf("maxTips length = %d, want 2", len(maxTips))
		}
	})
}

// TestUserTxCountWithDepositConfig verifies that userTxCount is calculated correctly
// based on the includeDepositTx configuration.
func TestUserTxCountWithDepositConfig(t *testing.T) {
	block := &rpc.BlockFull{
		Number:   12345,
		TxCount:  5, // 1 deposit + 4 user TXs
		Transactions: []rpc.Transaction{
			{From: "0xdeposit", Type: 126},
			{From: "0xsenderA", Type: 2, MaxPriorityFeePerGas: 100},
			{From: "0xsenderA", Type: 2, MaxPriorityFeePerGas: 50},
			{From: "0xsenderB", Type: 2, MaxPriorityFeePerGas: 80},
			{From: "0xsenderC", Type: 2, MaxPriorityFeePerGas: 30},
		},
	}

	t.Run("includeDepositTx=true: userTxCount = TxCount - 1", func(t *testing.T) {
		// When deposits are enabled, we expect TxCount-1 user TXs
		v := NewVerifierWithConfig(nil, nil, true)

		// Calculate expected user count
		userTxCount := block.TxCount
		if v.includeDepositTx {
			userTxCount = block.TxCount - 1
		}

		if userTxCount != 4 {
			t.Errorf("userTxCount = %d, want 4 (5 - 1 deposit)", userTxCount)
		}
	})

	t.Run("includeDepositTx=false: userTxCount = TxCount", func(t *testing.T) {
		// When deposits are disabled, all TXs are user TXs
		v := NewVerifierWithConfig(nil, nil, false)

		userTxCount := block.TxCount
		if v.includeDepositTx {
			userTxCount = block.TxCount - 1
		}

		if userTxCount != 5 {
			t.Errorf("userTxCount = %d, want 5 (no deposit to exclude)", userTxCount)
		}
	})
}
