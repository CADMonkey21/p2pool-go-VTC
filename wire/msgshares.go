package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var _ P2PoolMessage = &MsgShares{}

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
	GenTXHash      *chainhash.Hash
	MerkleRoot     *chainhash.Hash
	RefHash        *chainhash.Hash
}

type MerkleLink struct {
	Branch []*chainhash.Hash
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
	var buf bytes.Buffer
	var err error

	err = WriteVarInt(&buf, uint64(s.MinHeader.Version))
	if err != nil { return nil, err }
	var zeroHash chainhash.Hash
	err = WritePossiblyNoneHash(&buf, s.MinHeader.PreviousBlock, &zeroHash)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.MinHeader.Timestamp)
	if err != nil { return nil, err }
	err = WriteFloatingInteger(&buf, s.MinHeader.Bits)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.MinHeader.Nonce)
	if err != nil { return nil, err }

	err = WritePossiblyNoneHash(&buf, s.ShareInfo.ShareData.PreviousShareHash, &zeroHash)
	if err != nil { return nil, err }
	err = WriteVarString(&buf, s.ShareInfo.ShareData.CoinBase)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.Nonce)
	if err != nil { return nil, err }
	err = WriteFixedBytes(&buf, s.ShareInfo.ShareData.PubKeyHash, 20)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.Subsidy)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.Donation)
	if err != nil { return nil, err }
	err = WriteStaleInfo(&buf, s.ShareInfo.ShareData.StaleInfo)
	if err != nil { return nil, err }
	err = WriteVarInt(&buf, s.ShareInfo.ShareData.DesiredVersion)
	if err != nil { return nil, err }

	err = WriteChainHashList(&buf, s.ShareInfo.NewTransactionHashes)
	if err != nil { return nil, err }
	err = WriteTransactionHashRefs(&buf, s.ShareInfo.TransactionHashRefs)
	if err != nil { return nil, err }
	err = WritePossiblyNoneHash(&buf, s.ShareInfo.FarShareHash, &zeroHash)
	if err != nil { return nil, err }
	err = WriteFloatingInteger(&buf, s.ShareInfo.MaxBits)
	if err != nil { return nil, err }
	err = WriteFloatingInteger(&buf, s.ShareInfo.Bits)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.Timestamp)
	if err != nil { return nil, err }
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.AbsHeight)
	if err != nil { return nil, err }
	err = WriteInt(&buf, s.ShareInfo.AbsWork, 128, binary.LittleEndian)
	if err != nil { return nil, err }

	if s.Type >= 16 {
		if s.ShareInfo.SegwitData != nil {
			err = WriteChainHashList(&buf, s.ShareInfo.SegwitData.TXIDMerkleLink.Branch)
			if err != nil { return nil, err }
			err = WriteChainHash(&buf, s.ShareInfo.SegwitData.WTXIDMerkleRoot)
			if err != nil { return nil, err }
		} else {
			err = WriteVarInt(&buf, 0)
			if err != nil { return nil, err }
			noneWTXIDMerkleRoot, _ := chainhash.NewHashFromStr("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			err = WriteChainHash(&buf, noneWTXIDMerkleRoot)
			if err != nil { return nil, err }
		}
	}

	err = WriteChainHashList(&buf, s.RefMerkleLink.Branch)
	if err != nil { return nil, err }

	err = binary.Write(&buf, binary.LittleEndian, s.LastTxOutNonce)
	if err != nil { return nil, err }

	err = WriteFixedBytes(&buf, s.HashLink.State, 32)
	if err != nil { return nil, err }
	err = WriteFixedBytes(&buf, s.HashLink.ExtraData, 0)
	if err != nil { return nil, err }
	err = WriteVarInt(&buf, s.HashLink.Length)
	if err != nil { return nil, err }

	if s.Type < 16 {
		err = WriteChainHashList(&buf, s.MerkleLink.Branch)
		if err != nil { return nil, err }
	}

	return buf.Bytes(), nil
}

func (s *Share) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error

	version, err := ReadVarInt(r)
	if err != nil { return err }
	s.MinHeader.Version = int32(version)
	var zeroHash chainhash.Hash
	s.MinHeader.PreviousBlock, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Timestamp)
	if err != nil { return err }
	s.MinHeader.Bits, err = ReadFloatingInteger(r)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Nonce)
	if err != nil { return err }

	s.ShareInfo.ShareData.PreviousShareHash, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return err }
	s.ShareInfo.ShareData.CoinBase, err = ReadVarString(r)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Nonce)
	if err != nil { return err }
	s.ShareInfo.ShareData.PubKeyHash, err = ReadFixedBytes(r, 20)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Subsidy)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Donation)
	if err != nil { return err }
	s.ShareInfo.ShareData.StaleInfo, err = ReadStaleInfo(r)
	if err != nil { return err }
	s.ShareInfo.ShareData.DesiredVersion, err = ReadVarInt(r)
	if err != nil { return err }

	s.ShareInfo.NewTransactionHashes, err = ReadChainHashList(r)
	if err != nil { return err }
	s.ShareInfo.TransactionHashRefs, err = ReadTransactionHashRefs(r)
	if err != nil { return err }
	s.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return err }
	s.ShareInfo.MaxBits, err = ReadFloatingInteger(r)
	if err != nil { return err }
	s.ShareInfo.Bits, err = ReadFloatingInteger(r)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.Timestamp)
	if err != nil { return err }
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.AbsHeight)
	if err != nil { return err }
	s.ShareInfo.AbsWork, err = ReadInt(r, 128, binary.LittleEndian)
	if err != nil { return err }

	if s.Type >= 16 {
		var currentSegwitData SegwitData
		currentSegwitData.TXIDMerkleLink.Branch, err = ReadChainHashList(r)
		if err != nil { return err }
		currentSegwitData.WTXIDMerkleRoot, err = ReadChainHash(r)
		if err != nil { return err }

		noneWTXIDMerkleRoot, _ := chainhash.NewHashFromStr("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
		if len(currentSegwitData.TXIDMerkleLink.Branch) == 0 &&
			currentSegwitData.WTXIDMerkleRoot.IsEqual(noneWTXIDMerkleRoot) {
			s.ShareInfo.SegwitData = nil
		} else {
			s.ShareInfo.SegwitData = &currentSegwitData
		}
	}

	s.RefMerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil { return err }

	err = binary.Read(r, binary.LittleEndian, &s.LastTxOutNonce)
	if err != nil { return err }

	s.HashLink.State, err = ReadFixedBytes(r, 32)
	if err != nil { return err }
	s.HashLink.ExtraData, err = ReadFixedBytes(r, 0)
	if err != nil { return err }
	s.HashLink.Length, err = ReadVarInt(r)
	if err != nil { return err }

	if s.Type < 16 {
		s.MerkleLink.Branch, err = ReadChainHashList(r)
		if err != nil { return err }
	}

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

func init() {
	var err error
	DonationScript, err = hex.DecodeString("410418a74130b2f4fad899d8ed2bff272bc43a03c8ca72897ae3da584d7a770b5a9ea8dd1b37a620d27c6cf6d5a7a9bbd6872f5981e95816d701d94f201c5d093be6ac")
	if err != nil {
		panic(err)
	}

	genTxBeforeRefHashBuf := new(bytes.Buffer)
	WriteVarString(genTxBeforeRefHashBuf, DonationScript)
	binary.Write(genTxBeforeRefHashBuf, binary.LittleEndian, uint64(0))
	genTxBeforeRefHashBuf.Write([]byte{0x6a, 0x28, 0x00})
	GenTxBeforeRefHash = genTxBeforeRefHashBuf.Bytes()

	if DonationScript == nil {
		DonationScript = []byte{}
	}
	if GenTxBeforeRefHash == nil {
		GenTxBeforeRefHash = []byte{}
	}
}

var DonationScript []byte
var GenTxBeforeRefHash []byte
