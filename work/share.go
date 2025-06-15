package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
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
	// --- THIS IS THE FIX ---
	// The nonce from the miner is Big Endian, but the header requires Little Endian.
	// We must reverse the bytes to match the correct format.
	header.Write(ReverseBytes(nonceBytes))
	// --- END FIX ---

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

