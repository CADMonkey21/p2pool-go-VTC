package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pwire "github.com/CADMonkey21/p2pool-go-VTC/wire"
)

// calculateMerkleRoot is a helper function for header creation.
func calculateMerkleRoot(hashes [][]byte) []byte {
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}
		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			combined := append(hashes[i], hashes[i+1]...)
			newHash := DblSha256(combined)
			nextLevel = append(nextLevel, newHash)
		}
		hashes = nextLevel
	}
	return hashes[0]
}

// CreateShare takes a successful submission and converts it into a p2pool share object.
func CreateShare(job *BlockTemplate, extraNonce1, extraNonce2, nTimeHex, nonceHex, payoutAddress string, shareChain *ShareChain, stratumDifficulty float64) (*p2pwire.Share, error) {
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, err
	}

	nTimeBytes, err := hex.DecodeString(nTimeHex)
	if err != nil {
		return nil, err
	}
	nTimeUint32 := binary.BigEndian.Uint32(nTimeBytes)

	// Patched Address Handling
	var pkh []byte
	var pkhVersion byte

	if strings.HasPrefix(strings.ToLower(payoutAddress), "vtc1") || strings.HasPrefix(strings.ToLower(payoutAddress), "tvtc1") {
		// Handle modern Bech32 address
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
		// Handle legacy Base58 address
		decoded58 := base58.Decode(payoutAddress)
		if len(decoded58) < 5 { // Base58 addresses are longer than 4 bytes
			return nil, fmt.Errorf("invalid base58 address length for '%s'", payoutAddress)
		}
		pkhVersion = decoded58[0]
		pkh = decoded58[1 : len(decoded58)-4] // Exclude version and 4-byte checksum
	}
	// End Patched Address Handling

	nBitsBytes, err := hex.DecodeString(job.Bits)
	if err != nil {
		return nil, fmt.Errorf("could not decode bits from template: %v", err)
	}
	shareBits := binary.BigEndian.Uint32(nBitsBytes)

	prevBlockHash, _ := chainhash.NewHashFromStr(job.PreviousBlockHash)
	nonceUint32 := binary.BigEndian.Uint32(nonceBytes)

	wtxidMerkleRoot, witnessCommitment, err := CalculateWitnessCommitment(job)
	if err != nil {
		return nil, err
	}

	coinbaseTxBytes, err := CreateCoinbaseTx(job, payoutAddress, extraNonce1, extraNonce2, witnessCommitment.CloneBytes())
	if err != nil {
		return nil, err
	}

	coinbaseWtxid, _ := chainhash.NewHashFromStr("0000000000000000000000000000000000000000000000000000000000000000")
	wtxids := []*chainhash.Hash{coinbaseWtxid}
	for _, txTmpl := range job.Transactions {
		txBytes, _ := hex.DecodeString(txTmpl.Data)
		var msgTx wire.MsgTx
		_ = msgTx.Deserialize(bytes.NewReader(txBytes))
		wtxid := msgTx.WitnessHash()
		wtxids = append(wtxids, &wtxid)
	}
	txidMerkleLinkBranches := CalculateMerkleLinkFromHashes(wtxids, 0)

	coinbaseTxHash := DblSha256(coinbaseTxBytes)
	txHashesForLink := [][]byte{ReverseBytes(coinbaseTxHash)}
	for _, tx := range job.Transactions {
		txHashBytes, _ := hex.DecodeString(tx.Hash)
		// CORRECTED: Reverse the byte order of the transaction hashes
		txHashesForLink = append(txHashesForLink, ReverseBytes(txHashBytes))
	}
	merkleLinkBranches := CalculateMerkleLink(txHashesForLink, 0)

	newTxHashesForShareInfo := []*chainhash.Hash{}
	for _, tx := range job.Transactions {
		h, _ := chainhash.NewHashFromStr(tx.TxID)
		newTxHashesForShareInfo = append(newTxHashesForShareInfo, h)
	}

	share := &p2pwire.Share{
		Type: 23,
		MinHeader: p2pwire.SmallBlockHeader{
			Version:       int32(job.Version),
			PreviousBlock: prevBlockHash,
			Timestamp:     nTimeUint32,
			Bits:          shareBits,
			Nonce:         nonceUint32,
		},
		ShareInfo: p2pwire.ShareInfo{
			ShareData: p2pwire.ShareData{
				PreviousShareHash: shareChain.GetTipHash(),
				CoinBase:          coinbaseTxBytes,
				Nonce:             nonceUint32,
				PubKeyHash:        pkh,
				PubKeyHashVersion: pkhVersion,
				Subsidy:           uint64(job.CoinbaseValue),
				Donation:          uint16(config.Active.Fee * 100),
			},
			SegwitData: &p2pwire.SegwitData{
				TXIDMerkleLink: p2pwire.MerkleLink{
					Branch: txidMerkleLinkBranches,
					Index:  0,
				},
				WTXIDMerkleRoot: wtxidMerkleRoot,
			},
			NewTransactionHashes: newTxHashesForShareInfo,
			TransactionHashRefs:  []p2pwire.TransactionHashRef{},
			FarShareHash:         nil,
			MaxBits:              shareBits,
			Bits:                 shareBits,
			Timestamp:            int32(nTimeUint32),
			AbsHeight:            int32(job.Height),
			AbsWork:              new(big.Int),
		},
		MerkleLink:    p2pwire.MerkleLink{Branch: merkleLinkBranches, Index: 0},
		RefMerkleLink: p2pwire.MerkleLink{Branch: []*chainhash.Hash{}, Index: 0},
	}

	err = share.CalculateHashes()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate hashes for new share: %v", err)
	}

	logging.Infof("Successfully created new share with hash %s and populated SegwitData", share.Hash.String()[:12])
	return share, nil
}
