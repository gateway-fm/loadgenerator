package config

import (
	"testing"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestCalculateRequiredAccounts(t *testing.T) {
	tests := []struct {
		name        string
		targetTPS   int
		blockTimeMS int
		wantMin     int
		wantMax     int
	}{
		{
			name:        "low TPS with default block time",
			targetTPS:   10,
			blockTimeMS: DefaultBlockTimeMS, // 250ms
			wantMin:     MinAccounts,        // ceil(10*0.25/30*1.5)=1, clamped to min
			wantMax:     MinAccounts,
		},
		{
			name:        "100 TPS with 250ms blocks",
			targetTPS:   100,
			blockTimeMS: 250,
			wantMin:     MinAccounts, // ceil(100*0.25/30*1.5)=2, clamped to min
			wantMax:     MinAccounts,
		},
		{
			name:        "1000 TPS with 1s blocks",
			targetTPS:   1000,
			blockTimeMS: 1000,
			wantMin:     MinAccounts, // ceil(1000/30*1.5)=50, clamped to min
			wantMax:     MinAccounts,
		},
		{
			name:        "10000 TPS with 1s blocks",
			targetTPS:   10000,
			blockTimeMS: 1000,
			wantMin:     490, // ceil(10000/30*1.5)=500
			wantMax:     510,
		},
		{
			name:        "very high TPS capped at max",
			targetTPS:   100000,
			blockTimeMS: 1000,
			wantMin:     MaxAccountsLimit,
			wantMax:     MaxAccountsLimit,
		},
		{
			name:        "zero block time uses default",
			targetTPS:   100,
			blockTimeMS: 0,
			wantMin:     MinAccounts, // clamped to min
			wantMax:     MinAccounts,
		},
		{
			name:        "negative block time uses default",
			targetTPS:   100,
			blockTimeMS: -100,
			wantMin:     MinAccounts, // clamped to min
			wantMax:     MinAccounts,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateRequiredAccounts(tt.targetTPS, tt.blockTimeMS)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("CalculateRequiredAccounts(%d, %d) = %d, want between %d and %d",
					tt.targetTPS, tt.blockTimeMS, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestEstimateMaxTPS(t *testing.T) {
	tests := []struct {
		name        string
		numAccounts int
		blockTimeMS int
		wantMin     int
		wantMax     int
	}{
		{
			name:        "10 accounts with 250ms blocks",
			numAccounts: 10,
			blockTimeMS: 250,
			wantMin:     1100, // 10 * 30 / 0.25 = 1200
			wantMax:     1300,
		},
		{
			name:        "100 accounts with 1s blocks",
			numAccounts: 100,
			blockTimeMS: 1000,
			wantMin:     2900, // 100 * 30 / 1.0 = 3000
			wantMax:     3100,
		},
		{
			name:        "1 account with 1s blocks",
			numAccounts: 1,
			blockTimeMS: 1000,
			wantMin:     25, // 1 * 30 / 1.0 = 30
			wantMax:     35,
		},
		{
			name:        "zero block time uses default",
			numAccounts: 100,
			blockTimeMS: 0,
			wantMin:     11000, // 100 * 30 / 0.25 = 12000
			wantMax:     13000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateMaxTPS(tt.numAccounts, tt.blockTimeMS)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("EstimateMaxTPS(%d, %d) = %d, want between %d and %d",
					tt.numAccounts, tt.blockTimeMS, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCheckAccountSufficiency(t *testing.T) {
	tests := []struct {
		name        string
		numAccounts int
		targetTPS   int
		blockTimeMS int
		wantWarning bool
	}{
		{
			name:        "sufficient accounts",
			numAccounts: 100,
			targetTPS:   50,
			blockTimeMS: 250,
			wantWarning: false,
		},
		{
			name:        "insufficient accounts",
			numAccounts: 10,
			targetTPS:   1000,
			blockTimeMS: 250,
			wantWarning: true,
		},
		{
			name:        "zero TPS returns no warning",
			numAccounts: 10,
			targetTPS:   0,
			blockTimeMS: 250,
			wantWarning: false,
		},
		{
			name:        "zero accounts returns no warning",
			numAccounts: 0,
			targetTPS:   100,
			blockTimeMS: 250,
			wantWarning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning := CheckAccountSufficiency(tt.numAccounts, tt.targetTPS, tt.blockTimeMS)
			hasWarning := warning != ""
			if hasWarning != tt.wantWarning {
				t.Errorf("CheckAccountSufficiency(%d, %d, %d) warning=%q, wantWarning=%v",
					tt.numAccounts, tt.targetTPS, tt.blockTimeMS, warning, tt.wantWarning)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				BuilderRPCURL: "http://localhost:13000",
				L2RPCURL:      "http://localhost:8545",
				ChainID:       42069,
				GasTipCap:     1000000000,
				GasFeeCap:     0,
				GasLimit:      21000,
			},
			wantErr: false,
		},
		{
			name: "missing builder URL",
			config: Config{
				BuilderRPCURL: "",
				L2RPCURL:      "http://localhost:8545",
				ChainID:       42069,
				GasTipCap:     1000000000,
				GasLimit:      21000,
			},
			wantErr: true,
		},
		{
			name: "missing L2 URL",
			config: Config{
				BuilderRPCURL: "http://localhost:13000",
				L2RPCURL:      "",
				ChainID:       42069,
				GasTipCap:     1000000000,
				GasLimit:      21000,
			},
			wantErr: true,
		},
		{
			name: "invalid chain ID",
			config: Config{
				BuilderRPCURL: "http://localhost:13000",
				L2RPCURL:      "http://localhost:8545",
				ChainID:       0,
				GasTipCap:     1000000000,
				GasLimit:      21000,
			},
			wantErr: true,
		},
		{
			name: "zero gas tip cap",
			config: Config{
				BuilderRPCURL: "http://localhost:13000",
				L2RPCURL:      "http://localhost:8545",
				ChainID:       42069,
				GasTipCap:     0,
				GasLimit:      21000,
			},
			wantErr: true,
		},
		{
			name: "negative gas fee cap",
			config: Config{
				BuilderRPCURL: "http://localhost:13000",
				L2RPCURL:      "http://localhost:8545",
				ChainID:       42069,
				GasTipCap:     1000000000,
				GasFeeCap:     -1,
				GasLimit:      21000,
			},
			wantErr: true,
		},
		{
			name: "zero gas limit",
			config: Config{
				BuilderRPCURL: "http://localhost:13000",
				L2RPCURL:      "http://localhost:8545",
				ChainID:       42069,
				GasTipCap:     1000000000,
				GasLimit:      0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCLIConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  CLIConfig
		wantErr bool
	}{
		{
			name: "valid constant pattern",
			config: CLIConfig{
				Pattern:     types.PatternConstant,
				TPS:         100,
				Duration:    30000000000, // 30s
				NumAccounts: 100,
			},
			wantErr: false,
		},
		{
			name: "valid ramp pattern",
			config: CLIConfig{
				Pattern:     types.PatternRamp,
				TPS:         100,
				Duration:    30000000000,
				NumAccounts: 100,
			},
			wantErr: false,
		},
		{
			name: "valid adaptive pattern",
			config: CLIConfig{
				Pattern:     types.PatternAdaptive,
				TPS:         100,
				Duration:    30000000000,
				NumAccounts: 100,
			},
			wantErr: false,
		},
		{
			name: "invalid pattern",
			config: CLIConfig{
				Pattern:     "invalid",
				TPS:         100,
				Duration:    30000000000,
				NumAccounts: 100,
			},
			wantErr: true,
		},
		{
			name: "zero TPS",
			config: CLIConfig{
				Pattern:     types.PatternConstant,
				TPS:         0,
				Duration:    30000000000,
				NumAccounts: 100,
			},
			wantErr: true,
		},
		{
			name: "negative TPS",
			config: CLIConfig{
				Pattern:     types.PatternConstant,
				TPS:         -10,
				Duration:    30000000000,
				NumAccounts: 100,
			},
			wantErr: true,
		},
		{
			name: "zero duration",
			config: CLIConfig{
				Pattern:     types.PatternConstant,
				TPS:         100,
				Duration:    0,
				NumAccounts: 100,
			},
			wantErr: true,
		},
		{
			name: "zero accounts",
			config: CLIConfig{
				Pattern:     types.PatternConstant,
				TPS:         100,
				Duration:    30000000000,
				NumAccounts: 0,
			},
			wantErr: true,
		},
		{
			name: "too many accounts",
			config: CLIConfig{
				Pattern:     types.PatternConstant,
				TPS:         100,
				Duration:    30000000000,
				NumAccounts: 20000,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("CLIConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
