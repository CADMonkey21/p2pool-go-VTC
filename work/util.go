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
	maxTarget := new(big.Int)
	maxTarget.SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	scale := new(big.Int).Lsh(big.NewInt(1), 32)

	num := new(big.Int).Mul(maxTarget, scale)
	den := new(big.Int).SetInt64(int64(diff * float64(scale.Int64())))

	target := new(big.Int).Div(num, den)
	return target
}

// CalculateMerkleLink computes the merkle branches for a transaction at a given index.
// The p2pool coinbase is always at index 0.
func CalculateMerkleLink(hashes [][]byte) ([]*chainhash.Hash, error) {
	branches := []*chainhash.Hash{}
	index := 0
	for len(hashes) > 1 {
		if index%2 == 1 {
			h, _ := chainhash.NewHash(hashes[index-1])
			branches = append(branches, h)
		} else if index < len(hashes)-1 {
			h, _ := chainhash.NewHash(hashes[index+1])
			branches = append(branches, h)
		}

		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				combined := append(hashes[i], hashes[i+1]...)
				newHash := DblSha256(combined)
				nextLevel = append(nextLevel, newHash)
			} else {
				// This handles an odd number of hashes in a level
				nextLevel = append(nextLevel, hashes[i])
			}
		}
		hashes = nextLevel
		index /= 2
	}
	return branches, nil
}

