package work

import (
	"bytes"
	"encoding/hex"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// CalculateWitnessCommitment calculates the wtxid merkle root and the coinbase commitment
// for all transactions in a block template. This is the core of fulfilling the
// SegWit commitment required by modern p2pool shares.
func CalculateWitnessCommitment(tmpl *BlockTemplate) (witnessRoot *chainhash.Hash, witnessReservedValue *chainhash.Hash, err error) {
	// The first transaction is the coinbase, its wtxid is always all zeroes.
	coinbaseWtxid, _ := chainhash.NewHashFromStr("0000000000000000000000000000000000000000000000000000000000000000")
	wtxids := []*chainhash.Hash{coinbaseWtxid}

	// For all other transactions from the template, deserialize them and get their wtxid.
	for _, txTmpl := range tmpl.Transactions {
		txBytes, err := hex.DecodeString(txTmpl.Data)
		if err != nil {
			return nil, nil, err
		}

		var msgTx wire.MsgTx
		err = msgTx.Deserialize(bytes.NewReader(txBytes))
		if err != nil {
			return nil, nil, err
		}

		// For non-segwit txs, wtxid is the same as txid. For segwit txs, this computes the real wtxid.
		wtxid := msgTx.WitnessHash()
		wtxids = append(wtxids, &wtxid)
	}

	// The witness root is the merkle root of all the wtxids.
	witnessMerkleRoot := calculateMerkleRootFromHashes(wtxids)

	// The witness reserved value is a hash of the witness root and a nonce (all zeroes for now).
	// This is part of the BIP141 commitment structure.
	witnessNonce, _ := chainhash.NewHashFromStr("0000000000000000000000000000000000000000000000000000000000000000")
	combined := append(witnessMerkleRoot.CloneBytes(), witnessNonce.CloneBytes()...)
	commitmentHash := DblSha256(combined)
	finalCommitment, _ := chainhash.NewHash(commitmentHash)

	return witnessMerkleRoot, finalCommitment, nil
}

// calculateMerkleRootFromHashes is a generic merkle root calculator that takes a list of hashes.
// This is slightly different from the one that takes [][]byte.
func calculateMerkleRootFromHashes(hashes []*chainhash.Hash) *chainhash.Hash {
	if len(hashes) == 0 {
		return &chainhash.Hash{}
	}

	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1]) // Duplicate last hash if odd number
		}
		var nextLevel []*chainhash.Hash
		for i := 0; i < len(hashes); i += 2 {
			combined := append(hashes[i][:], hashes[i+1][:]...)
			newHashBytes := DblSha256(combined)
			newHash, _ := chainhash.NewHash(newHashBytes)
			nextLevel = append(nextLevel, newHash)
		}
		hashes = nextLevel
	}
	return hashes[0]
}
