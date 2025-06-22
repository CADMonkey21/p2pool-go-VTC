package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/big"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/wire"
)

// CreateHeader reconstructs the block header from template and miner data.
func CreateHeader(tmpl *BlockTemplate, extraNonce1, extraNonce2, nTime, nonceHex, payoutAddress string) ([]byte, []byte, error) {
	coinbaseTxBytes, err := CreateCoinbaseTx(tmpl, payoutAddress, extraNonce1, extraNonce2)
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
	header.Write(nTimeBytes)
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
func CreateShare(job *BlockTemplate, extraNonce1, extraNonce2, nTimeHex, nonceHex, payoutAddress string, shareChain *ShareChain) (*wire.Share, error) {
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil { return nil, err }

	nTimeBytes, err := hex.DecodeString(nTimeHex)
	if err != nil { return nil, err }

	nBitsBytes, err := hex.DecodeString(job.Bits)
	if err != nil { return nil, err }

	prevBlockHash, _ := chainhash.NewHashFromStr(job.PreviousBlockHash)
	nonceUint32 := binary.BigEndian.Uint32(nonceBytes)
	bitsUint32 := binary.LittleEndian.Uint32(nBitsBytes)

	coinbaseTxBytes, err := CreateCoinbaseTx(job, payoutAddress, extraNonce1, extraNonce2)
	if err != nil { return nil, err }

	header, _, err := CreateHeader(job, extraNonce1, extraNonce2, nTimeHex, nonceHex, payoutAddress)
	if err != nil { return nil, err }

	powHashBytes := p2pnet.ActiveNetwork.POWHash(header)
	powHash, _ := chainhash.NewHash(powHashBytes)
	shareHash, _ := chainhash.NewHash(DblSha256(header))

	txHashesForLink := [][]byte{DblSha256(coinbaseTxBytes)}
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

	share := &wire.Share{
		Type: 17, // Segwit share type
		Contents: wire.ShareContents{
			MinHeader: wire.SmallBlockHeader{
				Version:       int32(job.Version),
				PreviousBlock: prevBlockHash,
				Timestamp:     binary.LittleEndian.Uint32(nTimeBytes),
				Bits:          bitsUint32,
				Nonce:         nonceUint32,
			},
			ShareInfo: wire.ShareInfo{
				ShareData: wire.ShareData{
					PreviousShareHash: shareChain.GetTipHash(),
					CoinBase:          coinbaseTxBytes,
					Nonce:             nonceUint32,
					PubKeyHash:        make([]byte, 20),
					PubKeyHashVersion: 0,
					Subsidy:           uint64(job.CoinbaseValue),
					Donation:          uint16(config.Active.Fee * 100),
				},
				NewTransactionHashes: newTxHashesForShareInfo,
				TransactionHashRefs:  []wire.TransactionHashRef{},
				FarShareHash:         nil,
				MaxBits:              bitsUint32,
				Bits:                 bitsUint32,
				Timestamp:            int32(binary.LittleEndian.Uint32(nTimeBytes)),
				AbsHeight:            int32(job.Height),
				AbsWork:              new(big.Int),
			},
			MerkleLink: wire.MerkleLink{Branch: merkleLinkBranches, Index: 0},
			RefMerkleLink: wire.MerkleLink{Branch: []*chainhash.Hash{}, Index: 0}, // Placeholder
		},
		POWHash: powHash,
		Hash:    shareHash,
	}

	logging.Infof("Successfully created new share with hash %s", share.Hash.String()[:12])
	return share, nil
}
