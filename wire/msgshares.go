package wire

import (
	"bytes"
	"encoding/hex"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var _ P2PoolMessage = &MsgShares{}

type MsgShares struct {
	Shares []Share
}

type Share struct {
	Type           uint64
	MinHeader      SmallBlockHeader
	ShareInfo      ShareInfo
	RefMerkleLink  []*chainhash.Hash
	LastTxOutNonce uint64
	HashLink       HashLink
	MerkleLink     []*chainhash.Hash
	GenTXHash      *chainhash.Hash
	MerkleRoot     *chainhash.Hash
	RefHash        *chainhash.Hash
	Hash           *chainhash.Hash
	POWHash        *chainhash.Hash
}

type HashLink struct {
	State  string
	Length uint64
}

type SmallBlockHeader struct {
	Version       int32
	PreviousBlock *chainhash.Hash
	Timestamp     uint32
	Bits          uint32
	Nonce         uint32
}

type Ref struct {
	Identifier string
	ShareInfo  ShareInfo
}

type ShareInfo struct {
	ShareData            ShareData
	SegwitData           SegwitData
	NewTransactionHashes []*chainhash.Hash
	TransactionHashRefs  []TransactionHashRef
	FarShareHash         *chainhash.Hash
	MaxBits              int32
	Bits                 int32
	Timestamp            int32
	AbsHeight            int32
	AbsWork              *big.Int
}

type TransactionHashRef struct {
	ShareCount uint64
	TxCount    uint64
}

type ShareData struct {
	PreviousShareHash *chainhash.Hash
	CoinBase          string
	Nonce             uint32
	PubKeyHash        []byte
	PubKeyHashVersion uint8
	Subsidy           uint64
	Donation          uint16
	StaleInfo         StaleInfo
	DesiredVersion    uint64
}

type StaleInfo uint8

const (
	StaleInfoNone   = StaleInfo(0)
	StaleInfoOrphan = StaleInfo(253)
	StaleInfoDOA    = StaleInfo(254)
)

var DonationScript []byte
var GenTxBeforeRefHash []byte

type SegwitData struct {
	TXIDMerkleLink  []*chainhash.Hash
	WTXIDMerkleRoot *chainhash.Hash
}

func (s *Share) IsValid() bool {
	if s.POWHash == nil || s.ShareInfo.Bits == 0 {
		return false
	}
	target := blockchain.CompactToBig(uint32(s.ShareInfo.Bits))
	bnHash := blockchain.HashToBig(s.POWHash)
	return bnHash.Cmp(target) <= 0
}

func (m *MsgShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Shares, err = ReadShares(r)
	if err != nil {
		logging.Errorf("Failed to deserialize shares message: %v", err)
	}
	return err
}

func (m *MsgShares) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteShares(&buf, m.Shares)
	return buf.Bytes(), err
}

func (m *MsgShares) Command() string {
	return "shares"
}

func init() {
	DonationScript, _ = hex.DecodeString("410418a74130b2f4fad899d8ed2bff272bc43a03c8ca72897ae3da584d7a770b5a9ea8dd1b37a620d27c6cf6d5a7a9bbd6872f5981e95816d701d94f201c5d093be6ac")
	GenTxBeforeRefHash = make([]byte, len(DonationScript)+12)
	copy(GenTxBeforeRefHash, []byte{byte(len(DonationScript))})
	copy(GenTxBeforeRefHash[1:], DonationScript)
	copy(GenTxBeforeRefHash[len(DonationScript)+9:], []byte{42, 0x6A, 0x28})
}

