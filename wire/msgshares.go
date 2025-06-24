package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var _ P2PoolMessage = &MsgShares{}

type Share struct {
	Type                 uint64
	MinHeader            SmallBlockHeader
	ShareInfo            ShareInfo
	RefMerkleLink        MerkleLink
	LastTxOutNonce       uint64
	HashLink             HashLink
	MerkleLink           MerkleLink
	Hash                 *chainhash.Hash
	POWHash              *chainhash.Hash
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

func (s *Share) IsValid() bool {
	if s.POWHash == nil || s.ShareInfo.Bits == 0 {
		return false
	}
	target := blockchain.CompactToBig(s.ShareInfo.Bits)
	bnHash := blockchain.HashToBig(s.POWHash)
	return bnHash.Cmp(target) <= 0
}

func (s *Share) ToBytes() ([]byte, error) {
	var finalBuf bytes.Buffer
	var contentsBuf bytes.Buffer
	var err error
	var zeroHash chainhash.Hash

	// This function needs to be a 1-to-1 match of the Python share packing logic.
	// The order of operations below is our best-guess and needs to be verified.

	err = WriteVarInt(&contentsBuf, uint64(s.MinHeader.Version))
	if err != nil {
		return nil, err
	}
	err = WritePossiblyNoneHash(&contentsBuf, s.MinHeader.PreviousBlock, &zeroHash)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.MinHeader.Timestamp)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.MinHeader.Bits)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.MinHeader.Nonce)
	if err != nil {
		return nil, err
	}

	err = WritePossiblyNoneHash(&contentsBuf, s.ShareInfo.ShareData.PreviousShareHash, &zeroHash)
	if err != nil {
		return nil, err
	}
	err = WriteVarString(&contentsBuf, s.ShareInfo.ShareData.CoinBase)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.ShareData.Nonce)
	if err != nil {
		return nil, err
	}
	err = WriteFixedBytes(&contentsBuf, s.ShareInfo.ShareData.PubKeyHash, 20)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.ShareData.Subsidy)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.ShareData.Donation)
	if err != nil {
		return nil, err
	}
	err = WriteStaleInfo(&contentsBuf, s.ShareInfo.ShareData.StaleInfo)
	if err != nil {
		return nil, err
	}
	err = WriteVarInt(&contentsBuf, s.ShareInfo.ShareData.DesiredVersion)
	if err != nil {
		return nil, err
	}

	if s.Type >= 17 {
		if s.ShareInfo.SegwitData != nil {
			err = WriteChainHashList(&contentsBuf, s.ShareInfo.SegwitData.TXIDMerkleLink.Branch)
			if err != nil {
				return nil, err
			}
			err = WriteChainHash(&contentsBuf, s.ShareInfo.SegwitData.WTXIDMerkleRoot)
			if err != nil {
				return nil, err
			}
		}
	} else {
		err = WriteChainHashList(&contentsBuf, s.ShareInfo.NewTransactionHashes)
		if err != nil {
			return nil, err
		}
		err = WriteTransactionHashRefs(&contentsBuf, s.ShareInfo.TransactionHashRefs)
		if err != nil {
			return nil, err
		}
	}

	err = WritePossiblyNoneHash(&contentsBuf, s.ShareInfo.FarShareHash, &zeroHash)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.MaxBits)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.Bits)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.Timestamp)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&contentsBuf, binary.LittleEndian, s.ShareInfo.AbsHeight)
	if err != nil {
		return nil, err
	}
	absWorkBytes := s.ShareInfo.AbsWork.Bytes()
	paddedAbsWork := make([]byte, 16)
	copy(paddedAbsWork[16-len(absWorkBytes):], absWorkBytes)
	_, err = contentsBuf.Write(paddedAbsWork)
	if err != nil {
		return nil, err
	}

	err = WriteChainHashList(&contentsBuf, s.RefMerkleLink.Branch)
	if err != nil {
		return nil, err
	}
	err = WriteVarInt(&contentsBuf, s.LastTxOutNonce)
	if err != nil {
		return nil, err
	}

	err = WriteFixedBytes(&contentsBuf, s.HashLink.State, 32)
	if err != nil {
		return nil, err
	}
	err = WriteFixedBytes(&contentsBuf, s.HashLink.ExtraData, 0)
	if err != nil {
		return nil, err
	}
	err = WriteVarInt(&contentsBuf, s.HashLink.Length)
	if err != nil {
		return nil, err
	}

	err = WriteChainHashList(&contentsBuf, s.MerkleLink.Branch)
	if err != nil {
		return nil, err
	}

	err = WriteVarInt(&finalBuf, s.Type)
	if err != nil {
		return nil, err
	}
	err = WriteVarInt(&finalBuf, uint64(contentsBuf.Len()))
	if err != nil {
		return nil, err
	}
	_, err = finalBuf.Write(contentsBuf.Bytes())
	if err != nil {
		return nil, err
	}

	return finalBuf.Bytes(), nil
}

func (s *Share) FromBytes(r io.Reader) error {
	var err error

	s.Type, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed reading share Type: %v", err)
	}
	_, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed reading share contents length: %v", err)
	}

	// This function needs to be a 1-to-1 match of the Python share unpacking logic.
	// The order of operations below is our best-guess and needs to be verified.

	version, err := ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Version: %v", err)
	}
	s.MinHeader.Version = int32(version)

	s.MinHeader.PreviousBlock, err = ReadPossiblyNoneHash(r)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.PreviousBlock: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Timestamp)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Timestamp: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Bits)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Bits: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Nonce)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Nonce: %v", err)
	}

	s.ShareInfo.ShareData.PreviousShareHash, err = ReadPossiblyNoneHash(r)
	if err != nil {
		return fmt.Errorf("failed on ShareData.PreviousShareHash: %v", err)
	}
	s.ShareInfo.ShareData.CoinBase, err = ReadVarString(r)
	if err != nil {
		return fmt.Errorf("failed on ShareData.CoinBase: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Nonce)
	if err != nil {
		return fmt.Errorf("failed on ShareData.Nonce: %v", err)
	}
	s.ShareInfo.ShareData.PubKeyHash, err = ReadFixedBytes(r, 20)
	if err != nil {
		return fmt.Errorf("failed on ShareData.PubKeyHash: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil {
		return fmt.Errorf("failed on ShareData.PubKeyHashVersion: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Subsidy)
	if err != nil {
		return fmt.Errorf("failed on ShareData.Subsidy: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Donation)
	if err != nil {
		return fmt.Errorf("failed on ShareData.Donation: %v", err)
	}
	s.ShareInfo.ShareData.StaleInfo, err = ReadStaleInfo(r)
	if err != nil {
		return fmt.Errorf("failed on ShareData.StaleInfo: %v", err)
	}
	s.ShareInfo.ShareData.DesiredVersion, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed on ShareData.DesiredVersion: %v", err)
	}

	if s.Type >= 17 {
		var tempSegwitData SegwitData
		tempSegwitData.TXIDMerkleLink.Branch, err = ReadChainHashList(r)
		if err != nil {
			return fmt.Errorf("failed reading SegwitData.Branch: %v", err)
		}
		tempSegwitData.TXIDMerkleLink.Index = 0
		tempSegwitData.WTXIDMerkleRoot, err = ReadChainHash(r)
		if err != nil {
			return fmt.Errorf("failed reading SegwitData.WTXIDMerkleRoot: %v", err)
		}
		s.ShareInfo.SegwitData = &tempSegwitData
	} else {
		s.ShareInfo.NewTransactionHashes, err = ReadChainHashList(r)
		if err != nil {
			return fmt.Errorf("failed on NewTransactionHashes: %v", err)
		}
		s.ShareInfo.TransactionHashRefs, err = ReadTransactionHashRefs(r)
		if err != nil {
			return fmt.Errorf("failed on TransactionHashRefs: %v", err)
		}
	}

	s.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(r)
	if err != nil {
		return fmt.Errorf("failed on FarShareHash: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.MaxBits)
	if err != nil {
		return fmt.Errorf("failed on MaxBits: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.Bits)
	if err != nil {
		return fmt.Errorf("failed on Bits: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.Timestamp)
	if err != nil {
		return fmt.Errorf("failed on Timestamp: %v", err)
	}
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.AbsHeight)
	if err != nil {
		return fmt.Errorf("failed on AbsHeight: %v", err)
	}
	absWorkBytes := make([]byte, 16)
	_, err = io.ReadFull(r, absWorkBytes)
	if err != nil {
		return fmt.Errorf("failed on AbsWork: %v", err)
	}
	s.ShareInfo.AbsWork = new(big.Int).SetBytes(absWorkBytes)

	s.RefMerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil {
		return fmt.Errorf("failed on RefMerkleLink.Branch: %v", err)
	}
	s.RefMerkleLink.Index = 0
	s.LastTxOutNonce, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed on LastTxOutNonce: %v", err)
	}
	s.HashLink.State, err = ReadFixedBytes(r, 32)
	if err != nil {
		return fmt.Errorf("failed on HashLink.State: %v", err)
	}
	s.HashLink.ExtraData, err = ReadFixedBytes(r, 0)
	if err != nil {
		return fmt.Errorf("failed on HashLink.ExtraData: %v", err)
	}
	s.HashLink.Length, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed on HashLink.Length: %v", err)
	}
	s.MerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil {
		return fmt.Errorf("failed on MerkleLink.Branch: %v", err)
	}
	s.MerkleLink.Index = 0

	return nil
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
