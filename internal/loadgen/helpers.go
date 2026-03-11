package loadgen

import (
	"fmt"
	"os"
	"time"
)

// blockMetricsPoint captures block-level metrics for a single block
type blockMetricsPoint struct {
	timestamp            time.Time
	blockNumber          uint64
	gasUsed              uint64
	gasLimit             uint64
	blockTime            float64 // seconds since last block
	txCount              int     // Number of transactions in block
	blockTimeMs          int64   // Block production interval in ms
	filterDurationMs     int64   // Time spent filtering transactions
	engineApiDurationMs  int64   // Time spent on Engine API calls
	totalBuildDurationMs int64   // Total block build time
}

// rollingGasPoint tracks gas used at a specific time for rolling window calculation
type rollingGasPoint struct {
	timestamp time.Time
	gasUsed   uint64
}

// rollingTxPoint tracks transaction count at a specific time for rolling window calculation
type rollingTxPoint struct {
	timestamp time.Time
	txCount   int
}

// onChainMetricsResult holds the results of on-chain verification.
type onChainMetricsResult struct {
	firstBlock   uint64
	lastBlock    uint64
	txCount      uint64
	gasUsed      uint64
	mgasPerSec   float64
	tps          float64
	durationSecs float64
}

// builderHeaderAttestationResponse is the response from the block builder's header attestation API.
type builderHeaderAttestationResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Status        string `json:"status"`
	Domain        struct {
		ChainID uint64 `json:"chainId"`
	} `json:"domain"`
	Commitment struct {
		BlockHash        string `json:"blockHash"`
		ParentHash       string `json:"parentHash"`
		StateRoot        string `json:"stateRoot"`
		ReceiptsRoot     string `json:"receiptsRoot"`
		BlockNumber      uint64 `json:"blockNumber"`
		Timestamp        uint64 `json:"timestamp"`
		GasUsed          uint64 `json:"gasUsed"`
		BaseFeePerGasWei any    `json:"baseFeePerGasWei"`
		SequencerAddress string `json:"sequencerAddress"`
	} `json:"commitment"`
	DigestHex    string    `json:"digestHex"`
	SignatureHex string    `json:"signatureHex"`
	RHex         string    `json:"rHex"`
	SHex         string    `json:"sHex"`
	V            uint8     `json:"v"`
	KeyID        string    `json:"keyId"`
	Provider     string    `json:"provider"`
	Failover     bool      `json:"failover"`
	Error        string    `json:"error"`
	SignedAt     time.Time `json:"signedAt"`
}

// parseHexUint64 parses a hex string (with or without 0x prefix) to uint64.
func parseHexUint64(s string) (uint64, error) {
	if len(s) > 2 && s[:2] == "0x" {
		s = s[2:]
	}
	var n uint64
	_, err := fmt.Sscanf(s, "%x", &n)
	return n, err
}

// GetEnvOrDefault returns environment variable or default value.
func GetEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// GetEnvIntOrDefault returns environment variable as int or default value.
func GetEnvIntOrDefault(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		var i int
		if _, err := fmt.Sscanf(val, "%d", &i); err == nil {
			return i
		}
	}
	return defaultVal
}