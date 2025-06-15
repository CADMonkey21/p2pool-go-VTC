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

	coinbaseTxBytes, err := CreateCoinbaseTx(job, payoutAddress, extraNonce1, extraNonce2)
	if err != nil { return nil, err }
	coinbaseTxHash, _ := chainhash.NewHash(DblSha256(coinbaseTxBytes))

	header, merkleRootBytes, err := CreateHeader(job, extraNonce1, extraNonce2, nTimeHex, nonceHex, payoutAddress)
	if err != nil { return nil, err }

	powHashBytes := p2pnet.ActiveNetwork.POWHash(header)
	powHash, _ := chainhash.NewHash(powHashBytes)
	shareHash, _ := chainhash.NewHash(DblSha256(header))
	merkleRoot, _ := chainhash.NewHash(merkleRootBytes)
	nonceUint32 := binary.BigEndian.Uint32(nonceBytes)

	share := &wire.Share{
		Type: 17,
		MinHeader: wire.SmallBlockHeader{
			Version:       int32(job.Version),
			PreviousBlock: prevBlockHash,
			Timestamp:     binary.LittleEndian.Uint32(nTimeBytes),
			Bits:          binary.LittleEndian.Uint32(nBitsBytes),
			Nonce:         nonceUint32,
		},
		ShareInfo: wire.ShareInfo{
			ShareData: wire.ShareData{
				PreviousShareHash: shareChain.GetTipHash(),
				CoinBase:          hex.EncodeToString(coinbaseTxBytes),
				Nonce:             nonceUint32,
				PubKeyHash:        make([]byte, 20),
				Subsidy:           uint64(job.CoinbaseValue),
				Donation:          uint16(config.Active.Fee * 100),
			},
			Bits:      int32(binary.LittleEndian.Uint32(nBitsBytes)),
			AbsHeight: int32(job.Height),
			AbsWork:   new(big.Int),
		},
		MerkleRoot: merkleRoot,
		GenTXHash:  coinbaseTxHash,
		Hash:       shareHash,
		POWHash:    powHash,
	}

	logging.Infof("Successfully created new share with hash %s", share.Hash.String()[:12])
	return share, nil
}

