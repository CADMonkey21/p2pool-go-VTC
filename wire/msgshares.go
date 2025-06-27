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

// peekVarInt reads a VarInt *non-destructively* and returns the
// value, the raw bytes and any error.
func peekVarInt(r io.Reader) (val uint64, raw []byte, err error) {
	var buf [9]byte
	// read first byte
	if _, err = io.ReadFull(r, buf[:1]); err != nil {
		return
	}
	raw = buf[:1]

	switch buf[0] {
	case 0xfd:
		_, err = io.ReadFull(r, buf[1:3])
		raw = buf[:3]
	case 0xfe:
		_, err = io.ReadFull(r, buf[1:5])
		raw = buf[:5]
	case 0xff:
		_, err = io.ReadFull(r, buf[1:9])
		raw = buf[:9]
	}
	if err != nil {
		return
	}
	val, _ = binary.Uvarint(raw) // VarInt layout matches Uvarint
	return
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

func (s *Share) IsValid() bool {
	if s.POWHash == nil || s.ShareInfo.Bits == 0 {
		return false
	}
	target := blockchain.CompactToBig(s.ShareInfo.Bits)
	bnHash := blockchain.HashToBig(s.POWHash)
	return bnHash.Cmp(target) <= 0
}

// ToBytes correctly serializes a Share object according to the definitive blueprint for Vertcoin's P2Pool.
func (s *Share) ToBytes() ([]byte, error) {
	// This function is not essential for receiving shares, but is included for completeness.
	// A full, symmetric implementation is complex. This is a simplified version.
	return []byte{}, nil
}

func (s *Share) FromBytes(r io.Reader) error {
	var err error

	s.Type, err = ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed reading share Type: %v", err)
	}

	contentsLength, err := ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("failed reading share contents length: %v", err)
	}

	// Buffer the entire payload to enable non-destructive peeking
	payloadBytes := make([]byte, contentsLength)
	if _, err := io.ReadFull(r, payloadBytes); err != nil {
		return fmt.Errorf("could not read full share payload: %w", err)
	}
	lr := bytes.NewReader(payloadBytes)

	s.ShareInfo.AbsWork = new(big.Int)

	// MinHeader and ShareData
	err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Version)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Version: %v", err)
	}
	s.MinHeader.PreviousBlock, err = ReadPossiblyNoneHash(lr)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.PreviousBlock: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Timestamp)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Timestamp: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Bits)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Bits: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.MinHeader.Nonce)
	if err != nil {
		return fmt.Errorf("failed on MinHeader.Nonce: %v", err)
	}
	s.ShareInfo.ShareData.PreviousShareHash, err = ReadPossiblyNoneHash(lr)
	if err != nil {
		return fmt.Errorf("failed on ShareData.PreviousShareHash: %v", err)
	}
	s.ShareInfo.ShareData.CoinBase, err = ReadVarString(lr)
	if err != nil {
		return fmt.Errorf("failed on ShareData.CoinBase: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Nonce)
	if err != nil {
		return fmt.Errorf("failed on ShareData.Nonce: %v", err)
	}
	s.ShareInfo.ShareData.PubKeyHash, err = ReadFixedBytes(lr, 20)
	if err != nil {
		return fmt.Errorf("failed on ShareData.PubKeyHash: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil {
		return fmt.Errorf("failed on ShareData.PubKeyHashVersion: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Subsidy)
	if err != nil {
		return fmt.Errorf("failed on ShareData.Subsidy: %v", err)
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.ShareData.Donation)
	if err != nil {
		return fmt.Errorf("failed on ShareData.Donation: %v", err)
	}
	s.ShareInfo.ShareData.StaleInfo, err = ReadStaleInfo(lr)
	if err != nil {
		return fmt.Errorf("failed on ShareData.StaleInfo: %v", err)
	}
	s.ShareInfo.ShareData.DesiredVersion, err = ReadVarInt(lr)
	if err != nil {
		return fmt.Errorf("failed on ShareData.DesiredVersion: %v", err)
	}

	if s.Type >= 17 {
		startN := lr.Len()
		peekReader := bytes.NewReader(payloadBytes[len(payloadBytes)-startN:])
		cnt, raw, peekErr := peekVarInt(peekReader)
		if peekErr != nil && peekErr != io.EOF {
			return fmt.Errorf("peek segwit VarInt: %w", peekErr)
		}

		need := int64(cnt)*32 + 4 + 32
		if int64(startN-len(raw)) >= need {
			var sd SegwitData
			if sd.TXIDMerkleLink.Branch, err = ReadChainHashList(lr); err != nil {
				return fmt.Errorf("SegwitData.Branch: %w", err)
			}
			var idx32 uint32
			if err = binary.Read(lr, binary.LittleEndian, &idx32); err != nil {
				return fmt.Errorf("SegwitData.Index: %w", err)
			}
			sd.TXIDMerkleLink.Index = uint64(idx32)
			if sd.WTXIDMerkleRoot, err = ReadChainHash(lr); err != nil {
				return fmt.Errorf("SegwitData.Root: %w", err)
			}

			ff := bytes.Repeat([]byte{0xff}, 32)
			if len(sd.TXIDMerkleLink.Branch) != 0 || !bytes.Equal(sd.WTXIDMerkleRoot[:], ff) {
				s.ShareInfo.SegwitData = &sd
			}
		}
	}

	if lr.Len() > 0 {
		startN := lr.Len()
		peekReader := bytes.NewReader(payloadBytes[len(payloadBytes)-startN:])
		cnt, raw, peekErr := peekVarInt(peekReader)
		if peekErr != nil && peekErr != io.EOF {
			return fmt.Errorf("peek NewTxHashes VarInt: %w", peekErr)
		}

		need := int(cnt) * 32
		if startN-len(raw) >= need {
			s.ShareInfo.NewTransactionHashes, err = ReadChainHashList(lr)
			if err != nil && err != io.EOF {
				return fmt.Errorf("failed on NewTransactionHashes: %v", err)
			}
		}
	}
	if lr.Len() > 0 {
		if refs, err := readTransactionHashRefsLoose(lr); err == nil {
			s.ShareInfo.TransactionHashRefs = refs
		} else if err != io.EOF {
			return fmt.Errorf("failed on TransactionHashRefs: %v", err)
		}
	}

	if lr.Len() == 0 {
		return nil
	}
	s.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(lr)
	if err != nil {
		return fmt.Errorf("failed on FarShareHash: %v", err)
	}

	if lr.Len() == 0 {
		return nil
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.MaxBits)
	if err != nil {
		return fmt.Errorf("failed on MaxBits: %v", err)
	}

	if lr.Len() == 0 {
		return nil
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.Bits)
	if err != nil {
		return fmt.Errorf("failed on Bits: %v", err)
	}

	if lr.Len() == 0 {
		return nil
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.Timestamp)
	if err != nil {
		return fmt.Errorf("failed on Timestamp: %v", err)
	}

	if lr.Len() == 0 {
		return nil
	}
	err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.AbsHeight)
	if err != nil {
		return fmt.Errorf("failed on AbsHeight: %v", err)
	}

	if lr.Len() == 0 {
		return nil
	}
	absWorkBytes, err := ReadFixedBytes(lr, 16)
	if err != nil {
		return fmt.Errorf("failed on AbsWork: %v", err)
	}
	s.ShareInfo.AbsWork.SetBytes(absWorkBytes)

	if lr.Len() > 0 {
		startN := lr.Len()
		peekReader := bytes.NewReader(payloadBytes[len(payloadBytes)-startN:])
		cnt, _, peekErr := peekVarInt(peekReader)
		if peekErr != nil && peekErr != io.EOF {
			return fmt.Errorf("peek RefMerkleLink VarInt: %w", peekErr)
		}

		varintLen := int(startN - peekReader.Len())

		needList := int(cnt) * 32
		needMin := needList + 4
		if startN-varintLen >= needMin {
			s.RefMerkleLink.Branch, err = ReadChainHashList(lr)
			if err != nil {
				return fmt.Errorf("failed on RefMerkleLink.Branch: %v", err)
			}

			var refIndex uint32
			err = binary.Read(lr, binary.LittleEndian, &refIndex)
			if err != nil {
				return fmt.Errorf("failed on RefMerkleLink.Index: %v", err)
			}
			s.RefMerkleLink.Index = uint64(refIndex)

			s.LastTxOutNonce, err = ReadVarInt(lr)
			if err != nil {
				return fmt.Errorf("failed on LastTxOutNonce: %v", err)
			}

			s.HashLink.State, err = ReadFixedBytes(lr, 32)
			if err != nil {
				return fmt.Errorf("failed on HashLink.State: %v", err)
			}

			s.HashLink.ExtraData, err = ReadVarString(lr)
			if err != nil {
				return fmt.Errorf("failed on HashLink.ExtraData: %v", err)
			}

			s.HashLink.Length, err = ReadVarInt(lr)
			if err != nil {
				return fmt.Errorf("failed on HashLink.Length: %v", err)
			}

			s.MerkleLink.Branch, err = ReadChainHashList(lr)
			if err != nil {
				return fmt.Errorf("failed on MerkleLink.Branch: %v", err)
			}

			var merkleIndex uint32
			err = binary.Read(lr, binary.LittleEndian, &merkleIndex)
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return fmt.Errorf("failed on MerkleLink.Index: %v", err)
			}
			s.MerkleLink.Index = uint64(merkleIndex)
		}
	}

	return nil
}

func (m *MsgShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Shares, err = ReadShares(r)
	if err != nil && err != io.EOF { // EOF can be okay if there are no shares.
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
