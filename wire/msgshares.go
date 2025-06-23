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

// Top-level share, representing the (Type, Contents) wrapper
type Share struct {
	Type     uint64
	Contents ShareContents
	Hash     *chainhash.Hash
	POWHash  *chainhash.Hash
}

// Represents the data inside the `contents` VarStr
type ShareContents struct {
	MinHeader      SmallBlockHeader
	ShareInfo      ShareInfo
	RefMerkleLink  MerkleLink
	LastTxOutNonce uint64
	HashLink       HashLink
	MerkleLink     MerkleLink
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

// Corrected ShareInfo structure
type ShareInfo struct {
	ShareData            ShareData
	SegwitData           *SegwitData // Correctly nested
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
	if s.POWHash == nil || s.Contents.ShareInfo.Bits == 0 {
		return false
	}
	target := blockchain.CompactToBig(s.Contents.ShareInfo.Bits)
	bnHash := blockchain.HashToBig(s.POWHash)
	return bnHash.Cmp(target) <= 0
}

func (s *Share) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteVarInt(&buf, s.Type)
	if err != nil {
		return nil, err
	}
	contentsBytes, err := s.Contents.ToBytes(s.Type)
	if err != nil {
		return nil, err
	}
	err = WriteVarString(&buf, contentsBytes)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (sc *ShareContents) ToBytes(shareType uint64) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	var zeroHash chainhash.Hash

	err = WriteVarInt(&buf, uint64(sc.MinHeader.Version))
	if err != nil { return nil, err }
	err = WritePossiblyNoneHash(&buf, sc.MinHeader.PreviousBlock, &zeroHash)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.MinHeader.Timestamp)
	if err != nil { return nil, err }
	err = WriteFloatingInteger(&buf, sc.MinHeader.Bits)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.MinHeader.Nonce)
	if err != nil { return nil, err }

	err = WritePossiblyNoneHash(&buf, sc.ShareInfo.ShareData.PreviousShareHash, &zeroHash)
	if err != nil { return nil, err }
	err = WriteVarString(&buf, sc.ShareInfo.ShareData.CoinBase)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.ShareInfo.ShareData.Nonce)
	if err != nil { return nil, err }
	err = WriteFixedBytes(&buf, sc.ShareInfo.ShareData.PubKeyHash, 20)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.ShareInfo.ShareData.Subsidy)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.ShareInfo.ShareData.Donation)
	if err != nil { return nil, err }
	err = WriteStaleInfo(&buf, sc.ShareInfo.ShareData.StaleInfo)
	if err != nil { return nil, err }
	err = WriteVarInt(&buf, sc.ShareInfo.ShareData.DesiredVersion)
	if err != nil { return nil, err }

	if shareType >= 16 {
		if sc.ShareInfo.SegwitData != nil {
			err = WriteChainHashList(&buf, sc.ShareInfo.SegwitData.TXIDMerkleLink.Branch)
			if err != nil { return nil, err }
			err = WriteChainHash(&buf, sc.ShareInfo.SegwitData.WTXIDMerkleRoot)
			if err != nil { return nil, err }
		} else {
			err = WriteVarInt(&buf, 0)
			if err != nil { return nil, err }
			noneWTXIDMerkleRoot, _ := chainhash.NewHashFromStr("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			err = WriteChainHash(&buf, noneWTXIDMerkleRoot)
			if err != nil { return nil, err }
		}
	}

	err = WriteChainHashList(&buf, sc.ShareInfo.NewTransactionHashes)
	if err != nil { return nil, err }
	err = WriteTransactionHashRefs(&buf, sc.ShareInfo.TransactionHashRefs)
	if err != nil { return nil, err }
	err = WritePossiblyNoneHash(&buf, sc.ShareInfo.FarShareHash, &zeroHash)
	if err != nil { return nil, err }
	err = WriteFloatingInteger(&buf, sc.ShareInfo.MaxBits)
	if err != nil { return nil, err }
	err = WriteFloatingInteger(&buf, sc.ShareInfo.Bits)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.ShareInfo.Timestamp)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, sc.ShareInfo.AbsHeight)
	if err != nil { return nil, err }
	err = WriteInt(&buf, sc.ShareInfo.AbsWork, 128, binary.LittleEndian)
	if err != nil { return nil, err }
	
	err = WriteChainHashList(&buf, sc.RefMerkleLink.Branch)
	if err != nil { return nil, err }

	err = binary.Write(&buf, binary.LittleEndian, sc.LastTxOutNonce)
	if err != nil { return nil, err }
	
	err = WriteFixedBytes(&buf, sc.HashLink.State, 32)
	if err != nil { return nil, err }
	err = WriteFixedBytes(&buf, sc.HashLink.ExtraData, 0)
	if err != nil { return nil, err }
	err = WriteVarInt(&buf, sc.HashLink.Length)
	if err != nil { return nil, err }
	
	err = WriteChainHashList(&buf, sc.MerkleLink.Branch)
	if err != nil { return nil, err }

	return buf.Bytes(), nil
}

func (s *Share) FromBytes(r io.Reader) error {
	var err error
	s.Type, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("error reading share type: %v", err)
	}
	contentsBytes, err := ReadVarString(r)
	if err != nil {
		return fmt.Errorf("error reading share contents: %v", err)
	}
	contentsReader := bytes.NewReader(contentsBytes)
	return s.Contents.FromBytes(contentsReader, s.Type)
}

func (sc *ShareContents) FromBytes(r io.Reader, shareType uint64) error {
	var err error
	var zeroHash chainhash.Hash

	// --- 1. Read MinHeader ---
	version, err := ReadVarInt(r)
	if err != nil { return fmt.Errorf("failed on MinHeader.Version: %v", err) }
	sc.MinHeader.Version = int32(version)
	sc.MinHeader.PreviousBlock, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return fmt.Errorf("failed on MinHeader.PreviousBlock: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.MinHeader.Timestamp)
	if err != nil { return fmt.Errorf("failed on MinHeader.Timestamp: %v", err) }
	sc.MinHeader.Bits, err = ReadFloatingInteger(r)
	if err != nil { return fmt.Errorf("failed on MinHeader.Bits: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.MinHeader.Nonce)
	if err != nil { return fmt.Errorf("failed on MinHeader.Nonce: %v", err) }

	// --- 2. Read ShareInfo fields ---
	
	// 2a. Read ShareData sub-struct
	sc.ShareInfo.ShareData.PreviousShareHash, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return fmt.Errorf("failed on ShareData.PreviousShareHash: %v", err) }
	sc.ShareInfo.ShareData.CoinBase, err = ReadVarString(r)
	if err != nil { return fmt.Errorf("failed on ShareData.CoinBase: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.ShareInfo.ShareData.Nonce)
	if err != nil { return fmt.Errorf("failed on ShareData.Nonce: %v", err) }
	sc.ShareInfo.ShareData.PubKeyHash, err = ReadFixedBytes(r, 20)
	if err != nil { return fmt.Errorf("failed on ShareData.PubKeyHash: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil { return fmt.Errorf("failed on ShareData.PubKeyHashVersion: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.ShareInfo.ShareData.Subsidy)
	if err != nil { return fmt.Errorf("failed on ShareData.Subsidy: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.ShareInfo.ShareData.Donation)
	if err != nil { return fmt.Errorf("failed on ShareData.Donation: %v", err) }
	sc.ShareInfo.ShareData.StaleInfo, err = ReadStaleInfo(r)
	if err != nil { return fmt.Errorf("failed on ShareData.StaleInfo: %v", err) }
	sc.ShareInfo.ShareData.DesiredVersion, err = ReadVarInt(r)
	if err != nil { return fmt.Errorf("failed on ShareData.DesiredVersion: %v", err) }

	// 2b. Read SegwitData
	if shareType >= 16 {
		// This logic correctly mimics the legacy Python's "PossiblyNoneType"
		var tempSegwitData SegwitData
		
		// Step 1: Always attempt to read the fields of a SegwitData block.
		tempSegwitData.TXIDMerkleLink.Branch, err = ReadChainHashList(r)
		if err != nil {
			return fmt.Errorf("failed on SegwitData.Branch: %v", err)
		}
		tempSegwitData.TXIDMerkleLink.Index = 0 // The index is always present as a zero-byte value
	
		tempSegwitData.WTXIDMerkleRoot, err = ReadChainHash(r)
		if err != nil {
			return fmt.Errorf("failed on SegwitData.WTXIDMerkleRoot: %v", err)
		}
	
		// Step 2: Now, check if the data we just read is the special "None" value.
		// The "None" value is an empty branch list AND a wtxid_merkle_root of all 1s.
		isNoneValue := false
		if len(tempSegwitData.TXIDMerkleLink.Branch) == 0 {
			noneHash, _ := chainhash.NewHashFromStr("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			if tempSegwitData.WTXIDMerkleRoot.IsEqual(noneHash) {
				isNoneValue = true
			}
		}
	
		// Step 3: Only assign the SegwitData if it was not the "None" value.
		// If it *was* the "None" value, we have correctly consumed it from the stream
		// and now we simply discard it, leaving the parser in the correct position.
		if !isNoneValue {
			sc.ShareInfo.SegwitData = &tempSegwitData
		}
	}

	// 2c. Read the rest of the ShareInfo fields
	sc.ShareInfo.NewTransactionHashes, err = ReadChainHashList(r)
	if err != nil { return fmt.Errorf("failed on NewTransactionHashes: %v", err) }
	sc.ShareInfo.TransactionHashRefs, err = ReadTransactionHashRefs(r)
	if err != nil { return fmt.Errorf("failed on TransactionHashRefs: %v", err) }
	sc.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return fmt.Errorf("failed on FarShareHash: %v", err) }
	sc.ShareInfo.MaxBits, err = ReadFloatingInteger(r)
	if err != nil { return fmt.Errorf("failed on MaxBits: %v", err) }
	sc.ShareInfo.Bits, err = ReadFloatingInteger(r)
	if err != nil { return fmt.Errorf("failed on Bits: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.ShareInfo.Timestamp)
	if err != nil { return fmt.Errorf("failed on Timestamp: %v", err) }
	err = binary.Read(r, binary.LittleEndian, &sc.ShareInfo.AbsHeight)
	if err != nil { return fmt.Errorf("failed on AbsHeight: %v", err) }
	sc.ShareInfo.AbsWork, err = ReadInt(r, 128, binary.LittleEndian)
	if err != nil { return fmt.Errorf("failed on AbsWork: %v", err) }

	// --- 3. Read the remaining top-level Contents fields ---
	sc.RefMerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil { return fmt.Errorf("failed on RefMerkleLink.Branch: %v", err) }
	sc.RefMerkleLink.Index = 0

	err = binary.Read(r, binary.LittleEndian, &sc.LastTxOutNonce)
	if err != nil { return fmt.Errorf("failed on LastTxOutNonce: %v", err) }

	sc.HashLink.State, err = ReadFixedBytes(r, 32)
	if err != nil { return fmt.Errorf("failed on HashLink.State: %v", err) }
	sc.HashLink.ExtraData, err = ReadFixedBytes(r, 0)
	if err != nil { return fmt.Errorf("failed on HashLink.ExtraData: %v", err) }
	sc.HashLink.Length, err = ReadVarInt(r)
	if err != nil { return fmt.Errorf("failed on HashLink.Length: %v", err) }

	sc.MerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil { return fmt.Errorf("failed on MerkleLink.Branch: %v", err) }
	sc.MerkleLink.Index = 0

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
