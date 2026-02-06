// Package rpc provides JSON-RPC client functionality with retry logic.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Client is the interface for JSON-RPC communication.
type Client interface {
	// Call makes a JSON-RPC call.
	Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error)

	// BatchCall makes multiple JSON-RPC calls in a single HTTP request.
	BatchCall(ctx context.Context, calls []BatchRequest) ([]BatchResponse, error)

	// SendRawTransaction sends a signed transaction.
	SendRawTransaction(ctx context.Context, txRLP []byte) error

	// GetNonce fetches the nonce for an address.
	GetNonce(ctx context.Context, address string) (uint64, error)

	// GetConfirmedNonce fetches the confirmed nonce directly from chain (bypasses cache).
	GetConfirmedNonce(ctx context.Context, address string) (uint64, error)

	// GetBlockNumber returns the latest block number.
	GetBlockNumber(ctx context.Context) (uint64, error)

	// GetBlockByNumber fetches a block with transaction hashes.
	GetBlockByNumber(ctx context.Context, blockNum uint64) (*Block, error)

	// GetBlockByNumberFull fetches a block with full transaction data.
	GetBlockByNumberFull(ctx context.Context, blockNum uint64) (*BlockFull, error)

	// GetBlocksByNumberFullBatch fetches multiple blocks with full transaction data in a single request.
	GetBlocksByNumberFullBatch(ctx context.Context, blockNums []uint64) ([]*BlockFull, error)

	// GetCode returns contract code at an address.
	GetCode(ctx context.Context, address string) (string, error)

	// GetGasPrice returns the current gas price from the node.
	GetGasPrice(ctx context.Context) (uint64, error)

	// GetBaseFee returns the current block's baseFeePerGas.
	GetBaseFee(ctx context.Context) (uint64, error)

	// GetBalance returns the balance for an address.
	GetBalance(ctx context.Context, address string) (*big.Int, error)

	// GetTransactionReceipt returns the receipt for a transaction.
	GetTransactionReceipt(ctx context.Context, txHash string) (*TransactionReceipt, error)

	// GetTransactionReceiptsBatch fetches multiple receipts in a single request.
	GetTransactionReceiptsBatch(ctx context.Context, txHashes []string) ([]*TransactionReceipt, error)
}

// TransactionReceipt represents an Ethereum transaction receipt.
type TransactionReceipt struct {
	Status            uint64 `json:"status"`            // 1 = success, 0 = failure
	GasUsed           uint64 `json:"gasUsed"`           // Actual gas consumed
	ContractAddress   string `json:"contractAddress"`   // Created contract address (if any)
	BlockNumber       uint64 `json:"blockNumber"`       // Block this tx was included in
	EffectiveGasPrice uint64 `json:"effectiveGasPrice"` // Actual gas price paid
}

// Transaction represents a full transaction in a block.
type Transaction struct {
	Hash                 string `json:"hash"`
	From                 string `json:"from"`
	To                   string `json:"to"`
	Type                 uint64 `json:"type"`                 // Transaction type (0x7E/126 = deposit)
	MaxPriorityFeePerGas uint64 `json:"maxPriorityFeePerGas"` // EIP-1559 tip
	MaxFeePerGas         uint64 `json:"maxFeePerGas"`         // EIP-1559 max fee
	GasPrice             uint64 `json:"gasPrice"`             // Legacy gas price
	Nonce                uint64 `json:"nonce"`
}

// IsDeposit returns true if this is a deposit transaction (L1 attributes tx).
func (t Transaction) IsDeposit() bool {
	return t.Type == 126 // 0x7E = deposit transaction type in OP Stack
}

// BlockFull represents a block with full transaction data.
type BlockFull struct {
	Number        uint64        `json:"number"`
	Hash          string        `json:"hash"`
	Transactions  []Transaction `json:"transactions"`
	BaseFeePerGas string        `json:"baseFeePerGas,omitempty"`
	GasUsed       uint64        `json:"gasUsed"`
	GasLimit      uint64        `json:"gasLimit"`
	Timestamp     time.Time     `json:"timestamp"`
	TxCount       int           `json:"txCount"`     // Total transaction count
	UserTxCount   int           `json:"userTxCount"` // User transactions (excluding deposits)
}

// Block represents a block with transaction hashes and metrics.
type Block struct {
	Number        uint64    `json:"number"`
	Hash          string    `json:"hash"`
	Transactions  []string  `json:"transactions"`
	BaseFeePerGas string    `json:"baseFeePerGas,omitempty"` // EIP-1559 base fee (hex)
	GasUsed       uint64    `json:"gasUsed"`                 // Gas used in this block
	GasLimit      uint64    `json:"gasLimit"`                // Gas limit for this block
	Timestamp     time.Time `json:"timestamp"`               // Block timestamp
	TxCount       int       `json:"txCount"`                 // Number of transactions (convenience)
}

// JSONRPCRequest represents a JSON-RPC request.
type JSONRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

// JSONRPCResponse represents a JSON-RPC response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	ID      int             `json:"id"`
}

// JSONRPCError represents a JSON-RPC error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// BatchRequest represents a single request in a batch.
type BatchRequest struct {
	Method string
	Params []interface{}
}

// BatchResponse represents a single response in a batch.
type BatchResponse struct {
	Result json.RawMessage
	Error  error
}

// ClientConfig holds configuration for the RPC client.
type ClientConfig struct {
	URL            string
	Timeout        time.Duration
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Logger         *slog.Logger
}

// DefaultClientConfig returns default configuration.
// Uses 2s timeout to handle slow responses under load while detecting failures.
// For high-throughput sends, the sender loop handles nonce-based retry logic.
func DefaultClientConfig(url string) ClientConfig {
	return ClientConfig{
		URL:            url,
		Timeout:        2 * time.Second,  // Increased from 500ms - more resilient under load
		MaxRetries:     3,                // Increased from 1 - handle transient failures
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
	}
}

// HTTPClient implements Client using HTTP.
type HTTPClient struct {
	url        string
	httpClient *http.Client
	maxRetries int
	backoff    time.Duration
	maxBackoff time.Duration
	logger     *slog.Logger
}

// NewHTTPClient creates a new HTTP-based RPC client.
func NewHTTPClient(cfg ClientConfig) *HTTPClient {
	transport := &http.Transport{
		MaxIdleConns:        4000,
		MaxIdleConnsPerHost: 2000,
		MaxConnsPerHost:     2000, // Must match sender concurrency
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   false,
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &HTTPClient{
		url: cfg.URL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
		maxRetries: cfg.MaxRetries,
		backoff:    cfg.InitialBackoff,
		maxBackoff: cfg.MaxBackoff,
		logger:     logger,
	}
}

// Call makes a JSON-RPC call with retry logic.
func (c *HTTPClient) Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var lastErr error
	backoff := c.backoff

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, c.maxBackoff)
		}

		result, err := c.doRequest(ctx, body)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Check if it's a retryable HTTP error (429, 502, 503, 504)
		if isRetryableHTTPError(err) {
			// Use Retry-After header if present, otherwise exponential backoff
			backoff = getRetryDelay(err, backoff)
			c.logger.Debug("RPC got retryable HTTP error, retrying",
				slog.String("method", method),
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()),
				slog.Duration("backoff", backoff),
			)
			continue
		}

		// Don't retry on RPC errors (application-level errors)
		if isRPCError(err) {
			return nil, err
		}

		// Retry on other transient errors (network issues)
		c.logger.Debug("RPC call failed, retrying",
			slog.String("method", method),
			slog.Int("attempt", attempt+1),
			slog.String("error", err.Error()),
		)
	}

	return nil, fmt.Errorf("all retries failed: %w", lastErr)
}

func (c *HTTPClient) doRequest(ctx context.Context, body []byte) (json.RawMessage, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check HTTP status code BEFORE reading/parsing body
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var retryAfter time.Duration
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			// Try parsing as seconds (e.g., "2" or "0.5")
			if secs, err := strconv.ParseFloat(ra, 64); err == nil {
				retryAfter = time.Duration(secs * float64(time.Second))
			}
		}
		return nil, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
			Body:       string(errBody),
		}
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, &RPCError{
			Code:    rpcResp.Error.Code,
			Message: rpcResp.Error.Message,
		}
	}

	return rpcResp.Result, nil
}

// RPCError is an RPC-specific error.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

func isRPCError(err error) bool {
	_, ok := err.(*RPCError)
	return ok
}

// HTTPStatusError represents an HTTP-level error (non-2xx status).
type HTTPStatusError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       string
}

func (e *HTTPStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s (body: %s)", e.StatusCode, http.StatusText(e.StatusCode), e.Body)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// IsRetryable returns true if this HTTP error should be retried.
func (e *HTTPStatusError) IsRetryable() bool {
	// 429 Too Many Requests, 502 Bad Gateway, 503 Service Unavailable, 504 Gateway Timeout
	return e.StatusCode == 429 || e.StatusCode == 502 ||
		e.StatusCode == 503 || e.StatusCode == 504
}

func isRetryableHTTPError(err error) bool {
	if httpErr, ok := err.(*HTTPStatusError); ok {
		return httpErr.IsRetryable()
	}
	return false
}

func getRetryDelay(err error, defaultBackoff time.Duration) time.Duration {
	if httpErr, ok := err.(*HTTPStatusError); ok && httpErr.RetryAfter > 0 {
		return httpErr.RetryAfter
	}
	return defaultBackoff
}

// SendRawTransaction sends a signed transaction.
func (c *HTTPClient) SendRawTransaction(ctx context.Context, txRLP []byte) error {
	hexTx := hexutil.Encode(txRLP)
	_, err := c.Call(ctx, "eth_sendRawTransaction", []interface{}{hexTx})
	return err
}

// GetNonce fetches the nonce for an address.
// First tries eth_getPendingNonce (block builder's cached nonces) for better sync,
// falls back to eth_getTransactionCount with "pending" to include mempool transactions.
func (c *HTTPClient) GetNonce(ctx context.Context, address string) (uint64, error) {
	// Try the builder's pending nonce first (ensures load generator and builder are in sync)
	result, err := c.Call(ctx, "eth_getPendingNonce", []interface{}{address})
	if err == nil {
		var nonceHex string
		if err := json.Unmarshal(result, &nonceHex); err == nil {
			return hexutil.MustDecodeUint64(nonceHex), nil
		}
	}

	// Fall back to standard nonce query with "pending" to include mempool transactions.
	// Using "pending" is critical for high-throughput scenarios where multiple
	// transactions may be in-flight but not yet mined.
	result, err = c.Call(ctx, "eth_getTransactionCount", []interface{}{address, "pending"})
	if err != nil {
		return 0, err
	}

	var nonceHex string
	if err := json.Unmarshal(result, &nonceHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal nonce: %w", err)
	}

	return hexutil.MustDecodeUint64(nonceHex), nil
}

// GetConfirmedNonce fetches the confirmed nonce for an address directly from the chain.
// Uses "latest" to get only confirmed state, bypassing any pending/cache values.
// Use this when you need the true chain state (e.g., after cache reset).
func (c *HTTPClient) GetConfirmedNonce(ctx context.Context, address string) (uint64, error) {
	result, err := c.Call(ctx, "eth_getTransactionCount", []interface{}{address, "latest"})
	if err != nil {
		return 0, err
	}

	var nonceHex string
	if err := json.Unmarshal(result, &nonceHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal nonce: %w", err)
	}

	return hexutil.MustDecodeUint64(nonceHex), nil
}

// GetBlockNumber returns the latest block number.
func (c *HTTPClient) GetBlockNumber(ctx context.Context) (uint64, error) {
	result, err := c.Call(ctx, "eth_blockNumber", nil)
	if err != nil {
		return 0, err
	}

	var blockHex string
	if err := json.Unmarshal(result, &blockHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block number: %w", err)
	}

	return hexutil.MustDecodeUint64(blockHex), nil
}

// GetBlockByNumber fetches a block with transaction hashes and metrics.
func (c *HTTPClient) GetBlockByNumber(ctx context.Context, blockNum uint64) (*Block, error) {
	blockHex := hexutil.EncodeUint64(blockNum)
	result, err := c.Call(ctx, "eth_getBlockByNumber", []interface{}{blockHex, false})
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil
	}

	// Parse the response
	var rawBlock struct {
		Number        string   `json:"number"`
		Hash          string   `json:"hash"`
		Transactions  []string `json:"transactions"`
		BaseFeePerGas string   `json:"baseFeePerGas,omitempty"`
		GasUsed       string   `json:"gasUsed"`
		GasLimit      string   `json:"gasLimit"`
		Timestamp     string   `json:"timestamp"`
	}
	if err := json.Unmarshal(result, &rawBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	num, err := hexutil.DecodeUint64(rawBlock.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to decode block number: %w", err)
	}

	gasUsed, _ := hexutil.DecodeUint64(rawBlock.GasUsed)
	gasLimit, _ := hexutil.DecodeUint64(rawBlock.GasLimit)
	timestampUnix, _ := hexutil.DecodeUint64(rawBlock.Timestamp)

	return &Block{
		Number:        num,
		Hash:          rawBlock.Hash,
		Transactions:  rawBlock.Transactions,
		BaseFeePerGas: rawBlock.BaseFeePerGas,
		GasUsed:       gasUsed,
		GasLimit:      gasLimit,
		Timestamp:     time.Unix(int64(timestampUnix), 0),
		TxCount:       len(rawBlock.Transactions),
	}, nil
}

// GetBlockByNumberFull fetches a block with full transaction data.
// This is needed for tip ordering verification where we need maxPriorityFeePerGas.
func (c *HTTPClient) GetBlockByNumberFull(ctx context.Context, blockNum uint64) (*BlockFull, error) {
	blockHex := hexutil.EncodeUint64(blockNum)
	result, err := c.Call(ctx, "eth_getBlockByNumber", []interface{}{blockHex, true}) // true = full tx data
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil
	}

	// Parse the response with full transaction data
	var rawBlock struct {
		Number        string `json:"number"`
		Hash          string `json:"hash"`
		Transactions  []struct {
			Hash                 string `json:"hash"`
			From                 string `json:"from"`
			To                   string `json:"to"`
			Type                 string `json:"type"`
			MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas,omitempty"`
			MaxFeePerGas         string `json:"maxFeePerGas,omitempty"`
			GasPrice             string `json:"gasPrice"`
			Nonce                string `json:"nonce"`
		} `json:"transactions"`
		BaseFeePerGas string `json:"baseFeePerGas,omitempty"`
		GasUsed       string `json:"gasUsed"`
		GasLimit      string `json:"gasLimit"`
		Timestamp     string `json:"timestamp"`
	}
	if err := json.Unmarshal(result, &rawBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	num, err := hexutil.DecodeUint64(rawBlock.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to decode block number: %w", err)
	}

	gasUsed, _ := hexutil.DecodeUint64(rawBlock.GasUsed)
	gasLimit, _ := hexutil.DecodeUint64(rawBlock.GasLimit)
	timestampUnix, _ := hexutil.DecodeUint64(rawBlock.Timestamp)

	// Convert transactions and count user txs (excluding deposits)
	txs := make([]Transaction, 0, len(rawBlock.Transactions))
	userTxCount := 0
	for _, rawTx := range rawBlock.Transactions {
		var txType, maxPriorityFee, maxFee, gasPrice, nonce uint64
		if rawTx.Type != "" {
			txType, _ = hexutil.DecodeUint64(rawTx.Type)
		}
		if rawTx.MaxPriorityFeePerGas != "" {
			maxPriorityFee, _ = hexutil.DecodeUint64(rawTx.MaxPriorityFeePerGas)
		}
		if rawTx.MaxFeePerGas != "" {
			maxFee, _ = hexutil.DecodeUint64(rawTx.MaxFeePerGas)
		}
		if rawTx.GasPrice != "" {
			gasPrice, _ = hexutil.DecodeUint64(rawTx.GasPrice)
		}
		if rawTx.Nonce != "" {
			nonce, _ = hexutil.DecodeUint64(rawTx.Nonce)
		}

		tx := Transaction{
			Hash:                 rawTx.Hash,
			From:                 rawTx.From,
			To:                   rawTx.To,
			Type:                 txType,
			MaxPriorityFeePerGas: maxPriorityFee,
			MaxFeePerGas:         maxFee,
			GasPrice:             gasPrice,
			Nonce:                nonce,
		}
		txs = append(txs, tx)

		// Count non-deposit transactions
		if !tx.IsDeposit() {
			userTxCount++
		}
	}

	return &BlockFull{
		Number:        num,
		Hash:          rawBlock.Hash,
		Transactions:  txs,
		BaseFeePerGas: rawBlock.BaseFeePerGas,
		GasUsed:       gasUsed,
		GasLimit:      gasLimit,
		Timestamp:     time.Unix(int64(timestampUnix), 0),
		TxCount:       len(txs),
		UserTxCount:   userTxCount,
	}, nil
}

// GetCode returns contract code at an address.
func (c *HTTPClient) GetCode(ctx context.Context, address string) (string, error) {
	result, err := c.Call(ctx, "eth_getCode", []interface{}{address, "latest"})
	if err != nil {
		return "", err
	}

	var code string
	if err := json.Unmarshal(result, &code); err != nil {
		return "", fmt.Errorf("failed to unmarshal code: %w", err)
	}

	return code, nil
}

// GetGasPrice returns the current gas price from the node.
func (c *HTTPClient) GetGasPrice(ctx context.Context) (uint64, error) {
	result, err := c.Call(ctx, "eth_gasPrice", nil)
	if err != nil {
		return 0, err
	}

	var gasPriceHex string
	if err := json.Unmarshal(result, &gasPriceHex); err != nil {
		return 0, fmt.Errorf("failed to unmarshal gas price: %w", err)
	}

	return hexutil.MustDecodeUint64(gasPriceHex), nil
}

// GetBaseFee returns the current block's baseFeePerGas from the latest block.
func (c *HTTPClient) GetBaseFee(ctx context.Context) (uint64, error) {
	result, err := c.Call(ctx, "eth_getBlockByNumber", []any{"latest", false})
	if err != nil {
		return 0, err
	}

	var block struct {
		BaseFeePerGas string `json:"baseFeePerGas"`
	}
	if err := json.Unmarshal(result, &block); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	if block.BaseFeePerGas == "" {
		return 0, fmt.Errorf("baseFeePerGas not found in block")
	}

	return hexutil.MustDecodeUint64(block.BaseFeePerGas), nil
}

// GetBalance returns the balance for an address at the latest block.
func (c *HTTPClient) GetBalance(ctx context.Context, address string) (*big.Int, error) {
	result, err := c.Call(ctx, "eth_getBalance", []any{address, "latest"})
	if err != nil {
		return nil, err
	}

	var balanceHex string
	if err := json.Unmarshal(result, &balanceHex); err != nil {
		return nil, fmt.Errorf("failed to unmarshal balance: %w", err)
	}

	return hexutil.MustDecodeBig(balanceHex), nil
}

// GetTransactionReceipt returns the receipt for a transaction.
func (c *HTTPClient) GetTransactionReceipt(ctx context.Context, txHash string) (*TransactionReceipt, error) {
	result, err := c.Call(ctx, "eth_getTransactionReceipt", []any{txHash})
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, nil // Not found yet
	}

	var rawReceipt struct {
		Status            string `json:"status"`
		GasUsed           string `json:"gasUsed"`
		ContractAddress   string `json:"contractAddress"`
		BlockNumber       string `json:"blockNumber"`
		EffectiveGasPrice string `json:"effectiveGasPrice"`
	}
	if err := json.Unmarshal(result, &rawReceipt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
	}

	status, _ := hexutil.DecodeUint64(rawReceipt.Status)
	gasUsed, _ := hexutil.DecodeUint64(rawReceipt.GasUsed)
	blockNumber, _ := hexutil.DecodeUint64(rawReceipt.BlockNumber)
	effectiveGasPrice, _ := hexutil.DecodeUint64(rawReceipt.EffectiveGasPrice)

	return &TransactionReceipt{
		Status:            status,
		GasUsed:           gasUsed,
		ContractAddress:   rawReceipt.ContractAddress,
		BlockNumber:       blockNumber,
		EffectiveGasPrice: effectiveGasPrice,
	}, nil
}

// BatchCall makes multiple JSON-RPC calls in a single HTTP request.
// Results are returned in the same order as the input calls.
// Individual call errors are returned in BatchResponse.Error.
func (c *HTTPClient) BatchCall(ctx context.Context, calls []BatchRequest) ([]BatchResponse, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	// Build batch request array
	reqs := make([]JSONRPCRequest, len(calls))
	for i, call := range calls {
		reqs[i] = JSONRPCRequest{
			JSONRPC: "2.0",
			Method:  call.Method,
			Params:  call.Params,
			ID:      i + 1, // 1-indexed IDs for easier debugging
		}
	}

	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal batch request: %w", err)
	}

	var lastErr error
	backoff := c.backoff

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, c.maxBackoff)
		}

		results, err := c.doBatchRequest(ctx, body, len(calls))
		if err == nil {
			return results, nil
		}

		lastErr = err

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if isRetryableHTTPError(err) {
			backoff = getRetryDelay(err, backoff)
			c.logger.Debug("batch RPC got retryable HTTP error, retrying",
				slog.Int("callCount", len(calls)),
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()),
			)
			continue
		}

		// Don't retry on RPC errors
		if isRPCError(err) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("all batch retries failed: %w", lastErr)
}

func (c *HTTPClient) doBatchRequest(ctx context.Context, body []byte, expectedCount int) ([]BatchResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var retryAfter time.Duration
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.ParseFloat(ra, 64); err == nil {
				retryAfter = time.Duration(secs * float64(time.Second))
			}
		}
		return nil, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
			Body:       string(errBody),
		}
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var rpcResps []JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResps); err != nil {
		return nil, fmt.Errorf("failed to unmarshal batch response: %w", err)
	}

	// Build response map by ID for reordering
	respMap := make(map[int]*JSONRPCResponse, len(rpcResps))
	for i := range rpcResps {
		respMap[rpcResps[i].ID] = &rpcResps[i]
	}

	// Return results in original order
	results := make([]BatchResponse, expectedCount)
	for i := range expectedCount {
		rpcResp, ok := respMap[i+1]
		if !ok {
			results[i] = BatchResponse{Error: fmt.Errorf("missing response for request %d", i+1)}
			continue
		}
		if rpcResp.Error != nil {
			results[i] = BatchResponse{Error: &RPCError{Code: rpcResp.Error.Code, Message: rpcResp.Error.Message}}
			continue
		}
		results[i] = BatchResponse{Result: rpcResp.Result}
	}

	return results, nil
}

// GetBlocksByNumberFullBatch fetches multiple blocks with full transaction data in a single request.
// Returns blocks in the same order as blockNums. nil entries indicate blocks that weren't found or had errors.
func (c *HTTPClient) GetBlocksByNumberFullBatch(ctx context.Context, blockNums []uint64) ([]*BlockFull, error) {
	if len(blockNums) == 0 {
		return nil, nil
	}

	// Build batch requests
	calls := make([]BatchRequest, len(blockNums))
	for i, num := range blockNums {
		calls[i] = BatchRequest{
			Method: "eth_getBlockByNumber",
			Params: []interface{}{hexutil.EncodeUint64(num), true}, // true = full tx data
		}
	}

	responses, err := c.BatchCall(ctx, calls)
	if err != nil {
		return nil, fmt.Errorf("batch call failed: %w", err)
	}

	// Parse responses
	blocks := make([]*BlockFull, len(blockNums))
	for i, resp := range responses {
		if resp.Error != nil {
			c.logger.Debug("batch block fetch error", "block", blockNums[i], "error", resp.Error)
			continue
		}

		if string(resp.Result) == "null" {
			continue // Block not found
		}

		block, err := c.parseBlockFull(resp.Result)
		if err != nil {
			c.logger.Debug("failed to parse block", "block", blockNums[i], "error", err)
			continue
		}
		blocks[i] = block
	}

	return blocks, nil
}

// parseBlockFull parses a BlockFull from JSON.
func (c *HTTPClient) parseBlockFull(data json.RawMessage) (*BlockFull, error) {
	var rawBlock struct {
		Number       string `json:"number"`
		Hash         string `json:"hash"`
		Transactions []struct {
			Hash                 string `json:"hash"`
			From                 string `json:"from"`
			To                   string `json:"to"`
			Type                 string `json:"type"`
			MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas,omitempty"`
			MaxFeePerGas         string `json:"maxFeePerGas,omitempty"`
			GasPrice             string `json:"gasPrice"`
			Nonce                string `json:"nonce"`
		} `json:"transactions"`
		BaseFeePerGas string `json:"baseFeePerGas,omitempty"`
		GasUsed       string `json:"gasUsed"`
		GasLimit      string `json:"gasLimit"`
		Timestamp     string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &rawBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	num, err := hexutil.DecodeUint64(rawBlock.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to decode block number: %w", err)
	}

	gasUsed, _ := hexutil.DecodeUint64(rawBlock.GasUsed)
	gasLimit, _ := hexutil.DecodeUint64(rawBlock.GasLimit)
	timestampUnix, _ := hexutil.DecodeUint64(rawBlock.Timestamp)

	txs := make([]Transaction, 0, len(rawBlock.Transactions))
	userTxCount := 0
	for _, rawTx := range rawBlock.Transactions {
		var txType, maxPriorityFee, maxFee, gasPrice, nonce uint64
		if rawTx.Type != "" {
			txType, _ = hexutil.DecodeUint64(rawTx.Type)
		}
		if rawTx.MaxPriorityFeePerGas != "" {
			maxPriorityFee, _ = hexutil.DecodeUint64(rawTx.MaxPriorityFeePerGas)
		}
		if rawTx.MaxFeePerGas != "" {
			maxFee, _ = hexutil.DecodeUint64(rawTx.MaxFeePerGas)
		}
		if rawTx.GasPrice != "" {
			gasPrice, _ = hexutil.DecodeUint64(rawTx.GasPrice)
		}
		if rawTx.Nonce != "" {
			nonce, _ = hexutil.DecodeUint64(rawTx.Nonce)
		}

		tx := Transaction{
			Hash:                 rawTx.Hash,
			From:                 rawTx.From,
			To:                   rawTx.To,
			Type:                 txType,
			MaxPriorityFeePerGas: maxPriorityFee,
			MaxFeePerGas:         maxFee,
			GasPrice:             gasPrice,
			Nonce:                nonce,
		}
		txs = append(txs, tx)

		if !tx.IsDeposit() {
			userTxCount++
		}
	}

	return &BlockFull{
		Number:        num,
		Hash:          rawBlock.Hash,
		Transactions:  txs,
		BaseFeePerGas: rawBlock.BaseFeePerGas,
		GasUsed:       gasUsed,
		GasLimit:      gasLimit,
		Timestamp:     time.Unix(int64(timestampUnix), 0),
		TxCount:       len(txs),
		UserTxCount:   userTxCount,
	}, nil
}

// GetTransactionReceiptsBatch fetches multiple transaction receipts in a single request.
// Returns receipts in the same order as txHashes. nil entries indicate receipts not found or errors.
func (c *HTTPClient) GetTransactionReceiptsBatch(ctx context.Context, txHashes []string) ([]*TransactionReceipt, error) {
	if len(txHashes) == 0 {
		return nil, nil
	}

	// Build batch requests
	calls := make([]BatchRequest, len(txHashes))
	for i, hash := range txHashes {
		calls[i] = BatchRequest{
			Method: "eth_getTransactionReceipt",
			Params: []interface{}{hash},
		}
	}

	responses, err := c.BatchCall(ctx, calls)
	if err != nil {
		return nil, fmt.Errorf("batch call failed: %w", err)
	}

	// Parse responses
	receipts := make([]*TransactionReceipt, len(txHashes))
	for i, resp := range responses {
		if resp.Error != nil {
			c.logger.Debug("batch receipt fetch error", "txHash", txHashes[i], "error", resp.Error)
			continue
		}

		if string(resp.Result) == "null" {
			continue // Receipt not found
		}

		receipt, err := c.parseReceipt(resp.Result)
		if err != nil {
			c.logger.Debug("failed to parse receipt", "txHash", txHashes[i], "error", err)
			continue
		}
		receipts[i] = receipt
	}

	return receipts, nil
}

// parseReceipt parses a TransactionReceipt from JSON.
func (c *HTTPClient) parseReceipt(data json.RawMessage) (*TransactionReceipt, error) {
	var rawReceipt struct {
		Status            string `json:"status"`
		GasUsed           string `json:"gasUsed"`
		ContractAddress   string `json:"contractAddress"`
		BlockNumber       string `json:"blockNumber"`
		EffectiveGasPrice string `json:"effectiveGasPrice"`
	}
	if err := json.Unmarshal(data, &rawReceipt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
	}

	status, _ := hexutil.DecodeUint64(rawReceipt.Status)
	gasUsed, _ := hexutil.DecodeUint64(rawReceipt.GasUsed)
	blockNumber, _ := hexutil.DecodeUint64(rawReceipt.BlockNumber)
	effectiveGasPrice, _ := hexutil.DecodeUint64(rawReceipt.EffectiveGasPrice)

	return &TransactionReceipt{
		Status:            status,
		GasUsed:           gasUsed,
		ContractAddress:   rawReceipt.ContractAddress,
		BlockNumber:       blockNumber,
		EffectiveGasPrice: effectiveGasPrice,
	}, nil
}
