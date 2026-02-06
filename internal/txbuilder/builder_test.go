package txbuilder

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

var testRecipient = common.HexToAddress("0x1234567890123456789012345678901234567890")

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	if r.builders == nil {
		t.Fatal("expected builders map to be initialized")
	}
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	builder := NewETHTransferBuilder(testRecipient)

	r.Register(builder)

	got, err := r.Get(types.TxTypeEthTransfer)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got == nil {
		t.Fatal("expected builder to be registered")
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()

	_, err := r.Get(types.TxTypeEthTransfer)
	if err == nil {
		t.Error("expected error for unregistered type")
	}
}

func TestRegistry_GetAll(t *testing.T) {
	r := NewRegistry()
	r.Register(NewETHTransferBuilder(testRecipient))
	r.Register(NewStorageWriteBuilder())
	r.Register(NewHeavyComputeBuilder())

	builders := r.GetAll()
	if len(builders) != 3 {
		t.Errorf("GetAll() returned %d builders, want 3", len(builders))
	}
}

func TestRegistry_GetComplexBuilder(t *testing.T) {
	r := NewDefaultRegistry(testRecipient)

	// Uniswap V3 swap is a complex builder
	cb, ok := r.GetComplexBuilder(types.TxTypeUniswapSwap)
	if !ok {
		t.Error("expected UniswapV3SwapBuilder to be a ComplexBuilder")
	}
	if cb == nil {
		t.Error("expected non-nil ComplexBuilder")
	}
	if !cb.IsComplex() {
		t.Error("expected IsComplex() to return true")
	}

	// ETH transfer is not a complex builder
	_, ok = r.GetComplexBuilder(types.TxTypeEthTransfer)
	if ok {
		t.Error("expected ETHTransferBuilder to NOT be a ComplexBuilder")
	}

	// Non-existent type
	_, ok = r.GetComplexBuilder("nonexistent")
	if ok {
		t.Error("expected false for non-existent type")
	}
}

func TestNewDefaultRegistry(t *testing.T) {
	r := NewDefaultRegistry(testRecipient)

	expectedTypes := []types.TransactionType{
		types.TxTypeEthTransfer,
		types.TxTypeERC20Transfer,
		types.TxTypeERC20Approve,
		types.TxTypeUniswapSwap,
		types.TxTypeStorageWrite,
		types.TxTypeHeavyCompute,
	}

	for _, txType := range expectedTypes {
		builder, err := r.Get(txType)
		if err != nil {
			t.Errorf("Get(%s) error = %v", txType, err)
		}
		if builder == nil {
			t.Errorf("Get(%s) returned nil", txType)
		}
	}

	builders := r.GetAll()
	if len(builders) != len(expectedTypes) {
		t.Errorf("GetAll() returned %d builders, want %d", len(builders), len(expectedTypes))
	}
}

func TestETHTransferBuilder(t *testing.T) {
	builder := NewETHTransferBuilder(testRecipient)

	t.Run("Type", func(t *testing.T) {
		if got := builder.Type(); got != types.TxTypeEthTransfer {
			t.Errorf("Type() = %v, want %v", got, types.TxTypeEthTransfer)
		}
	})

	t.Run("GasLimit", func(t *testing.T) {
		if got := builder.GasLimit(); got != 21000 {
			t.Errorf("GasLimit() = %v, want 21000", got)
		}
	})

	t.Run("RequiresContract", func(t *testing.T) {
		if builder.RequiresContract() {
			t.Error("RequiresContract() should be false for ETH transfer")
		}
	})

	t.Run("ContractBytecode", func(t *testing.T) {
		if bc := builder.ContractBytecode(); bc != nil {
			t.Error("ContractBytecode() should be nil for ETH transfer")
		}
	})

	t.Run("SetContractAddress", func(t *testing.T) {
		// Should not panic
		builder.SetContractAddress(common.Address{})
	})

	t.Run("Build", func(t *testing.T) {
		params := TxParams{
			ChainID:   big.NewInt(42069),
			Nonce:     5,
			GasTipCap: big.NewInt(1000000000),  // 1 gwei
			GasFeeCap: big.NewInt(10000000000), // 10 gwei
		}

		tx, err := builder.Build(params)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if tx == nil {
			t.Fatal("Build() returned nil transaction")
		}

		// Verify transaction fields
		if tx.ChainId().Cmp(params.ChainID) != 0 {
			t.Errorf("ChainId = %v, want %v", tx.ChainId(), params.ChainID)
		}
		if tx.Nonce() != params.Nonce {
			t.Errorf("Nonce = %v, want %v", tx.Nonce(), params.Nonce)
		}
		if tx.GasTipCap().Cmp(params.GasTipCap) != 0 {
			t.Errorf("GasTipCap = %v, want %v", tx.GasTipCap(), params.GasTipCap)
		}
		if tx.GasFeeCap().Cmp(params.GasFeeCap) != 0 {
			t.Errorf("GasFeeCap = %v, want %v", tx.GasFeeCap(), params.GasFeeCap)
		}
		if tx.Gas() != 21000 {
			t.Errorf("Gas = %v, want 21000", tx.Gas())
		}
		if tx.To() == nil || *tx.To() != testRecipient {
			t.Errorf("To = %v, want %v", tx.To(), testRecipient)
		}
		if tx.Value().Cmp(big.NewInt(1)) != 0 {
			t.Errorf("Value = %v, want 1 wei", tx.Value())
		}
		if len(tx.Data()) != 0 {
			t.Errorf("Data length = %v, want 0", len(tx.Data()))
		}
	})
}

func TestHeavyComputeBuilder(t *testing.T) {
	builder := NewHeavyComputeBuilder()

	t.Run("Type", func(t *testing.T) {
		if got := builder.Type(); got != types.TxTypeHeavyCompute {
			t.Errorf("Type() = %v, want %v", got, types.TxTypeHeavyCompute)
		}
	})

	t.Run("GasLimit", func(t *testing.T) {
		if got := builder.GasLimit(); got != 500000 {
			t.Errorf("GasLimit() = %v, want 500000", got)
		}
	})

	t.Run("RequiresContract", func(t *testing.T) {
		if !builder.RequiresContract() {
			t.Error("RequiresContract() should be true for heavy compute")
		}
	})

	t.Run("ContractBytecode", func(t *testing.T) {
		bc := builder.ContractBytecode()
		if bc == nil {
			t.Error("ContractBytecode() should not be nil")
		}
		if len(bc) == 0 {
			t.Error("ContractBytecode() should not be empty")
		}
	})

	t.Run("SetContractAddress", func(t *testing.T) {
		contractAddr := common.HexToAddress("0xABCDEF1234567890ABCDEF1234567890ABCDEF12")
		builder.SetContractAddress(contractAddr)
		// Verify by building a transaction
		params := TxParams{
			ChainID:   big.NewInt(1),
			Nonce:     0,
			GasTipCap: big.NewInt(1),
			GasFeeCap: big.NewInt(1),
		}
		tx, _ := builder.Build(params)
		if tx.To() == nil || *tx.To() != contractAddr {
			t.Errorf("Transaction To = %v, want %v", tx.To(), contractAddr)
		}
	})

	t.Run("SetIterations", func(t *testing.T) {
		builder.SetIterations(500)
		// Verify by checking the builder's internal state or building a transaction
		// The iterations value affects the calldata
		params := TxParams{
			ChainID:   big.NewInt(1),
			Nonce:     0,
			GasTipCap: big.NewInt(1),
			GasFeeCap: big.NewInt(1),
		}
		tx, _ := builder.Build(params)
		// Data should be function selector (4 bytes) + iterations (32 bytes)
		if len(tx.Data()) != 36 {
			t.Errorf("Data length = %v, want 36", len(tx.Data()))
		}
	})

	t.Run("Build", func(t *testing.T) {
		b := NewHeavyComputeBuilder()
		contractAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
		b.SetContractAddress(contractAddr)

		params := TxParams{
			ChainID:   big.NewInt(42069),
			Nonce:     10,
			GasTipCap: big.NewInt(2000000000),
			GasFeeCap: big.NewInt(20000000000),
		}

		tx, err := b.Build(params)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if tx == nil {
			t.Fatal("Build() returned nil transaction")
		}

		if tx.Nonce() != 10 {
			t.Errorf("Nonce = %v, want 10", tx.Nonce())
		}
		if tx.Gas() != 500000 {
			t.Errorf("Gas = %v, want 500000", tx.Gas())
		}
		if tx.Value().Cmp(big.NewInt(0)) != 0 {
			t.Errorf("Value = %v, want 0", tx.Value())
		}
		// Verify function selector (first 4 bytes)
		if len(tx.Data()) < 4 {
			t.Fatalf("Data too short: %d bytes", len(tx.Data()))
		}
		expectedSelector := common.FromHex("0xa329e8de")
		for i := 0; i < 4; i++ {
			if tx.Data()[i] != expectedSelector[i] {
				t.Errorf("Selector byte %d = %x, want %x", i, tx.Data()[i], expectedSelector[i])
			}
		}
	})
}

func TestStorageWriteBuilder(t *testing.T) {
	builder := NewStorageWriteBuilder()

	t.Run("Type", func(t *testing.T) {
		if got := builder.Type(); got != types.TxTypeStorageWrite {
			t.Errorf("Type() = %v, want %v", got, types.TxTypeStorageWrite)
		}
	})

	t.Run("GasLimit", func(t *testing.T) {
		if got := builder.GasLimit(); got != 70000 {
			t.Errorf("GasLimit() = %v, want 70000", got)
		}
	})

	t.Run("RequiresContract", func(t *testing.T) {
		if !builder.RequiresContract() {
			t.Error("RequiresContract() should be true for storage write")
		}
	})

	t.Run("ContractBytecode", func(t *testing.T) {
		// StorageWriteBuilder returns nil - shares GasConsumer contract
		if bc := builder.ContractBytecode(); bc != nil {
			t.Error("ContractBytecode() should be nil (shares GasConsumer)")
		}
	})

	t.Run("SetContractAddress", func(t *testing.T) {
		contractAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
		builder.SetContractAddress(contractAddr)
		params := TxParams{
			ChainID:   big.NewInt(1),
			Nonce:     0,
			GasTipCap: big.NewInt(1),
			GasFeeCap: big.NewInt(1),
		}
		tx, _ := builder.Build(params)
		if tx.To() == nil || *tx.To() != contractAddr {
			t.Errorf("Transaction To = %v, want %v", tx.To(), contractAddr)
		}
	})

	t.Run("Build", func(t *testing.T) {
		b := NewStorageWriteBuilder()
		contractAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")
		b.SetContractAddress(contractAddr)

		params := TxParams{
			ChainID:   big.NewInt(42069),
			Nonce:     42,
			GasTipCap: big.NewInt(1000000000),
			GasFeeCap: big.NewInt(10000000000),
		}

		tx, err := b.Build(params)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		if tx == nil {
			t.Fatal("Build() returned nil transaction")
		}

		if tx.Nonce() != 42 {
			t.Errorf("Nonce = %v, want 42", tx.Nonce())
		}
		if tx.Gas() != 70000 {
			t.Errorf("Gas = %v, want 70000", tx.Gas())
		}
		// Verify function selector (first 4 bytes)
		if len(tx.Data()) < 4 {
			t.Fatalf("Data too short: %d bytes", len(tx.Data()))
		}
		expectedSelector := common.FromHex("0x6057361d")
		for i := 0; i < 4; i++ {
			if tx.Data()[i] != expectedSelector[i] {
				t.Errorf("Selector byte %d = %x, want %x", i, tx.Data()[i], expectedSelector[i])
			}
		}
	})
}

func TestEncodeStorageWrite(t *testing.T) {
	data := encodeStorageWrite(big.NewInt(12345))
	if len(data) != 36 {
		t.Errorf("encodeStorageWrite length = %d, want 36", len(data))
	}
	// Check function selector
	expectedSelector := common.FromHex("0x6057361d")
	for i := 0; i < 4; i++ {
		if data[i] != expectedSelector[i] {
			t.Errorf("Selector byte %d = %x, want %x", i, data[i], expectedSelector[i])
		}
	}
}

func TestEncodeHeavyCompute(t *testing.T) {
	data := encodeHeavyCompute(big.NewInt(1000))
	if len(data) != 36 {
		t.Errorf("encodeHeavyCompute length = %d, want 36", len(data))
	}
	// Check function selector
	expectedSelector := common.FromHex("0xa329e8de")
	for i := 0; i < 4; i++ {
		if data[i] != expectedSelector[i] {
			t.Errorf("Selector byte %d = %x, want %x", i, data[i], expectedSelector[i])
		}
	}
}

func TestBuilderInterface(t *testing.T) {
	// Verify all builders implement the Builder interface
	builders := []Builder{
		NewETHTransferBuilder(testRecipient),
		NewStorageWriteBuilder(),
		NewHeavyComputeBuilder(),
	}

	params := TxParams{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
	}

	for _, b := range builders {
		t.Run(string(b.Type()), func(t *testing.T) {
			// All methods should be callable
			_ = b.Type()
			_ = b.GasLimit()
			_ = b.RequiresContract()
			_ = b.ContractBytecode()
			b.SetContractAddress(common.Address{})

			tx, err := b.Build(params)
			if err != nil {
				t.Errorf("Build() error = %v", err)
			}
			if tx == nil {
				t.Error("Build() returned nil")
			}
		})
	}
}
