package work

import (
	"crypto/sha256"
	"math/big"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// DblSha256 is a helper for Bitcoin's standard hashing algorithm.
func DblSha256(b []byte) []byte {
	a := sha256.Sum256(b)
	c := sha256.Sum256(a[:])
	return c[:]
}

// ReverseBytes is a helper for handling endianness differences.
func ReverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		r[i] = b[len(b)-1-i]
	}
	return r
}

// DiffToTarget converts a stratum difficulty float into a 256-bit target.
func DiffToTarget(diff float64) *big.Int {
	// The universally recognized "difficulty 1" target for most coins.
	// 0x00000000FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF
	diff1 := "00000000FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
	maxTarget := new(big.Int)
	maxTarget.SetString(diff1, 16)

	// Use big.Float for precise division to avoid rounding errors
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	diffFloat := new(big.Float).SetFloat64(diff)

	targetFloat := new(big.Float).Quo(maxTargetFloat, diffFloat)

	targetInt, _ := targetFloat.Int(nil)
	return targetInt
}

// CalculateMerkleLink computes the merkle branches for a transaction at a given index.
func CalculateMerkleLink(hashes [][]byte, index int) []*chainhash.Hash {
	branches := []*chainhash.Hash{}
	for len(hashes) > 1 {
		if index^1 < len(hashes) {
			h, _ := chainhash.NewHash(hashes[index^1])
			branches = append(branches, h)
		}

		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				combined := append(hashes[i], hashes[i+1]...)
				newHash := DblSha256(combined)
				nextLevel = append(nextLevel, newHash)
			} else {
				nextLevel = append(nextLevel, hashes[i])
			}
		}
		hashes = nextLevel
		index /= 2
	}
	return branches
}

// CalculateMerkleLinkFromHashes computes the merkle branches from a list of chainhash.Hash pointers.
// This is needed for the WTXID merkle tree calculation.
func CalculateMerkleLinkFromHashes(hashes []*chainhash.Hash, index int) []*chainhash.Hash {
	branches := []*chainhash.Hash{}
	for len(hashes) > 1 {
		if index^1 < len(hashes) {
			branches = append(branches, hashes[index^1])
		}

		var nextLevel []*chainhash.Hash
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				combined := append(hashes[i][:], hashes[i+1][:]...)
				newHashBytes := DblSha256(combined)
				newHash, _ := chainhash.NewHash(newHashBytes)
				nextLevel = append(nextLevel, newHash)
			} else {
				nextLevel = append(nextLevel, hashes[i])
			}
		}
		hashes = nextLevel
		index /= 2
	}
	return branches
}
