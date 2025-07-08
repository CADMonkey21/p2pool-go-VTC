package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-vtc/config"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-vtc/net"
	p2pwire "github.com/CADMonkey21/p2pool-go-vtc/wire"
)

// CreateHeader reconstructs the block header from template and miner data.
func CreateHeader(tmpl *BlockTemplate, extraNonce1, extraNonce2, nTime, nonceHex, payoutAddress string) ([]byte, []byte, error) {
	_, witnessCommitment, err := CalculateWitnessCommitment(tmpl)
	if err != nil {
		return nil, nil, err
	}

	coinbaseTxBytes, err := CreateCoinbaseTx(tmpl, payoutAddress, extraNonce1, extraNonce2, witnessCommitment.CloneBytes())
	if err != nil {
		return nil, nil, err
	}
	coinbaseTxHashBytes := DblSha256(coinbaseTxBytes)

	txHashesForMerkle := [][]byte{coinbaseTxHashBytes}
	for _, tx := range tmpl.Transactions {
		txHashBytes, _ := hex.DecodeString(tx.Hash)
		txHashesForMerkle = append(txHashesForMerkle, ReverseBytes(txHashBytes))
	}
	merkleRootBytes := calculateMerkleRoot(txHashesForMerkle)

	versionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(versionBytes, tmpl.Version)

	prevHashBytes, err := hex.DecodeString(tmpl.PreviousBlockHash)
	if err != nil {
		return nil, nil, err
	}

	nTimeBytes, err := hex.DecodeString(nTime)
	if err != nil {
		return nil, nil, err
	}

	nBitsBytes, err := hex.DecodeString(tmpl.Bits)
	if err != nil {
		return nil, nil, err
	}

	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, nil, err
	}

	var header bytes.Buffer
	header.Write(versionBytes)
	header.Write(ReverseBytes(prevHashBytes))
	header.Write(ReverseBytes(merkleRootBytes))
	header.Write(ReverseBytes(nTimeBytes)) // Timestamps in headers are little-endian
	header.Write(nBitsBytes)
	header.Write(ReverseBytes(nonceBytes))

	return header.Bytes(), merkleRootBytes, nil
}

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
	// The nTime from stratum is big-endian, so we read it that way to get the correct unix time.
	nTimeUint32 := binary.BigEndian.Uint32(nTimeBytes)

	hrp, decoded, err := bech32.Decode(payoutAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to decode bech32 payout address '%s': %v", payoutAddress, err)
	}
	if hrp != "vtc" && hrp != "tvtc" {
		return nil, fmt.Errorf("address is not a valid vertcoin bech32 address (hrp: %s)", hrp)
	}
	pkhVersion := decoded[0]
	pkh := decoded[1:]

	shareTarget := DiffToTarget(stratumDifficulty)
	shareBits := blockchain.BigToCompact(shareTarget)

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

	header, _, err := CreateHeader(job, extraNonce1, extraNonce2, nTimeHex, nonceHex, payoutAddress)
	if err != nil {
		return nil, err
	}

	powHashBytes := p2pnet.ActiveNetwork.POWHash(header)
	powHash, _ := chainhash.NewHash(powHashBytes)
	shareHash, _ := chainhash.NewHash(DblSha256(header))

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
	txHashesForLink := [][]byte{coinbaseTxHash}
	for _, tx := range job.Transactions {
		txHashBytes, _ := hex.DecodeString(tx.Hash)
		txHashesForLink = append(txHashesForLink, ReverseBytes(txHashBytes))
	}
	merkleLinkBranches := CalculateMerkleLink(txHashesForLink, 0)

	newTxHashesForShareInfo := []*chainhash.Hash{}
	for _, tx := range job.Transactions {
		h, _ := chainhash.NewHashFromStr(tx.TxID)
		newTxHashesForShareInfo = append(newTxHashesForShareInfo, h)
	}

	share := &p2pwire.Share{
		Type: 17,
		MinHeader: p2pwire.SmallBlockHeader{
			Version:       int32(job.Version),
			PreviousBlock: prevBlockHash,
			Timestamp:     nTimeUint32, // Use corrected timestamp
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
			Timestamp:            int32(nTimeUint32), // Use corrected timestamp
			AbsHeight:            int32(job.Height),
			AbsWork:              new(big.Int),
		},
		MerkleLink:    p2pwire.MerkleLink{Branch: merkleLinkBranches, Index: 0},
		RefMerkleLink: p2pwire.MerkleLink{Branch: []*chainhash.Hash{}, Index: 0},
		POWHash:       powHash,
		Hash:          shareHash,
	}

	logging.Infof("Successfully created new share with hash %s and populated SegwitData", share.Hash.String()[:12])
	return share, nil
}
