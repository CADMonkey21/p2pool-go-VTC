package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-vtc/net"
	"github.com/CADMonkey21/p2pool-go-vtc/util"
)

// Helper to peek at a varint from a reader without consuming it
func peekVarInt(r *bytes.Reader) (val uint64, size int, err error) {
	clone := *r
	val, err = ReadVarInt(&clone)
	if err != nil {
		return 0, 0, err
	}
	size = r.Len() - clone.Len()
	return val, size, nil
}

// readVarIntLoose decodes the CompactSize format but *does not*
// enforce that the shortest possible prefix was used.
func readVarIntLoose(r io.Reader) (uint64, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	switch b[0] {
	case 0xff:
		var v uint64
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return 0, err
		}
		return v, nil
	case 0xfe:
		var v uint32
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return 0, err
		}
		return uint64(v), nil
	case 0xfd:
		var v uint16
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return 0, err
		}
		return uint64(v), nil
	default:
		return uint64(b[0]), nil
	}
}

// readTransactionHashRefsLoose uses the loose VarInt reader to accept
// non-canonically packed values from older peers.
func readTransactionHashRefsLoose(r io.Reader) ([]TransactionHashRef, error) {
	cnt, err := readVarIntLoose(r)
	if err != nil {
		return nil, err
	}
	if cnt > 10000 { // Sanity check
		return nil, fmt.Errorf("transaction hash ref count too high: %d", cnt)
	}
	out := make([]TransactionHashRef, cnt)
	for i := uint64(0); i < cnt; i++ {
		sc, err := readVarIntLoose(r) // ShareCount
		if err != nil {
			return nil, err
		}
		tc, err := readVarIntLoose(r) // TxCount
		if err != nil {
			return nil, err
		}
		out[i] = TransactionHashRef{ShareCount: sc, TxCount: tc}
	}
	return out, nil
}

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
	Target         []byte
}

// FullBlockHeader reconstructs the full wire.BlockHeader from the share data.
func (s *Share) FullBlockHeader() (*wire.BlockHeader, error) {
	coinbaseTxHashBytes := util.Sha256d(s.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)
	merkleRoot := util.ComputeMerkleRootFromLink(coinbaseTxHash, s.MerkleLink.Branch, s.MerkleLink.Index)

	if s.MinHeader.PreviousBlock == nil {
		return nil, fmt.Errorf("cannot construct header, previous block hash is nil")
	}

	var effectiveBits uint32
	switch {
	case s.ShareInfo.Bits != 0:
		effectiveBits = s.ShareInfo.Bits
	case s.ShareInfo.ShareData.Bits != 0:
		effectiveBits = s.ShareInfo.ShareData.Bits
	case len(s.Target) == 32:
		effectiveBits = blockchain.BigToCompact(new(big.Int).SetBytes(s.Target))
	case s.ShareInfo.MaxBits != 0:
		effectiveBits = s.ShareInfo.MaxBits
	default:
		return nil, fmt.Errorf("share %s has no difficulty fields", s.Hash.String()[:12])
	}

	header := &wire.BlockHeader{
		Version:    s.MinHeader.Version,
		PrevBlock:  *s.MinHeader.PreviousBlock,
		MerkleRoot: *merkleRoot,
		Timestamp:  time.Unix(int64(s.MinHeader.Timestamp), 0),
		Bits:       effectiveBits,
		Nonce:      s.MinHeader.Nonce,
	}

	return header, nil
}

// RecalculatePOW computes and sets the share's POWHash.
func (s *Share) RecalculatePOW() error {
	header, err := s.FullBlockHeader()
	if err != nil {
		return fmt.Errorf("could not construct header to recalculate PoW: %v", err)
	}

	var hdrBuf bytes.Buffer
	if err := header.Serialize(&hdrBuf); err != nil {
		return fmt.Errorf("could not serialize header to recalculate PoW: %v", err)
	}

	powBytesLE, err := p2pnet.ActiveNetwork.Verthash.SumVerthash(hdrBuf.Bytes())
	if err != nil {
		return fmt.Errorf("verthash failed during PoW recalculation: %v", err)
	}

	powBytesBE := util.ReverseBytes(powBytesLE[:])
	s.POWHash, _ = chainhash.NewHash(powBytesBE)
	return nil
}

// IsValid checks if the share's PoW hash is less than or equal to its target.
func (s *Share) IsValid() bool {
	if s.POWHash == nil {
		if err := s.RecalculatePOW(); err != nil {
			logging.Warnf("Could not recalculate PoW for share %s: %v", s.Hash.String()[:12], err)
			return false
		}
	}

	var effectiveBits uint32
	switch {
	case s.ShareInfo.Bits != 0:
		effectiveBits = s.ShareInfo.Bits
	case s.ShareInfo.ShareData.Bits != 0:
		effectiveBits = s.ShareInfo.ShareData.Bits
	case len(s.Target) == 32:
		effectiveBits = blockchain.BigToCompact(new(big.Int).SetBytes(s.Target))
	case s.ShareInfo.MaxBits != 0:
		effectiveBits = s.ShareInfo.MaxBits
	default:
		logging.Warnf("Share %s has no difficulty fields.", s.Hash.String()[:12])
		return false
	}
	s.ShareInfo.Bits = effectiveBits

	target := blockchain.CompactToBig(effectiveBits)
	if target.Sign() <= 0 {
		return false
	}

	// ***** THIS IS THE FIX *****
	// Reverse the bytes of the PoW hash before converting to a big.Int
	// This corrects the endianness mismatch for the comparison.
	hashInt := new(big.Int).SetBytes(util.ReverseBytes(s.POWHash.CloneBytes()))
	// **************************

	return hashInt.Cmp(target) <= 0
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
	Bits              uint32
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

func (s *Share) ToBytes() ([]byte, error) {
	var contents bytes.Buffer
	binary.Write(&contents, binary.LittleEndian, s.MinHeader.Version)
	WritePossiblyNoneHash(&contents, s.MinHeader.PreviousBlock)
	binary.Write(&contents, binary.LittleEndian, s.MinHeader.Timestamp)
	binary.Write(&contents, binary.LittleEndian, s.MinHeader.Bits)
	binary.Write(&contents, binary.LittleEndian, s.MinHeader.Nonce)
	WritePossiblyNoneHash(&contents, s.ShareInfo.ShareData.PreviousShareHash)
	WriteVarString(&contents, s.ShareInfo.ShareData.CoinBase)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.ShareData.Nonce)
	WriteFixedBytes(&contents, s.ShareInfo.ShareData.PubKeyHash, 20)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.ShareData.PubKeyHashVersion)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.ShareData.Subsidy)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.ShareData.Donation)
	WriteStaleInfo(&contents, s.ShareInfo.ShareData.StaleInfo)
	WriteVarInt(&contents, s.ShareInfo.ShareData.DesiredVersion)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.ShareData.Bits)
	if s.ShareInfo.ShareData.Bits == 0 && len(s.Target) == 32 {
		contents.Write(s.Target)
	}
	if s.Type >= 17 {
		if s.ShareInfo.SegwitData != nil {
			WriteChainHashList(&contents, s.ShareInfo.SegwitData.TXIDMerkleLink.Branch)
			binary.Write(&contents, binary.LittleEndian, uint32(s.ShareInfo.SegwitData.TXIDMerkleLink.Index))
			WriteChainHash(&contents, s.ShareInfo.SegwitData.WTXIDMerkleRoot)
		} else {
			WriteChainHashList(&contents, []*chainhash.Hash{})
			binary.Write(&contents, binary.LittleEndian, uint32(0))
			WriteChainHash(&contents, &chainhash.Hash{})
		}
	}
	WriteChainHashList(&contents, s.ShareInfo.NewTransactionHashes)
	WriteTransactionHashRefs(&contents, s.ShareInfo.TransactionHashRefs)
	WritePossiblyNoneHash(&contents, s.ShareInfo.FarShareHash)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.MaxBits)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.Bits)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.Timestamp)
	binary.Write(&contents, binary.LittleEndian, s.ShareInfo.AbsHeight)
	workBytes := s.ShareInfo.AbsWork.Bytes()
	paddedWork := make([]byte, 16)
	copy(paddedWork[16-len(workBytes):], workBytes)
	WriteFixedBytes(&contents, paddedWork, 16)
	WriteChainHashList(&contents, s.RefMerkleLink.Branch)
	binary.Write(&contents, binary.LittleEndian, uint32(s.RefMerkleLink.Index))
	WriteVarInt(&contents, s.LastTxOutNonce)
	WriteFixedBytes(&contents, s.HashLink.State, 32)
	WriteVarString(&contents, s.HashLink.ExtraData)
	WriteVarInt(&contents, s.HashLink.Length)
	WriteChainHashList(&contents, s.MerkleLink.Branch)
	binary.Write(&contents, binary.LittleEndian, uint32(s.MerkleLink.Index))

	var finalBuf bytes.Buffer
	WriteVarInt(&finalBuf, s.Type)
	WriteVarInt(&finalBuf, uint64(contents.Len()))
	finalBuf.Write(contents.Bytes())
	return finalBuf.Bytes(), nil
}

func (s *Share) FromBytes(r io.Reader) error {
	var err error
	var refIndex, mIndex uint32
	var aw []byte

	s.Type, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed reading share Type: %v", err)
	}

	contentsLength, err := ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed reading share contents length: %v", err)
	}

	payloadBytes := make([]byte, contentsLength)
	if _, err := io.ReadFull(r, payloadBytes); err != nil {
		return fmt.Errorf("could not read full share payload: %w", err)
	}

	lr := bytes.NewReader(payloadBytes)
	initialLen := lr.Len()
	s.ShareInfo.AbsWork = new(big.Int)

	if err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Version); err != nil { return err }
	if s.MinHeader.PreviousBlock, err = ReadPossiblyNoneHash(lr); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Timestamp); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Bits); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Nonce); err != nil { return err }
	if s.ShareInfo.ShareData.PreviousShareHash, err = ReadPossiblyNoneHash(lr); err != nil { return err }
	if s.ShareInfo.ShareData.CoinBase, err = ReadVarString(lr); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Nonce); err != nil { return err }
	if s.ShareInfo.ShareData.PubKeyHash, err = ReadFixedBytes(lr, 20); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.PubKeyHashVersion); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Subsidy); err != nil { return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Donation); err != nil { return err }
	if s.ShareInfo.ShareData.StaleInfo, err = ReadStaleInfo(lr); err != nil { return err }
	if s.ShareInfo.ShareData.DesiredVersion, err = ReadVarInt(lr); err != nil { return err }

	isEOF := func(e error) bool { return e == io.EOF || e == io.ErrUnexpectedEOF }

	if lr.Len() >= 4 {
		if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Bits); err != nil { if isEOF(err) { goto finalize }; return err }
	}
	if s.ShareInfo.ShareData.Bits == 0 && lr.Len() >= 32 {
		if s.Target, err = ReadFixedBytes(lr, 32); err != nil { if isEOF(err) { goto finalize }; return err }
	}

	if s.Type >= 17 {
		count, size, peekErr := peekVarInt(lr)
		if peekErr == nil {
			bytesNeeded := size + (int(count) * 32) + 4 + 32
			if lr.Len() >= bytesNeeded {
				var sd SegwitData
				if sd.TXIDMerkleLink.Branch, err = ReadChainHashList(lr); err != nil { return fmt.Errorf("SegwitData.Branch: %w", err) }
				var idx32 uint32
				if err = binary.Read(lr, binary.LittleEndian, &idx32); err != nil { return fmt.Errorf("SegwitData.Index: %w", err) }
				sd.TXIDMerkleLink.Index = uint64(idx32)
				if sd.WTXIDMerkleRoot, err = ReadChainHash(lr); err != nil { return fmt.Errorf("SegwitData.Root: %w", err) }
				ff := bytes.Repeat([]byte{0xff}, 32)
				if len(sd.TXIDMerkleLink.Branch) != 0 || !bytes.Equal(sd.WTXIDMerkleRoot[:], ff) {
					s.ShareInfo.SegwitData = &sd
				}
			}
		}
	}

	if s.ShareInfo.NewTransactionHashes, err = ReadChainHashList(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if s.ShareInfo.TransactionHashRefs, err = readTransactionHashRefsLoose(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if s.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.MaxBits); err != nil { if isEOF(err) { goto finalize }; return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.Bits); err != nil { if isEOF(err) { goto finalize }; return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.Timestamp); err != nil { if isEOF(err) { goto finalize }; return err }
	if err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.AbsHeight); err != nil { if isEOF(err) { goto finalize }; return err }

	aw, err = ReadFixedBytes(lr, 16)
	if err != nil { if isEOF(err) { goto finalize }; return err }
	s.ShareInfo.AbsWork.SetBytes(aw)

	if s.RefMerkleLink.Branch, err = ReadChainHashList(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if err = binary.Read(lr, binary.LittleEndian, &refIndex); err != nil { if isEOF(err) { goto finalize }; return err }
	s.RefMerkleLink.Index = uint64(refIndex)

	if s.LastTxOutNonce, err = ReadVarInt(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if s.HashLink.State, err = ReadFixedBytes(lr, 32); err != nil { if isEOF(err) { goto finalize }; return err }
	if s.HashLink.ExtraData, err = ReadVarString(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if s.HashLink.Length, err = ReadVarInt(lr); err != nil { if isEOF(err) { goto finalize }; return err }

	if s.MerkleLink.Branch, err = ReadChainHashList(lr); err != nil { if isEOF(err) { goto finalize }; return err }
	if err = binary.Read(lr, binary.LittleEndian, &mIndex); err != nil { if isEOF(err) { goto finalize }; return err }
	s.MerkleLink.Index = uint64(mIndex)

finalize:
	bytesRead := initialLen - lr.Len()
	finalPayload := payloadBytes[:bytesRead]
	shareHashBytes := util.Sha256d(finalPayload)
	s.Hash, _ = chainhash.NewHash(shareHashBytes)
	return nil
}

func (m *MsgShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Shares, err = ReadShares(r)
	if err != nil {
		logging.Debugf("Error deserializing shares message: %v", err)
		return err
	}
	return nil
}

func (m *MsgShares) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteShares(&buf, m.Shares)
	return buf.Bytes(), err
}

func (m *MsgShares) Command() string {
	return "shares"
}
