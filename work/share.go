package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/util"
)

/* -------------------------------------------------------------------- */
/* Share Structs (Ported from old wire protocol)                        */
/* -------------------------------------------------------------------- */

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

/* -------------------------------------------------------------------- */
/* Hashing & Validation Methods                                         */
/* -------------------------------------------------------------------- */

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

func (s *Share) CalculateHash() error {
	header, err := s.FullBlockHeader()
	if err != nil {
		return fmt.Errorf("could not construct header to calculate hash: %v", err)
	}

	var hdrBuf bytes.Buffer
	err = legacyHeaderSerialize(&hdrBuf, header)
	if err != nil {
		return fmt.Errorf("could not serialize header for hash: %v", err)
	}

	blockHashBytes := util.Sha256d(hdrBuf.Bytes())
	s.Hash, _ = chainhash.NewHash(blockHashBytes)
	return nil
}

func (s *Share) CalculatePOWHash() error {
	header, err := s.FullBlockHeader()
	if err != nil {
		return fmt.Errorf("could not construct header to calculate PoW: %v", err)
	}

	var hdrBuf bytes.Buffer
	err = legacyHeaderSerialize(&hdrBuf, header)
	if err != nil {
		return fmt.Errorf("could not serialize header for PoW: %v", err)
	}
	logging.Debugf("[DIAG] Serialized Header for Hashing: %x", hdrBuf.Bytes())

	powBytesLE, err := p2pnet.ActiveNetwork.Verthash.Hash(hdrBuf.Bytes())
	if err != nil {
		return fmt.Errorf("verthash failed during PoW recalculation: %v", err)
	}

	s.POWHash, _ = chainhash.NewHash(util.ReverseBytes(powBytesLE))
	return nil
}

func (s *Share) CalculateHashes() error {
	if err := s.CalculateHash(); err != nil {
		return err
	}
	if err := s.CalculatePOWHash(); err != nil {
		return err
	}
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

/* -------------------------------------------------------------------- */
/* Share Creation Engine                                                */
/* -------------------------------------------------------------------- */

func CreateShare(
	job *BlockTemplate,
	extraNonce1, extraNonce2, nTimeHex, nonceHex, payoutAddress string,
	shareChain *ShareChain,
	stratumDifficulty float64,
	witnessCommitment []byte,
	wtxidMerkleRoot *chainhash.Hash,
	txidMerkleLinkBranches []*chainhash.Hash,
	coinbaseMerkleLinkBranches []*chainhash.Hash,
) (*Share, error) {
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, err
	}

	nTimeBytes, err := hex.DecodeString(nTimeHex)
	if err != nil {
		return nil, err
	}
	nTimeUint32 := binary.BigEndian.Uint32(nTimeBytes)

	var pkh []byte
	var pkhVersion byte

	if strings.HasPrefix(strings.ToLower(payoutAddress), "vtc1") || strings.HasPrefix(strings.ToLower(payoutAddress), "tvtc1") {
		hrp, decodedBech32, err := bech32.Decode(payoutAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to decode bech32 payout address '%s': %v", payoutAddress, err)
		}
		if hrp != "vtc" && hrp != "tvtc" {
			return nil, fmt.Errorf("address is not a valid vertcoin bech32 address (hrp: %s)", hrp)
		}
		pkhVersion = decodedBech32[0]
		pkh, err = bech32.ConvertBits(decodedBech32[1:], 5, 8, false)
		if err != nil {
			return nil, fmt.Errorf("failed to convert bits for bech32 address '%s': %v", payoutAddress, err)
		}
	} else {
		decoded58 := base58.Decode(payoutAddress)
		if len(decoded58) < 5 {
			return nil, fmt.Errorf("invalid base58 address length for '%s'", payoutAddress)
		}
		pkhVersion = decoded58[0]
		pkh = decoded58[1 : len(decoded58)-4]
	}

	nBitsBytes, err := hex.DecodeString(job.Bits)
	if err != nil {
		return nil, fmt.Errorf("could not decode bits from template: %v", err)
	}
	shareBits := binary.BigEndian.Uint32(nBitsBytes)

	prevBlockHash, _ := chainhash.NewHashFromStr(job.PreviousBlockHash)
	nonceUint32 := binary.BigEndian.Uint32(nonceBytes)

	coinbaseTxBytes, err := CreateCoinbaseTx(job, payoutAddress, extraNonce1, extraNonce2, witnessCommitment)
	if err != nil {
		return nil, err
	}

	coinbaseTxHash := DblSha256(coinbaseTxBytes)
	merkleLinkBranches := coinbaseMerkleLinkBranches
	if len(merkleLinkBranches) > 0 {
		if len(merkleLinkBranches) > 0 && merkleLinkBranches[0] != nil {
			merkleLinkBranches[0], _ = chainhash.NewHash(ReverseBytes(coinbaseTxHash))
		}
	} else {
		merkleLinkBranches = []*chainhash.Hash{}
	}

	newTxHashesForShareInfo := []*chainhash.Hash{}
	for _, tx := range job.Transactions {
		h, _ := chainhash.NewHashFromStr(tx.TxID)
		newTxHashesForShareInfo = append(newTxHashesForShareInfo, h)
	}

	share := &Share{
		Type: 23,
		MinHeader: SmallBlockHeader{
			Version:       int32(job.Version),
			PreviousBlock: prevBlockHash,
			Timestamp:     nTimeUint32,
			Bits:          shareBits,
			Nonce:         nonceUint32,
		},
		ShareInfo: ShareInfo{
			ShareData: ShareData{
				PreviousShareHash: shareChain.GetTipHash(),
				CoinBase:          coinbaseTxBytes,
				Nonce:             nonceUint32,
				PubKeyHash:        pkh,
				PubKeyHashVersion: pkhVersion,
				Subsidy:           uint64(job.CoinbaseValue),
				Donation:          uint16(config.Active.Fee * 100),
			},
			SegwitData: &SegwitData{
				TXIDMerkleLink: MerkleLink{
					Branch: txidMerkleLinkBranches,
					Index:  0,
				},
				WTXIDMerkleRoot: wtxidMerkleRoot,
			},
			NewTransactionHashes: newTxHashesForShareInfo,
			TransactionHashRefs:  []TransactionHashRef{},
			FarShareHash:         nil,
			MaxBits:              shareBits,
			Bits:                 shareBits,
			Timestamp:            int32(nTimeUint32),
			AbsHeight:            int32(job.Height),
			AbsWork:              new(big.Int),
		},
		MerkleLink:    MerkleLink{Branch: merkleLinkBranches, Index: 0},
		RefMerkleLink: MerkleLink{Branch: []*chainhash.Hash{}, Index: 0},
	}

	err = share.CalculateHashes()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate hashes for new share: %v", err)
	}

	logging.Infof("Successfully created new share with hash %s and populated SegwitData", share.Hash.String()[:12])
	return share, nil
}
