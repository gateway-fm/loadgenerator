package txbuilder

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

func TestEncodeERC721Mint(t *testing.T) {
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tokenID := big.NewInt(42)

	data := encodeERC721Mint(to, tokenID)

	if len(data) != 4+32+32 {
		t.Fatalf("encodeERC721Mint length = %d, want %d", len(data), 4+32+32)
	}

	wantSelector := common.FromHex("0x40c10f19")
	if !bytes.Equal(data[0:4], wantSelector) {
		t.Errorf("selector = %x, want %x", data[0:4], wantSelector)
	}

	// Left-padded address: 12 zero bytes then 20 address bytes.
	for i := 4; i < 4+12; i++ {
		if data[i] != 0 {
			t.Errorf("address padding byte %d = %x, want 0", i, data[i])
		}
	}
	if !bytes.Equal(data[4+12:4+32], to.Bytes()) {
		t.Errorf("address bytes mismatch")
	}

	// Token ID right-aligned in last 32 bytes.
	gotID := new(big.Int).SetBytes(data[4+32 : 4+64])
	if gotID.Cmp(tokenID) != 0 {
		t.Errorf("tokenID = %s, want %s", gotID, tokenID)
	}
}

func TestEncodeERC721Mint_NegativeTokenIDPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on negative tokenID")
		}
	}()
	encodeERC721Mint(common.Address{}, big.NewInt(-1))
}

func TestEncodeERC721TransferFrom(t *testing.T) {
	from := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	to := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	tokenID := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)) // max uint256

	data := encodeERC721TransferFrom(from, to, tokenID)

	if len(data) != 4+32+32+32 {
		t.Fatalf("encodeERC721TransferFrom length = %d, want %d", len(data), 4+32+32+32)
	}

	wantSelector := common.FromHex("0x23b872dd")
	if !bytes.Equal(data[0:4], wantSelector) {
		t.Errorf("selector = %x, want %x", data[0:4], wantSelector)
	}

	if !bytes.Equal(data[4+12:4+32], from.Bytes()) {
		t.Errorf("from bytes mismatch")
	}
	if !bytes.Equal(data[4+32+12:4+64], to.Bytes()) {
		t.Errorf("to bytes mismatch")
	}
	gotID := new(big.Int).SetBytes(data[4+64 : 4+96])
	if gotID.Cmp(tokenID) != 0 {
		t.Errorf("tokenID = %s, want max uint256", gotID)
	}
}

func TestERC721TransferBuilder_Interface(t *testing.T) {
	b := NewERC721TransferBuilder()

	if got := b.Type(); got != types.TxTypeERC721Transfer {
		t.Errorf("Type() = %v, want %v", got, types.TxTypeERC721Transfer)
	}
	if got := b.GasLimit(); got != 100000 {
		t.Errorf("GasLimit() = %d, want 100000", got)
	}
	if !b.RequiresContract() {
		t.Error("RequiresContract() should be true")
	}
	if bc := b.ContractBytecode(); len(bc) == 0 {
		t.Error("ContractBytecode() should not be empty")
	}
}

func TestERC721TransferBuilder_SetContractAddress(t *testing.T) {
	b := NewERC721TransferBuilder()
	addr := common.HexToAddress("0xc0ffee0000000000000000000000000000000000")
	b.SetContractAddress(addr)

	tx, err := b.Build(TxParams{
		ChainID:   big.NewInt(42069),
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if tx.To() == nil || *tx.To() != addr {
		t.Errorf("Transaction To = %v, want %v", tx.To(), addr)
	}
}

func TestERC721TransferBuilder_Build(t *testing.T) {
	b := NewERC721TransferBuilder()
	contractAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	b.SetContractAddress(contractAddr)

	params := TxParams{
		ChainID:   big.NewInt(42069),
		Nonce:     7,
		GasTipCap: big.NewInt(1_000_000_000),
		GasFeeCap: big.NewInt(10_000_000_000),
	}

	tx, err := b.Build(params)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if tx.ChainId().Cmp(params.ChainID) != 0 {
		t.Errorf("ChainId = %v, want %v", tx.ChainId(), params.ChainID)
	}
	if tx.Nonce() != params.Nonce {
		t.Errorf("Nonce = %d, want %d", tx.Nonce(), params.Nonce)
	}
	if tx.Gas() != 100000 {
		t.Errorf("Gas = %d, want 100000", tx.Gas())
	}
	if tx.Value().Sign() != 0 {
		t.Errorf("Value = %v, want 0", tx.Value())
	}

	data := tx.Data()
	if len(data) != 100 {
		t.Fatalf("Data length = %d, want 100 (4 + 3*32)", len(data))
	}

	wantSelector := common.FromHex("0x23b872dd")
	if !bytes.Equal(data[0:4], wantSelector) {
		t.Errorf("selector = %x, want %x", data[0:4], wantSelector)
	}
}

func TestERC721TransferBuilder_BuildRejectsBadChainID(t *testing.T) {
	b := NewERC721TransferBuilder()

	if _, err := b.Build(TxParams{ChainID: nil}); err == nil {
		t.Error("expected error for nil ChainID")
	}
	if _, err := b.Build(TxParams{ChainID: big.NewInt(0)}); err == nil {
		t.Error("expected error for zero ChainID")
	}
}

func TestERC721TransferBuilder_RandomnessAcrossCalls(t *testing.T) {
	b := NewERC721TransferBuilder()
	b.SetContractAddress(common.HexToAddress("0x2222222222222222222222222222222222222222"))

	params := TxParams{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
	}

	// Build N transfers; collect (from, to, tokenId) calldata slices.
	const n = 16
	calldata := make([][]byte, n)
	for i := range n {
		tx, err := b.Build(params)
		if err != nil {
			t.Fatalf("Build() iteration %d: %v", i, err)
		}
		calldata[i] = tx.Data()
	}

	// At least two of N must differ — otherwise randomness is broken.
	allSame := true
	for i := 1; i < n; i++ {
		if !bytes.Equal(calldata[0], calldata[i]) {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("all N calldata slices identical — random recipients/tokenIDs not working")
	}
}

func TestERC721TransferBuilder_ContractBytecodeMatchesNFTBytecode(t *testing.T) {
	b := NewERC721TransferBuilder()
	if !bytes.Equal(b.ContractBytecode(), NFTBytecode) {
		t.Error("ContractBytecode() did not return NFTBytecode")
	}
}

func TestBuildMintTx(t *testing.T) {
	chainID := big.NewInt(42069)
	contract := common.HexToAddress("0x3333333333333333333333333333333333333333")
	to := common.HexToAddress("0x4444444444444444444444444444444444444444")
	tokenID := big.NewInt(123)

	tx := BuildMintTx(chainID, 5, contract, to, tokenID, big.NewInt(1), big.NewInt(2), false)

	if tx.Nonce() != 5 {
		t.Errorf("Nonce = %d, want 5", tx.Nonce())
	}
	if tx.To() == nil || *tx.To() != contract {
		t.Errorf("To = %v, want %v", tx.To(), contract)
	}
	if tx.Value().Sign() != 0 {
		t.Errorf("Value = %v, want 0", tx.Value())
	}
	if tx.Gas() != 100000 {
		t.Errorf("Gas = %d, want 100000", tx.Gas())
	}

	data := tx.Data()
	if len(data) != 4+32+32 {
		t.Fatalf("Data length = %d, want %d", len(data), 4+32+32)
	}
	wantSelector := common.FromHex("0x40c10f19")
	if !bytes.Equal(data[0:4], wantSelector) {
		t.Errorf("selector = %x, want %x", data[0:4], wantSelector)
	}
	if !bytes.Equal(data[4+12:4+32], to.Bytes()) {
		t.Errorf("to bytes mismatch")
	}
	gotID := new(big.Int).SetBytes(data[4+32 : 4+64])
	if gotID.Cmp(tokenID) != 0 {
		t.Errorf("tokenID = %s, want %s", gotID, tokenID)
	}
}

func TestBuildMintTx_Legacy(t *testing.T) {
	tx := BuildMintTx(big.NewInt(1), 0, common.Address{}, common.Address{}, big.NewInt(0), big.NewInt(0), big.NewInt(5), true)
	if tx.Type() != 0 { // LegacyTxType
		t.Errorf("Type = %d, want 0 (legacy)", tx.Type())
	}
	if tx.GasPrice().Cmp(big.NewInt(5)) != 0 {
		t.Errorf("GasPrice = %v, want 5", tx.GasPrice())
	}
}

func TestNewDefaultRegistry_IncludesERC721(t *testing.T) {
	r := NewDefaultRegistry(common.HexToAddress("0x5555555555555555555555555555555555555555"))

	b, err := r.Get(types.TxTypeERC721Transfer)
	if err != nil {
		t.Fatalf("Get(erc721-transfer) error = %v", err)
	}
	if b == nil {
		t.Fatal("Get(erc721-transfer) returned nil")
	}
	if b.Type() != types.TxTypeERC721Transfer {
		t.Errorf("Type() = %v, want %v", b.Type(), types.TxTypeERC721Transfer)
	}
}
