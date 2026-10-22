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
// Uses VerthashMiner's native Diff-1 target (0x000000ffff000000..., 2^24 = 16,777,216 hashes per diff 1).
func DiffToTarget(diff float64) *big.Int {
	if diff <= 0 {
		diff = 0.000001
	}
	maxTarget := new(big.Int)
	maxTarget.SetString("000000FFFF000000000000000000000000000000000000000000000000000000", 16)

	maxTargetFloat := new(big.Float).SetPrec(256).SetInt(maxTarget)
	diffFloat := new(big.Float).SetPrec(256).SetFloat64(diff)

	targetFloat := new(big.Float).SetPrec(256).Quo(maxTargetFloat, diffFloat)

	targetInt, _ := targetFloat.Int(nil)
	return targetInt
}

// TargetToDiff converts a 256-bit target or POWHash int into a Verthash difficulty float.
func TargetToDiff(target *big.Int) float64 {
	if target == nil || target.Sign() <= 0 {
		return 0
	}
	maxTarget, _ := new(big.Int).SetString("000000FFFF000000000000000000000000000000000000000000000000000000", 16)
	maxTargetFloat := new(big.Float).SetPrec(256).SetInt(maxTarget)
	targetFloat := new(big.Float).SetPrec(256).SetInt(target)
	diffFloatBig := new(big.Float).SetPrec(256).Quo(maxTargetFloat, targetFloat)
	diffFloat, _ := diffFloatBig.Float64()
	return diffFloat
}

// CalculateMerkleLink computes the merkle branches for a transaction at a given index.
func CalculateMerkleLink(hashes [][]byte, index int) []*chainhash.Hash {
	branches := []*chainhash.Hash{}
	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}

		h, _ := chainhash.NewHash(hashes[index^1])
		branches = append(branches, h)

		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			combined := append(hashes[i], hashes[i+1]...)
			newHash := DblSha256(combined)
			nextLevel = append(nextLevel, newHash)
		}
		hashes = nextLevel
		index /= 2
	}
	return branches
}

// CalculateMerkleLinkFromHashes computes the merkle branches from a list of chainhash.Hash pointers.
func CalculateMerkleLinkFromHashes(hashes []*chainhash.Hash, index int) []*chainhash.Hash {
	branches := []*chainhash.Hash{}
	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}

		clone := *hashes[index^1]
		branches = append(branches, &clone)

		var nextLevel []*chainhash.Hash
		for i := 0; i < len(hashes); i += 2 {
			combined := append(hashes[i][:], hashes[i+1][:]...)
			newHashBytes := DblSha256(combined)
			newHash, _ := chainhash.NewHash(newHashBytes)
			nextLevel = append(nextLevel, newHash)
		}
		hashes = nextLevel
		index /= 2
	}
	return branches
}
