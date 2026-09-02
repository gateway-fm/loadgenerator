package transport

import (
	"strings"
	"testing"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestValidateStartRequest_Constant(t *testing.T) {
	tests := []struct {
		name    string
		req     types.StartTestRequest
		wantErr string // Empty string = no error expected
	}{
		{
			name: "valid constant pattern",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  60,
				ConstantRate: 100,
			},
			wantErr: "",
		},
		{
			name: "invalid pattern",
			req: types.StartTestRequest{
				Pattern:     "invalid",
				DurationSec: 60,
			},
			wantErr: "invalid pattern",
		},
		{
			name: "zero duration",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  0,
				ConstantRate: 100,
			},
			wantErr: "durationSec must be positive",
		},
		{
			name: "negative duration",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  -1,
				ConstantRate: 100,
			},
			wantErr: "durationSec must be positive",
		},
		{
			name: "duration exceeds max",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  90000, // 25 hours, max is 24 hours
				ConstantRate: 100,
			},
			wantErr: "durationSec exceeds maximum",
		},
		{
			name: "negative numAccounts",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  60,
				ConstantRate: 100,
				NumAccounts:  -1,
			},
			wantErr: "numAccounts cannot be negative",
		},
		{
			name: "numAccounts exceeds max",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  60,
				ConstantRate: 100,
				NumAccounts:  200000,
			},
			wantErr: "numAccounts exceeds maximum",
		},
		{
			name: "invalid transaction type",
			req: types.StartTestRequest{
				Pattern:         types.PatternConstant,
				DurationSec:     60,
				ConstantRate:    100,
				TransactionType: "invalid_tx_type",
			},
			wantErr: "invalid transactionType",
		},
		{
			name: "zero constantRate",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  60,
				ConstantRate: 0,
			},
			wantErr: "constantRate must be positive",
		},
		{
			name: "constantRate exceeds max",
			req: types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  60,
				ConstantRate: 200000,
			},
			wantErr: "constantRate exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStartRequest(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateStartRequest() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateStartRequest() expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateStartRequest() error = %q, want error containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestValidateStartRequest_Ramp(t *testing.T) {
	tests := []struct {
		name    string
		req     types.StartTestRequest
		wantErr string
	}{
		{
			name: "valid ramp pattern",
			req: types.StartTestRequest{
				Pattern:     types.PatternRamp,
				DurationSec: 60,
				RampStart:   10,
				RampEnd:     100,
				RampSteps:   10,
			},
			wantErr: "",
		},
		{
			name: "negative rampStart",
			req: types.StartTestRequest{
				Pattern:     types.PatternRamp,
				DurationSec: 60,
				RampStart:   -1,
				RampEnd:     100,
				RampSteps:   10,
			},
			wantErr: "rampStart cannot be negative",
		},
		{
			name: "zero rampEnd",
			req: types.StartTestRequest{
				Pattern:     types.PatternRamp,
				DurationSec: 60,
				RampStart:   10,
				RampEnd:     0,
				RampSteps:   10,
			},
			wantErr: "rampEnd must be positive",
		},
		{
			name: "rampEnd exceeds max",
			req: types.StartTestRequest{
				Pattern:     types.PatternRamp,
				DurationSec: 60,
				RampStart:   10,
				RampEnd:     200000,
				RampSteps:   10,
			},
			wantErr: "rampEnd exceeds maximum",
		},
		{
			name: "zero rampSteps",
			req: types.StartTestRequest{
				Pattern:     types.PatternRamp,
				DurationSec: 60,
				RampStart:   10,
				RampEnd:     100,
				RampSteps:   0,
			},
			wantErr: "rampSteps must be positive",
		},
		{
			name: "rampSteps exceeds max",
			req: types.StartTestRequest{
				Pattern:     types.PatternRamp,
				DurationSec: 60,
				RampStart:   10,
				RampEnd:     100,
				RampSteps:   2000,
			},
			wantErr: "rampSteps exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStartRequest(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateStartRequest() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateStartRequest() expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateStartRequest() error = %q, want error containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestValidateStartRequest_Spike(t *testing.T) {
	tests := []struct {
		name    string
		req     types.StartTestRequest
		wantErr string
	}{
		{
			name: "valid spike pattern",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     200,
				SpikeDuration: 10,
				SpikeInterval: 30,
			},
			wantErr: "",
		},
		{
			name: "negative baselineRate",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  -1,
				SpikeRate:     200,
				SpikeDuration: 10,
				SpikeInterval: 30,
			},
			wantErr: "baselineRate cannot be negative",
		},
		{
			name: "zero spikeRate",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     0,
				SpikeDuration: 10,
				SpikeInterval: 30,
			},
			wantErr: "spikeRate must be positive",
		},
		{
			name: "spikeRate exceeds max",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     200000,
				SpikeDuration: 10,
				SpikeInterval: 30,
			},
			wantErr: "spikeRate exceeds maximum",
		},
		{
			name: "zero spikeDuration",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     200,
				SpikeDuration: 0,
				SpikeInterval: 30,
			},
			wantErr: "spikeDuration must be positive",
		},
		{
			name: "spikeDuration exceeds max",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     200,
				SpikeDuration: 5000,
				SpikeInterval: 30,
			},
			wantErr: "spikeDuration exceeds maximum",
		},
		{
			name: "zero spikeInterval",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     200,
				SpikeDuration: 10,
				SpikeInterval: 0,
			},
			wantErr: "spikeInterval must be positive",
		},
		{
			name: "spikeInterval exceeds max",
			req: types.StartTestRequest{
				Pattern:       types.PatternSpike,
				DurationSec:   60,
				BaselineRate:  50,
				SpikeRate:     200,
				SpikeDuration: 10,
				SpikeInterval: 5000,
			},
			wantErr: "spikeInterval exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStartRequest(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateStartRequest() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateStartRequest() expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateStartRequest() error = %q, want error containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestValidateStartRequest_Adaptive(t *testing.T) {
	tests := []struct {
		name    string
		req     types.StartTestRequest
		wantErr string
	}{
		{
			name: "valid adaptive pattern",
			req: types.StartTestRequest{
				Pattern:             types.PatternAdaptive,
				DurationSec:         60,
				AdaptiveInitialRate: 100,
			},
			wantErr: "",
		},
		{
			name: "adaptive with zero initial rate (valid)",
			req: types.StartTestRequest{
				Pattern:             types.PatternAdaptive,
				DurationSec:         60,
				AdaptiveInitialRate: 0,
			},
			wantErr: "",
		},
		{
			name: "negative adaptiveInitialRate",
			req: types.StartTestRequest{
				Pattern:             types.PatternAdaptive,
				DurationSec:         60,
				AdaptiveInitialRate: -1,
			},
			wantErr: "adaptiveInitialRate cannot be negative",
		},
		{
			name: "adaptiveInitialRate exceeds max",
			req: types.StartTestRequest{
				Pattern:             types.PatternAdaptive,
				DurationSec:         60,
				AdaptiveInitialRate: 200000,
			},
			wantErr: "adaptiveInitialRate exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStartRequest(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateStartRequest() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateStartRequest() expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateStartRequest() error = %q, want error containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// Read load is opt-in. An absent block must validate exactly as before, so every
// pre-existing write-only arm keeps behaving identically (PRST-4293).
func TestValidateStartRequest_ReadLoad(t *testing.T) {
	validMix := types.ReadMix{
		EthCall: 60, GetBalance: 15, GetLogs: 10, GetBlockByNumber: 10, GetReceipt: 5,
	}

	tests := []struct {
		name     string
		readLoad *types.ReadLoadConfig
		wantErr  string
	}{
		{"absent read load is valid", nil, ""},
		{
			"disabled read load skips validation",
			&types.ReadLoadConfig{Enabled: false},
			"",
		},
		{
			"valid read load",
			&types.ReadLoadConfig{Enabled: true, TargetRPS: 3000, Mix: validMix},
			"",
		},
		{
			"mix must sum to 100",
			&types.ReadLoadConfig{Enabled: true, TargetRPS: 3000, Mix: types.ReadMix{EthCall: 50}},
			"must sum to 100",
		},
		{
			"targetRps must be positive",
			&types.ReadLoadConfig{Enabled: true, TargetRPS: 0, Mix: validMix},
			"targetRps must be positive",
		},
		{
			"invalid block selection",
			&types.ReadLoadConfig{Enabled: true, TargetRPS: 100, Mix: validMix, BlockSelection: "ancient"},
			"invalid readLoad.blockSelection",
		},
		{
			"archive selection is accepted",
			&types.ReadLoadConfig{Enabled: true, TargetRPS: 100, Mix: validMix, BlockSelection: types.ReadBlockArchive},
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := types.StartTestRequest{
				Pattern:      types.PatternConstant,
				DurationSec:  60,
				ConstantRate: 100,
				ReadLoad:     tc.readLoad,
			}
			err := validateStartRequest(&req)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err)
			}
		})
	}
}

func TestValidateStartRequest_Realistic(t *testing.T) {
	tests := []struct {
		name    string
		req     types.StartTestRequest
		wantErr string
	}{
		{
			name: "valid realistic pattern",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 100,
					TargetTPS:   50,
					MinTipGwei:  1.0,
					MaxTipGwei:  10.0,
				},
			},
			wantErr: "",
		},
		{
			name: "missing realisticConfig",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
			},
			wantErr: "realisticConfig is required",
		},
		{
			name: "zero numAccounts triggers auto-calculation",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 0,
					TargetTPS:   50,
				},
			},
			wantErr: "", // 0 is valid: triggers auto-calculation
		},
		{
			name: "negative numAccounts in realisticConfig",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: -1,
					TargetTPS:   50,
				},
			},
			wantErr: "realisticConfig.numAccounts cannot be negative",
		},
		{
			name: "numAccounts exceeds max in realisticConfig",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 200000,
					TargetTPS:   50,
				},
			},
			wantErr: "realisticConfig.numAccounts exceeds maximum",
		},
		{
			name: "zero targetTps in realisticConfig",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 100,
					TargetTPS:   0,
				},
			},
			wantErr: "realisticConfig.targetTps must be positive",
		},
		{
			name: "targetTps exceeds max in realisticConfig",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 100,
					TargetTPS:   200000,
				},
			},
			wantErr: "realisticConfig.targetTps exceeds maximum",
		},
		{
			name: "negative minTipGwei",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 100,
					TargetTPS:   50,
					MinTipGwei:  -1.0,
					MaxTipGwei:  10.0,
				},
			},
			wantErr: "realisticConfig.minTipGwei cannot be negative",
		},
		{
			name: "maxTipGwei less than minTipGwei",
			req: types.StartTestRequest{
				Pattern:     types.PatternRealistic,
				DurationSec: 60,
				RealisticConfig: &types.RealisticTestConfig{
					NumAccounts: 100,
					TargetTPS:   50,
					MinTipGwei:  10.0,
					MaxTipGwei:  5.0,
				},
			},
			wantErr: "realisticConfig.maxTipGwei",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStartRequest(&tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateStartRequest() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateStartRequest() expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateStartRequest() error = %q, want error containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestValidPatterns(t *testing.T) {
	// Test that all expected patterns are valid
	expectedPatterns := []types.LoadPattern{
		types.PatternConstant,
		types.PatternRamp,
		types.PatternSpike,
		types.PatternAdaptive,
		types.PatternRealistic,
	}

	for _, p := range expectedPatterns {
		if !validPatterns[p] {
			t.Errorf("Expected pattern %q to be valid", p)
		}
	}

	// Test that an invalid pattern is not valid
	if validPatterns["invalid"] {
		t.Error("Expected 'invalid' pattern to not be valid")
	}
}

func TestValidTxTypes(t *testing.T) {
	// Test that all expected tx types are valid
	expectedTypes := []types.TransactionType{
		types.TxTypeEthTransfer,
		types.TxTypeERC20Transfer,
		types.TxTypeERC20Approve,
		types.TxTypeUniswapSwap,
		types.TxTypeStorageWrite,
		types.TxTypeHeavyCompute,
		"", // Empty is valid (default)
	}

	for _, txType := range expectedTypes {
		if !validTxTypes[txType] {
			t.Errorf("Expected tx type %q to be valid", txType)
		}
	}

	// Test that an invalid tx type is not valid
	if validTxTypes["invalid_type"] {
		t.Error("Expected 'invalid_type' to not be valid")
	}
}
