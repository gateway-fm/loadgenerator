package txbuilder

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// ERC20 function selectors
var (
	// transfer(address,uint256) = 0xa9059cbb
	erc20TransferSelector = common.FromHex("0xa9059cbb")
	// approve(address,uint256) = 0x095ea7b3
	erc20ApproveSelector = common.FromHex("0x095ea7b3")
)

// Test ERC20 contract bytecode - realistic gas usage (~70k), never reverts
//
// Solidity source (0.8.20):
//
//	contract ERC20 {
//	    mapping(address => uint256) public balanceOf;
//	    mapping(address => mapping(address => uint256)) public allowance;
//	    uint256 public totalSupply;
//	    event Transfer(address indexed from, address indexed to, uint256 value);
//	    event Approval(address indexed owner, address indexed spender, uint256 value);
//
//	    constructor() { totalSupply = type(uint256).max; }
//
//	    // Uses unchecked to prevent reverts on underflow - realistic ~65k gas
//	    function transfer(address to, uint256 amount) external returns (bool) {
//	        unchecked { balanceOf[msg.sender] -= amount; balanceOf[to] += amount; }
//	        emit Transfer(msg.sender, to, amount);
//	        return true;
//	    }
//	    function approve(address spender, uint256 amount) external returns (bool) {
//	        allowance[msg.sender][spender] = amount;
//	        emit Approval(msg.sender, spender, amount);
//	        return true;
//	    }
//	    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
//	        unchecked { allowance[from][msg.sender] -= amount; balanceOf[from] -= amount; balanceOf[to] += amount; }
//	        emit Transfer(from, to, amount);
//	        return true;
//	    }
//	}
//
// Function selectors:
//
//	transfer(address,uint256)              = 0xa9059cbb
//	approve(address,uint256)               = 0x095ea7b3
//	transferFrom(address,address,uint256)  = 0x23b872dd
//	balanceOf(address)                     = 0x70a08231
//	allowance(address,address)             = 0xdd62ed3e
//	totalSupply()                          = 0x18160ddd
var ERC20Bytecode = common.FromHex(
	// Compiled bytecode from Solidity 0.8.20 via forge - unchecked arithmetic (never reverts)
	"608060405234801561000f575f80fd5b507fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff60028190555061073c806100445f395ff3fe608060405234801561000f575f80fd5b5060043610610060575f3560e01c8063095ea7b31461006457806318160ddd1461009457806323b872dd146100b257806370a08231146100e2578063a9059cbb14610112578063dd62ed3e14610142575b5f80fd5b61007e600480360381019061007991906105b4565b610172565b60405161008b919061060c565b60405180910390f35b61009c61025f565b6040516100a99190610634565b60405180910390f35b6100cc60048036038101906100c7919061064d565b610265565b6040516100d9919061060c565b60405180910390f35b6100fc60048036038101906100f7919061069d565b6103ed565b6040516101099190610634565b60405180910390f35b61012c600480360381019061012791906105b4565b610401565b604051610139919061060c565b60405180910390f35b61015c600480360381019061015791906106c8565b610503565b6040516101699190610634565b60405180910390f35b5f8160015f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20819055508273ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff167f8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b9258460405161024d9190610634565b60405180910390a36001905092915050565b60025481565b5f8160015f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282540392505081905550815f808673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282540392505081905550815f808573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f82825401925050819055508273ffffffffffffffffffffffffffffffffffffffff168473ffffffffffffffffffffffffffffffffffffffff167fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef846040516103da9190610634565b60405180910390a3600190509392505050565b5f602052805f5260405f205f915090505481565b5f815f803373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282540392505081905550815f808573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f82825401925050819055508273ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff167fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef846040516104f19190610634565b60405180910390a36001905092915050565b6001602052815f5260405f20602052805f5260405f205f91509150505481565b5f80fd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f61055082610527565b9050919050565b61056081610546565b811461056a575f80fd5b50565b5f8135905061057b81610557565b92915050565b5f819050919050565b61059381610581565b811461059d575f80fd5b50565b5f813590506105ae8161058a565b92915050565b5f80604083850312156105ca576105c9610523565b5b5f6105d78582860161056d565b92505060206105e8858286016105a0565b9150509250929050565b5f8115159050919050565b610606816105f2565b82525050565b5f60208201905061061f5f8301846105fd565b92915050565b61062e81610581565b82525050565b5f6020820190506106475f830184610625565b92915050565b5f805f6060848603121561066457610663610523565b5b5f6106718682870161056d565b93505060206106828682870161056d565b9250506040610693868287016105a0565b9150509250925092565b5f602082840312156106b2576106b1610523565b5b5f6106bf8482850161056d565b91505092915050565b5f80604083850312156106de576106dd610523565b5b5f6106eb8582860161056d565b92505060206106fc8582860161056d565b915050925092905056fea26469706673582212203bf5cd39aee51811d687054a175115ba0eb43348972e4375ad2d0b659a764d4564736f6c63430008140033",
)

// encodeERC20Transfer encodes a transfer(address,uint256) call.
func encodeERC20Transfer(to common.Address, amount *big.Int) []byte {
	if amount.Sign() < 0 {
		panic("amount must be non-negative")
	}
	data := make([]byte, 4+32+32)
	copy(data[0:4], erc20TransferSelector)
	copy(data[4+12:4+32], to.Bytes())
	amount.FillBytes(data[4+32 : 4+64])
	return data
}

// encodeERC20Approve encodes an approve(address,uint256) call.
func encodeERC20Approve(spender common.Address, amount *big.Int) []byte {
	if amount.Sign() < 0 {
		panic("amount must be non-negative")
	}
	data := make([]byte, 4+32+32)
	copy(data[0:4], erc20ApproveSelector)
	copy(data[4+12:4+32], spender.Bytes())
	amount.FillBytes(data[4+32 : 4+64])
	return data
}

const (
	EnvERC20RecipientPool     = "ERC20_RECIPIENT_POOL"
	EnvERC20RecipientPoolSeed = "ERC20_RECIPIENT_POOL_SEED"

	defaultERC20RecipientPoolSeed = "gasstorm-erc20-recipient-pool-v1"
)

// ERC20TransferBuilder builds ERC20 transfer transactions.
//
// Recipients are fresh random addresses unless ERC20_RECIPIENT_POOL=N bounds
// them to N derived addresses. Pooled recipients repeat, so they pay a warm
// SSTORE (~30k gas) rather than a cold one (~52k): gas and tx/s from a pooled
// run are not comparable with an all-random one.
type ERC20TransferBuilder struct {
	contractAddress common.Address

	// poolSize is 0 for fully random recipients, else the pool cardinality.
	poolSize uint64
	poolSeed []byte

	// configErr is a malformed ERC20_RECIPIENT_POOL, reported from Build so a
	// bad value cannot present as the unbounded-random workload.
	configErr error
}

// NewERC20TransferBuilder creates a new ERC20 transfer builder.
// The recipient parameter is ignored - see ERC20TransferBuilder.
func NewERC20TransferBuilder(_ common.Address) *ERC20TransferBuilder {
	poolSize, seed, err := ERC20RecipientPoolFromEnv()
	b := newERC20TransferBuilderWithPool(poolSize, seed)
	b.configErr = err
	return b
}

func newERC20TransferBuilderWithPool(poolSize uint64, seed string) *ERC20TransferBuilder {
	return &ERC20TransferBuilder{
		poolSize: poolSize,
		poolSeed: []byte(seed),
	}
}

// ParseERC20RecipientPool validates ERC20_RECIPIENT_POOL.
//
// Unparseable and non-positive values are ERRORS rather than silently falling
// back to 0, because 0 is not a default here - it selects the unbounded-random
// workload. ERC20_RECIPIENT_POOL=1_000_000 would otherwise start cleanly, grow
// one holder per transfer, and report all-random gas and tx/s under a "1M pool"
// label. Failing at startup is cheaper than diagnosing that from a load-test
// result.
func ParseERC20RecipientPool(v string) (uint64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", EnvERC20RecipientPool, v, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid %s %q: must be positive", EnvERC20RecipientPool, v)
	}
	return uint64(n), nil
}

// ERC20RecipientPoolFromEnv reads both recipient-pool variables. An unset pool
// yields 0 with no error; a set but malformed one yields the error.
func ERC20RecipientPoolFromEnv() (uint64, string, error) {
	seed := defaultERC20RecipientPoolSeed
	if v := os.Getenv(EnvERC20RecipientPoolSeed); v != "" {
		seed = v
	}

	v := os.Getenv(EnvERC20RecipientPool)
	if v == "" {
		return 0, seed, nil
	}

	poolSize, err := ParseERC20RecipientPool(v)
	if err != nil {
		return 0, seed, err
	}
	return poolSize, seed, nil
}

// poolAddress derives addr(i) = sha256(seed || big-endian uint64(i))[:20], so
// a pool of any size costs no memory and is reproducible from (seed, N).
func (b *ERC20TransferBuilder) poolAddress(i uint64) common.Address {
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], i)

	h := sha256.New()
	h.Write(b.poolSeed)
	h.Write(idx[:])
	sum := h.Sum(nil)

	var addr common.Address
	copy(addr[:], sum[:common.AddressLength])
	return addr
}

// recipient returns the recipient for one transfer. math/rand/v2's top-level
// generator is goroutine-safe and lock-free per-thread; Build is called
// concurrently by the senders.
func (b *ERC20TransferBuilder) recipient() (common.Address, error) {
	if b.poolSize == 0 {
		var addr common.Address
		if _, err := crand.Read(addr[:]); err != nil {
			return common.Address{}, fmt.Errorf("generate random recipient: %w", err)
		}
		return addr, nil
	}
	return b.poolAddress(mrand.Uint64N(b.poolSize)), nil
}

// Type returns the transaction type identifier.
func (b *ERC20TransferBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeERC20Transfer
}

// GasLimit returns the gas limit for ERC20 transfer.
// Uses 70k for cold SSTORE (random recipient with zero balance ~52k + safe
// buffer), which bounds the cheaper pooled case too.
func (b *ERC20TransferBuilder) GasLimit() uint64 {
	return 70000
}

// Build creates an ERC20 transfer transaction.
func (b *ERC20TransferBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}
	if b.configErr != nil {
		return nil, b.configErr
	}
	recipient, err := b.recipient()
	if err != nil {
		return nil, err
	}

	data := encodeERC20Transfer(recipient, big.NewInt(1))

	return NewTransferTx(params.ChainID, params.Nonce, b.contractAddress, big.NewInt(0), b.GasLimit(), params.GasTipCap, params.GasFeeCap, data, params.UseLegacy), nil
}

// RequiresContract returns true - ERC20 needs a deployed contract.
func (b *ERC20TransferBuilder) RequiresContract() bool {
	return true
}

// ContractBytecode returns the ERC20 contract bytecode.
func (b *ERC20TransferBuilder) ContractBytecode() []byte {
	return ERC20Bytecode
}

// SetContractAddress sets the deployed contract address.
func (b *ERC20TransferBuilder) SetContractAddress(addr common.Address) {
	b.contractAddress = addr
}

// ERC20ApproveBuilder builds ERC20 approve transactions.
type ERC20ApproveBuilder struct {
	spender         common.Address
	contractAddress common.Address
}

// NewERC20ApproveBuilder creates a new ERC20 approve builder.
func NewERC20ApproveBuilder(spender common.Address) *ERC20ApproveBuilder {
	return &ERC20ApproveBuilder{
		spender: spender,
	}
}

// Type returns the transaction type identifier.
func (b *ERC20ApproveBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeERC20Approve
}

// GasLimit returns the gas limit for ERC20 approve (~60000).
func (b *ERC20ApproveBuilder) GasLimit() uint64 {
	return 60000
}

// Build creates an ERC20 approve transaction.
func (b *ERC20ApproveBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}
	// Approve max uint256
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	data := encodeERC20Approve(b.spender, maxUint256)

	return NewTransferTx(params.ChainID, params.Nonce, b.contractAddress, big.NewInt(0), b.GasLimit(), params.GasTipCap, params.GasFeeCap, data, params.UseLegacy), nil
}

// RequiresContract returns true - uses the same ERC20 contract.
func (b *ERC20ApproveBuilder) RequiresContract() bool {
	return true
}

// ContractBytecode returns nil - uses same contract as ERC20Transfer.
func (b *ERC20ApproveBuilder) ContractBytecode() []byte {
	return nil // Share ERC20 contract
}

// SetContractAddress sets the deployed contract address.
func (b *ERC20ApproveBuilder) SetContractAddress(addr common.Address) {
	b.contractAddress = addr
}
