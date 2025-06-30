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
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/util"
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

// CORRECTED: This now properly handles little-endian hash comparison.
func (s *Share) IsValid() bool {
	if s.POWHash == nil || s.ShareInfo.Bits == 0 {
		return false
	}

	// 1. Target from compact bits -> *big.Int
	target := blockchain.CompactToBig(s.ShareInfo.Bits)
	if target.Sign() <= 0 {
		return false
	}

	// 2. Hash -> little-endian bytes -> *big.Int
	// The POWHash from the Verthash library is already in the correct byte order
	// to be interpreted as a little-endian integer by big.Int.
	// We just need to reverse it for the comparison.
	hashBytes := s.POWHash.CloneBytes()
	// Reverse the bytes for little-endian comparison
	for i, j := 0, len(hashBytes)-1; i < j; i, j = i+1, j-1 {
		hashBytes[i], hashBytes[j] = hashBytes[j], hashBytes[i]
	}
	hashInt := new(big.Int).SetBytes(hashBytes)

	// 3. Valid if hash <= target
	return hashInt.Cmp(target) <= 0
}

func (s *Share) ToBytes() ([]byte, error) {
	return []byte{}, nil
}

func (s *Share) FromBytes(r io.Reader) error {
	var err error
	var refIndex, mIndex uint32
	var aw []byte

	s.Type, err = ReadVarInt(r)
	if err != nil { return fmt.Errorf("failed reading share Type: %v", err) }

	contentsLength, err := ReadVarInt(r)
	if err != nil { return fmt.Errorf("failed reading share contents length: %v", err) }

	payloadBytes := make([]byte, contentsLength)
	if _, err := io.ReadFull(r, payloadBytes); err != nil { return fmt.Errorf("could not read full share payload: %w", err) }
	
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
	
	isEOF := func(e error) bool { return e == io.EOF || e == io.ErrUnexpectedEOF }

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

	coinbaseTxHashBytes := util.Sha256d(s.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)

	merkleRoot := util.ComputeMerkleRootFromLink(coinbaseTxHash, s.MerkleLink.Branch, s.MerkleLink.Index)

	var hdr bytes.Buffer
	binary.Write(&hdr, binary.LittleEndian, s.MinHeader.Version)
	if s.MinHeader.PreviousBlock != nil {
		hdr.Write(util.ReverseBytes(s.MinHeader.PreviousBlock.CloneBytes()))
	} else {
		hdr.Write(make([]byte, 32))
	}
	hdr.Write(util.ReverseBytes(merkleRoot.CloneBytes()))
	binary.Write(&hdr, binary.LittleEndian, s.MinHeader.Timestamp)
	binary.Write(&hdr, binary.LittleEndian, s.MinHeader.Bits)
	binary.Write(&hdr, binary.LittleEndian, s.MinHeader.Nonce)

	powBytes, err := p2pnet.ActiveNetwork.Verthash.SumVerthash(hdr.Bytes())
	if err != nil {
		return fmt.Errorf("verthash: %w", err)
	}

	powLE := util.ReverseBytes(powBytes[:])
	s.POWHash, _ = chainhash.NewHash(powLE)

	return nil
}

func (m *MsgShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Shares, err = ReadShares(r)
	if err != nil {
		logging.Errorf("Failed to deserialize shares message: %v", err)
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
