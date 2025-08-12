package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/util"
)

func legacyHeaderSerialize(w io.Writer, header *wire.BlockHeader) error {
	err := binary.Write(w, binary.LittleEndian, header.Version)
	if err != nil {
		return err
	}
	if _, err := w.Write(util.ReverseBytes(header.PrevBlock.CloneBytes())); err != nil {
		return err
	}
	if _, err := w.Write(util.ReverseBytes(header.MerkleRoot.CloneBytes())); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(header.Timestamp.Unix())); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, header.Bits); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, header.Nonce); err != nil {
		return err
	}
	return nil
}

type Share struct {
	Type           uint64
	MinHeader      SmallBlockHeader
	ShareInfo      ShareInfo
	RefMerkleLink  MerkleLink
	LastTxOutNonce uint64
	HashLink       HashLink
	MerkleLink     MerkleLink
	Hash           *chainhash.Hash
	POWHash        *chainhash.Hash
	Target         *big.Int
}

func (s *Share) FullBlockHeader() (*wire.BlockHeader, error) {
	coinbaseTxHashBytes := util.Sha256d(s.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)
	merkleRoot := util.ComputeMerkleRootFromLink(coinbaseTxHash, s.MerkleLink.Branch, s.MerkleLink.Index)

	header := &wire.BlockHeader{
		Version:    s.MinHeader.Version,
		PrevBlock:  *s.MinHeader.PreviousBlock,
		MerkleRoot: *merkleRoot,
		Timestamp:  time.Unix(int64(s.MinHeader.Timestamp), 0),
		Bits:       s.MinHeader.Bits,
		Nonce:      s.MinHeader.Nonce,
	}

	return header, nil
}

func (s *Share) CalculateHashes() error {
	header, err := s.FullBlockHeader()
	if err != nil {
		return fmt.Errorf("could not construct header to calculate PoW: %v", err)
	}

	var hdrBuf bytes.Buffer
	err = legacyHeaderSerialize(&hdrBuf, header)
	if err != nil {
		return fmt.Errorf("could not serialize header for PoW: %v", err)
	}

	powBytesLE, err := p2pnet.ActiveNetwork.Verthash.Hash(hdrBuf.Bytes())
	if err != nil {
		return fmt.Errorf("verthash failed during PoW recalculation: %v", err)
	}

	s.POWHash, _ = chainhash.NewHash(util.ReverseBytes(powBytesLE))
	s.Hash, _ = chainhash.NewHash(util.Sha256d(hdrBuf.Bytes()))
	return nil
}


func (s *Share) IsValid() (bool, string) {
	if s.POWHash == nil {
		return false, "share has no PoW hash to validate"
	}
	if s.Target == nil || s.Target.Sign() <= 0 {
		return false, "target is zero or negative"
	}

	hashInt := new(big.Int).SetBytes(s.POWHash.CloneBytes())
	if hashInt.Cmp(s.Target) <= 0 {
		return true, ""
	}

	return false, "hash is greater than target"
}

type MerkleLink struct {
	Branch []*chainhash.Hash
	Index  uint64
}

type HashLink struct {
	State     []byte
	ExtraData []byte
	Length    uint64
}

type SmallBlockHeader struct {
	Version       int32
	PreviousBlock *chainhash.Hash
	Timestamp     uint32
	Bits          uint32
	Nonce         uint32
}

type ShareInfo struct {
	ShareData            ShareData
	SegwitData           *SegwitData
	NewTransactionHashes []*chainhash.Hash
	TransactionHashRefs  []TransactionHashRef
	FarShareHash         *chainhash.Hash
	MaxBits              uint32
	Bits                 uint32
	Timestamp            int32
	AbsHeight            int32
	AbsWork              *big.Int
}

type SegwitData struct {
	TXIDMerkleLink  MerkleLink
	WTXIDMerkleRoot *chainhash.Hash
}

type TransactionHashRef struct {
	ShareCount uint64
	TxCount    uint64
}

type ShareData struct {
	PreviousShareHash *chainhash.Hash
	CoinBase          []byte
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

type MsgShares struct {
	Shares []Share
}


func (m *MsgShares) FromBytes(b []byte) error { return nil } 
func (m *MsgShares) ToBytes() ([]byte, error) { return nil, nil } 
func (m *MsgShares) Command() string { return "shares" }
