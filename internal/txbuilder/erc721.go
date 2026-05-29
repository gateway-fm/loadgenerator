package txbuilder

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	ptypes "github.com/gateway-fm/loadgenerator/pkg/types"
)

// ERC721 function selectors
var (
	// transferFrom(address,address,uint256) = 0x23b872dd
	erc721TransferFromSelector = common.FromHex("0x23b872dd")
	// mint(address,uint256) = 0x40c10f19
	erc721MintSelector = common.FromHex("0x40c10f19")
)

// Test ERC721 contract bytecode - permissive (no ownership/approval checks), never reverts.
//
// Solidity source (^0.8.20, optimizer enabled, runs=200):
//
//	contract NFT {
//	    mapping(uint256 => address) public ownerOf;
//	    mapping(address => uint256) public balanceOf;
//	    event Transfer(address indexed from, address indexed to, uint256 indexed tokenId);
//
//	    function mint(address to, uint256 tokenId) external {
//	        ownerOf[tokenId] = to;
//	        unchecked { balanceOf[to]++; }
//	        emit Transfer(address(0), to, tokenId);
//	    }
//
//	    function transferFrom(address from, address to, uint256 tokenId) external {
//	        ownerOf[tokenId] = to;
//	        unchecked {
//	            balanceOf[from]--;
//	            balanceOf[to]++;
//	        }
//	        emit Transfer(from, to, tokenId);
//	    }
//	}
//
// Function selectors:
//
//	transferFrom(address,address,uint256) = 0x23b872dd
//	mint(address,uint256)                  = 0x40c10f19
//	ownerOf(uint256)                       = 0x6352211e
//	balanceOf(address)                     = 0x70a08231
var NFTBytecode = common.FromHex(
	"6080604052348015600e575f5ffd5b506102bb8061001c5f395ff3fe608060405234801561000f575f5ffd5b506004361061004a575f3560e01c806323b872dd1461004e57806340c10f19146100635780636352211e1461007657806370a08231146100bb575b5f5ffd5b61006161005c3660046101ec565b6100e8565b005b610061610071366004610226565b610166565b61009e61008436600461024e565b5f602081905290815260409020546001600160a01b031681565b6040516001600160a01b0390911681526020015b60405180910390f35b6100da6100c9366004610265565b60016020525f908152604090205481565b6040519081526020016100b2565b5f8181526020818152604080832080546001600160a01b0319166001600160a01b0387811691821790925590871680855260019384905282852080545f190190558185528285208054909401909355905184939192917fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef91a4505050565b5f8181526020818152604080832080546001600160a01b0319166001600160a01b0387169081179091558084526001928390528184208054909301909255518392907fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef908290a45050565b80356001600160a01b03811681146101e7575f5ffd5b919050565b5f5f5f606084860312156101fe575f5ffd5b610207846101d1565b9250610215602085016101d1565b929592945050506040919091013590565b5f5f60408385031215610237575f5ffd5b610240836101d1565b946020939093013593505050565b5f6020828403121561025e575f5ffd5b5035919050565b5f60208284031215610275575f5ffd5b61027e826101d1565b939250505056fea2646970667358221220d2a1f2ba316c443f1f1acdab7ef7ad65ba7967b15d1eb52f06de51b445fa7bbd64736f6c634300081d0033",
)

// encodeERC721Mint encodes a mint(address,uint256) call.
func encodeERC721Mint(to common.Address, tokenID *big.Int) []byte {
	if tokenID.Sign() < 0 {
		panic("tokenID must be non-negative")
	}
	data := make([]byte, 4+32+32)
	copy(data[0:4], erc721MintSelector)
	copy(data[4+12:4+32], to.Bytes())
	tokenID.FillBytes(data[4+32 : 4+64])
	return data
}

// encodeERC721TransferFrom encodes a transferFrom(address,address,uint256) call.
func encodeERC721TransferFrom(from, to common.Address, tokenID *big.Int) []byte {
	if tokenID.Sign() < 0 {
		panic("tokenID must be non-negative")
	}
	data := make([]byte, 4+32+32+32)
	copy(data[0:4], erc721TransferFromSelector)
	copy(data[4+12:4+32], from.Bytes())
	copy(data[4+32+12:4+64], to.Bytes())
	tokenID.FillBytes(data[4+64 : 4+96])
	return data
}

// ERC721TransferBuilder builds ERC721 transferFrom transactions.
// Each transfer uses a random from, random to, and random uint256 tokenId
// to simulate cold SSTORE costs on the _owners mapping (~50-70k gas).
// The deployed contract is permissive: it does not check ownership/approval,
// so transfers never revert regardless of who owns the token.
type ERC721TransferBuilder struct {
	contractAddress common.Address
}

// NewERC721TransferBuilder creates a new ERC721 transfer builder.
func NewERC721TransferBuilder() *ERC721TransferBuilder {
	return &ERC721TransferBuilder{}
}

// Type returns the transaction type identifier.
func (b *ERC721TransferBuilder) Type() ptypes.TransactionType {
	return ptypes.TxTypeERC721Transfer
}

// GasLimit returns the gas limit for ERC721 transferFrom.
// Random tokenId → cold _owners SSTORE (~22k) + two balanceOf SSTOREs + event ≈ 70k worst case.
func (b *ERC721TransferBuilder) GasLimit() uint64 {
	return 100000
}

// Build creates an ERC721 transferFrom transaction with the loadgen sender as
// `from` and a random `to` / random `tokenId`. Using the sender as `from`
// mirrors how ERC20.transfer emits its Transfer event (from = msg.sender), so
// when the privacy-proxy redactor runs over the resulting Transfer log it sees
// at least one identifiable participant and keeps the row (rendering the
// random `to` as [PRIVATE]). With random-on-both-sides, every transfer was
// dropped wholesale because the redactor treats two-Hidden-sides transfers as
// noise it must filter out. The random `to` and random `tokenId` still hit
// cold SSTOREs on balanceOf[to] and ownerOf[tokenId], so the gas-cost goal of
// this builder is unchanged.
func (b *ERC721TransferBuilder) Build(params TxParams) (*types.Transaction, error) {
	if params.ChainID == nil || params.ChainID.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("ChainID must be non-nil and non-zero")
	}

	from := params.From
	var to common.Address
	rand.Read(to[:])

	var idBytes [32]byte
	rand.Read(idBytes[:])
	tokenID := new(big.Int).SetBytes(idBytes[:])

	data := encodeERC721TransferFrom(from, to, tokenID)

	return NewTransferTx(params.ChainID, params.Nonce, b.contractAddress, big.NewInt(0), b.GasLimit(), params.GasTipCap, params.GasFeeCap, data, params.UseLegacy), nil
}

// RequiresContract returns true - ERC721 needs a deployed contract.
func (b *ERC721TransferBuilder) RequiresContract() bool {
	return true
}

// ContractBytecode returns the NFT contract bytecode.
func (b *ERC721TransferBuilder) ContractBytecode() []byte {
	return NFTBytecode
}

// SetContractAddress sets the deployed contract address.
func (b *ERC721TransferBuilder) SetContractAddress(addr common.Address) {
	b.contractAddress = addr
}

// BuildMintTx builds a mint(to, tokenId) transaction against the NFT contract.
// Used by the pre-mint setup phase (not part of the load-test Builder interface).
func BuildMintTx(chainID *big.Int, nonce uint64, contract, to common.Address, tokenID *big.Int, gasTipCap, gasFeeCap *big.Int, useLegacy bool) *types.Transaction {
	const mintGasLimit uint64 = 100000
	data := encodeERC721Mint(to, tokenID)
	return NewTransferTx(chainID, nonce, contract, big.NewInt(0), mintGasLimit, gasTipCap, gasFeeCap, data, useLegacy)
}
