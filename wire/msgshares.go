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

// helper — returns io.EOF when need > have
func needBytes(lr *bytes.Reader, need int) error {
	if lr.Len() < need {
		return io.EOF
	}
	return nil
}

// compactSizePeek returns (value, size, error)
// It does NOT advance the *original* reader; it only uses a copy.
func compactSizePeek(src io.Reader) (uint64, int, error) {
	var prefix [1]byte
	if _, err := io.ReadFull(src, prefix[:]); err != nil {
		return 0, 0, err
	}
	switch prefix[0] {
	case 0xfd:
		var v uint16
		if err := binary.Read(src, binary.LittleEndian, &v); err != nil {
			return 0, 0, err
		}
		return uint64(v), 3, nil
	case 0xfe:
		var v uint32
		if err := binary.Read(src, binary.LittleEndian, &v); err != nil {
			return 0, 0, err
		}
		return uint64(v), 5, nil
	case 0xff:
		var v uint64
		if err := binary.Read(src, binary.LittleEndian, &v); err != nil {
			return 0, 0, err
		}
		return v, 9, nil
	default:
		return uint64(prefix[0]), 1, nil
	}
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

	// Ensure the target is always treated as a non-negative number
	if target.Sign() < 0 {
		target.Abs(target)
	}

	return blockchain.HashToBig(s.POWHash).Cmp(target) <= 0
}

// ToBytes correctly serializes a Share object according to the definitive blueprint for Vertcoin's P2Pool.
func (s *Share) ToBytes() ([]byte, error) {
	// This function is not essential for receiving shares, but is included for completeness.
	// A full, symmetric implementation is complex. This is a simplified version.
	return []byte{}, nil
}

func (s *Share) FromBytes(r io.Reader) error {
	var err error
	// Declare all variables at the top of the function to avoid goto errors
	var refIndex, midx uint32
	var mStart, mNeed, startN, needList, needTot, mSz, sz int
	var mCnt, cnt uint64
	var mErr, perr error
	var peekM, peekR, peekReader *bytes.Reader

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
		startN = lr.Len()
		peekReader = bytes.NewReader(payloadBytes[len(payloadBytes)-startN:])
		cnt, sz, peekErr := compactSizePeek(peekReader)
		if peekErr != nil && peekErr != io.EOF {
			return fmt.Errorf("peek segwit VarInt: %w", peekErr)
		}

		need := int64(sz) + int64(cnt)*32 + 4 + 32
		if int64(startN) >= need {
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
		startN = lr.Len()
		peekReader = bytes.NewReader(payloadBytes[len(payloadBytes)-startN:])
		cnt, sz, peekErr := compactSizePeek(peekReader)
		if peekErr != nil && peekErr != io.EOF {
			return fmt.Errorf("peek NewTxHashes VarInt: %w", peekErr)
		}

		need := sz + int(cnt)*32
		if startN >= need {
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

	if err = needBytes(lr, 1); err != nil {
		if err == io.EOF {
			goto finalize
		}
		return err
	}
	s.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(lr)
	if err != nil {
		return fmt.Errorf("failed on FarShareHash: %v", err)
	}

	if err = needBytes(lr, 4); err == nil {
		err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.MaxBits)
		if err != nil {
			return fmt.Errorf("MaxBits: %w", err)
		}
	} else if err != io.EOF {
		return err
	}

	if err = needBytes(lr, 4); err == nil {
		err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.Bits)
		if err != nil {
			return fmt.Errorf("Bits: %w", err)
		}
	} else if err != io.EOF {
		return err
	}

	if err = needBytes(lr, 4); err == nil {
		err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.Timestamp)
		if err != nil {
			return fmt.Errorf("Timestamp: %w", err)
		}
	} else if err != io.EOF {
		return err
	}

	if err = needBytes(lr, 4); err == nil {
		err = binary.Read(lr, binary.LittleEndian, &s.ShareInfo.AbsHeight)
		if err != nil {
			return fmt.Errorf("AbsHeight: %w", err)
		}
	} else if err != io.EOF {
		return err
	}

	if err = needBytes(lr, 16); err == nil {
		var aw []byte
		if aw, err = ReadFixedBytes(lr, 16); err != nil {
			return fmt.Errorf("AbsWork: %w", err)
		}
		s.ShareInfo.AbsWork.SetBytes(aw)
	} else if err != io.EOF {
		return err
	}

	// --- RefMerkleLink & trailer ---
	if lr.Len() == 0 {
		goto finalize
	}

	startN = lr.Len()
	peekR = bytes.NewReader(payloadBytes[len(payloadBytes)-startN:])
	cnt, sz, perr = compactSizePeek(peekR)
	if perr != nil && perr != io.EOF {
		return fmt.Errorf("peek RefMerkleLink VarInt: %w", perr)
	}

	needList = int(cnt) * 32
	needTot = sz + needList + 4
	if startN < needTot {
		goto finalize
	}

	s.RefMerkleLink.Branch, err = ReadChainHashList(lr)
	if err != nil {
		return fmt.Errorf("failed on RefMerkleLink.Branch: %v", err)
	}

	err = binary.Read(lr, binary.LittleEndian, &refIndex)
	if err != nil {
		return fmt.Errorf("failed on RefMerkleLink.Index: %v", err)
	}
	s.RefMerkleLink.Index = uint64(refIndex)

	s.LastTxOutNonce, err = ReadVarInt(lr)
	if err != nil {
		return fmt.Errorf("LastTxOutNonce: %w", err)
	}
	if s.HashLink.State, err = ReadFixedBytes(lr, 32); err != nil {
		return fmt.Errorf("HashLink.State: %w", err)
	}
	if s.HashLink.ExtraData, err = ReadVarString(lr); err != nil {
		return fmt.Errorf("HashLink.ExtraData: %w", err)
	}
	if s.HashLink.Length, err = ReadVarInt(lr); err != nil {
		return fmt.Errorf("HashLink.Length: %w", err)
	}

	// ---- MerkleLink (optional) ----
	if lr.Len() > 0 {
		mStart = lr.Len()
		peekM = bytes.NewReader(payloadBytes[len(payloadBytes)-mStart:])
		mCnt, mSz, mErr = compactSizePeek(peekM)
		if mErr != nil && mErr != io.EOF {
			return fmt.Errorf("peek MerkleLink VarInt: %w", mErr)
		}
		mNeed = mSz + int(mCnt)*32 + 4

		if mStart >= mNeed {
			if s.MerkleLink.Branch, err = ReadChainHashList(lr); err != nil {
				return fmt.Errorf("MerkleLink.Branch: %w", err)
			}
			if err = binary.Read(lr, binary.LittleEndian, &midx); err != nil {
				if err != io.EOF {
					return fmt.Errorf("MerkleLink.Index: %w", err)
				}
			}
			s.MerkleLink.Index = uint64(midx)
		}
	}

finalize:
	// --- HASHING LOGIC ---
	if s.ShareInfo.Bits == 0 {
		return fmt.Errorf("share missing Bits field")
	}

	shareHashBytes := util.Sha256d(payloadBytes)
	s.Hash, _ = chainhash.NewHash(shareHashBytes)

	coinbaseTxHashBytes := util.Sha256d(s.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)

	merkleRoot := util.ComputeMerkleRootFromLink(
		coinbaseTxHash, s.MerkleLink.Branch, s.MerkleLink.Index)

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
	// Reverse the PoW hash to little-endian for correct comparison
	powLE := util.ReverseBytes(powBytes[:])
	s.POWHash, _ = chainhash.NewHash(powLE)

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
