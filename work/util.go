package work

import (
	"crypto/sha256"
	"math/big"
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
// This new version correctly implements the p2pool formula.
func DiffToTarget(diff float64) *big.Int {
	// The p2pool target formula is (2^256 - 1) / difficulty
	
	// Create a big.Int for (2^256 - 1)
	maxTarget := new(big.Int)
	maxTarget.SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	// To perform the division with a float, we can multiply both sides by a large
	// power of 2 to work with integers, then divide.
	// For example, multiply by 2^32
	scale := new(big.Int).Lsh(big.NewInt(1), 32)

	// (maxTarget * scale) / (difficulty * scale)
	num := new(big.Int).Mul(maxTarget, scale)
	den := new(big.Int).SetInt64(int64(diff * float64(scale.Int64())))

	target := new(big.Int).Div(num, den)
	return target
}

