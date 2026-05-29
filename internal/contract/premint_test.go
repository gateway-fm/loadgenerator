package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/gateway-fm/loadgenerator/internal/account"
	"github.com/gateway-fm/loadgenerator/internal/rpc"
)

// premintMockClient is a focused rpc.Client mock for PreMintNFTs tests.
//
// It records every batched send (so tests can introspect the RLP traffic)
// and exposes a virtual nonce counter that advances on every successful send.
// GetNonce returns the current counter value, so the helper's confirmation
// poll resolves immediately after all batches land.
type premintMockClient struct {
	mu        sync.Mutex
	sentRLPs  [][]byte
	nonceAddr atomic.Uint64

	getNonceCalls atomic.Int32
	sendErrFor    map[int]error // override per-index error in next batch (by absolute index)
	totalSent     atomic.Int32
}

var _ rpc.Client = (*premintMockClient)(nil)

func newPremintMockClient(startNonce uint64) *premintMockClient {
	m := &premintMockClient{}
	m.nonceAddr.Store(startNonce)
	return m
}

func (m *premintMockClient) GetNonce(ctx context.Context, address string) (uint64, error) {
	m.getNonceCalls.Add(1)
	return m.nonceAddr.Load(), nil
}

func (m *premintMockClient) SendRawTransactionBatch(ctx context.Context, txRLPs [][]byte) []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	errs := make([]error, len(txRLPs))
	for i, rlp := range txRLPs {
		absIdx := int(m.totalSent.Load()) + i
		if e, ok := m.sendErrFor[absIdx]; ok {
			errs[i] = e
			continue
		}
		copyRLP := make([]byte, len(rlp))
		copy(copyRLP, rlp)
		m.sentRLPs = append(m.sentRLPs, copyRLP)
		m.nonceAddr.Add(1)
	}
	m.totalSent.Add(int32(len(txRLPs)))
	return errs
}

func (m *premintMockClient) SendRawTransaction(ctx context.Context, txRLP []byte) error {
	errs := m.SendRawTransactionBatch(ctx, [][]byte{txRLP})
	return errs[0]
}

// Stubs for the rest of the interface — unused by PreMintNFTs but needed for assignability.
func (m *premintMockClient) Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	return nil, nil
}
func (m *premintMockClient) BatchCall(ctx context.Context, calls []rpc.BatchRequest) ([]rpc.BatchResponse, error) {
	return nil, nil
}
func (m *premintMockClient) GetConfirmedNonce(ctx context.Context, address string) (uint64, error) {
	return 0, nil
}
func (m *premintMockClient) GetBlockNumber(ctx context.Context) (uint64, error) { return 0, nil }
func (m *premintMockClient) GetBlockByNumber(ctx context.Context, blockNum uint64) (*rpc.Block, error) {
	return nil, nil
}
func (m *premintMockClient) GetBlockByNumberFull(ctx context.Context, blockNum uint64) (*rpc.BlockFull, error) {
	return nil, nil
}
func (m *premintMockClient) GetBlocksByNumberFullBatch(ctx context.Context, blockNums []uint64) ([]*rpc.BlockFull, error) {
	return nil, nil
}
func (m *premintMockClient) GetCode(ctx context.Context, address string) (string, error) {
	return "", nil
}
func (m *premintMockClient) GetGasPrice(ctx context.Context) (uint64, error)       { return 0, nil }
func (m *premintMockClient) GetBaseFee(ctx context.Context) (uint64, error)        { return 0, nil }
func (m *premintMockClient) GetBalance(ctx context.Context, address string) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (m *premintMockClient) GetTransactionReceipt(ctx context.Context, txHash string) (*rpc.TransactionReceipt, error) {
	return nil, nil
}
func (m *premintMockClient) GetTransactionReceiptsBatch(ctx context.Context, txHashes []string) ([]*rpc.TransactionReceipt, error) {
	return nil, nil
}
func (m *premintMockClient) GetTransactionByHash(ctx context.Context, txHash string) (*rpc.TransactionInfo, error) {
	return nil, nil
}

func newTestAccount(t *testing.T) *account.Account {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return account.NewAccount(key)
}

func TestPreMintNFTs_ZeroCount(t *testing.T) {
	client := newPremintMockClient(0)
	d := NewDeployer(client, big.NewInt(1), big.NewInt(1), nil)

	if err := d.PreMintNFTs(context.Background(), newTestAccount(t), common.Address{}, 0, nil); err != nil {
		t.Errorf("PreMintNFTs(0) error = %v, want nil", err)
	}
	if client.totalSent.Load() != 0 {
		t.Errorf("zero count should not send any TX, sent = %d", client.totalSent.Load())
	}
}

func TestPreMintNFTs_SendsExpectedTxs(t *testing.T) {
	const startNonce uint64 = 100
	const count = 5

	client := newPremintMockClient(startNonce)
	d := NewDeployer(client, big.NewInt(42069), big.NewInt(1_000_000_000), nil)
	d.SetUseLegacy(true) // simpler signer, no chain-id quirks in legacy parsing

	acct := newTestAccount(t)
	nftAddr := common.HexToAddress("0xdeadbeef00000000000000000000000000000000")

	var progressCalls []struct{ minted, total int }
	progress := func(minted, total int) {
		progressCalls = append(progressCalls, struct{ minted, total int }{minted, total})
	}

	if err := d.PreMintNFTs(context.Background(), acct, nftAddr, count, progress); err != nil {
		t.Fatalf("PreMintNFTs() error = %v", err)
	}

	if got := int(client.totalSent.Load()); got != count {
		t.Fatalf("sent %d TX, want %d", got, count)
	}
	if len(progressCalls) == 0 {
		t.Error("progress callback never invoked")
	}
	last := progressCalls[len(progressCalls)-1]
	if last.minted != count || last.total != count {
		t.Errorf("final progress = %+v, want minted=%d total=%d", last, count, count)
	}

	signer := types.LatestSignerForChainID(big.NewInt(42069))
	mintSelector := common.FromHex("0x40c10f19")

	for i, rlp := range client.sentRLPs {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(rlp); err != nil {
			t.Fatalf("decode tx %d: %v", i, err)
		}

		if tx.To() == nil || *tx.To() != nftAddr {
			t.Errorf("tx %d To = %v, want %v", i, tx.To(), nftAddr)
		}
		if tx.Nonce() != startNonce+uint64(i) {
			t.Errorf("tx %d Nonce = %d, want %d", i, tx.Nonce(), startNonce+uint64(i))
		}

		data := tx.Data()
		if len(data) != 4+32+32 {
			t.Fatalf("tx %d data length = %d, want %d", i, len(data), 4+32+32)
		}
		if !bytes.Equal(data[0:4], mintSelector) {
			t.Errorf("tx %d selector = %x, want %x", i, data[0:4], mintSelector)
		}
		gotTokenID := new(big.Int).SetBytes(data[4+32 : 4+64])
		if gotTokenID.Cmp(big.NewInt(int64(i))) != 0 {
			t.Errorf("tx %d tokenID = %s, want %d", i, gotTokenID, i)
		}

		// Confirm the signer is the minter account.
		from, err := types.Sender(signer, tx)
		if err != nil {
			t.Fatalf("recover sender tx %d: %v", i, err)
		}
		if from != acct.Address {
			t.Errorf("tx %d sender = %v, want %v", i, from, acct.Address)
		}
	}
}

func TestPreMintNFTs_BatchesLargeCounts(t *testing.T) {
	// preMintBatchSize is 200; with count=350 we expect two batches (200 + 150).
	const count = 350
	client := newPremintMockClient(0)
	d := NewDeployer(client, big.NewInt(1), big.NewInt(1), nil)
	d.SetUseLegacy(true)

	if err := d.PreMintNFTs(context.Background(), newTestAccount(t), common.Address{}, count, nil); err != nil {
		t.Fatalf("PreMintNFTs(%d) error = %v", count, err)
	}
	if got := int(client.totalSent.Load()); got != count {
		t.Errorf("sent %d, want %d", got, count)
	}
	if got := len(client.sentRLPs); got != count {
		t.Errorf("captured %d RLPs, want %d", got, count)
	}
}

func TestPreMintNFTs_ReturnsErrorOnSendFailure(t *testing.T) {
	client := newPremintMockClient(0)
	client.sendErrFor = map[int]error{2: errors.New("boom")}
	d := NewDeployer(client, big.NewInt(1), big.NewInt(1), nil)
	d.SetUseLegacy(true)

	err := d.PreMintNFTs(context.Background(), newTestAccount(t), common.Address{}, 5, nil)
	if err == nil {
		t.Fatal("expected error from PreMintNFTs when send fails")
	}
}
