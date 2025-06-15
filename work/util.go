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
func DiffToTarget(diff float64) *big.Int {
	maxTarget := new(big.Int)
	maxTarget.SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	scale := new(big.Int).Lsh(big.NewInt(1), 32)

	num := new(big.Int).Mul(maxTarget, scale)
	den := new(big.Int).SetInt64(int64(diff * float64(scale.Int64())))

	target := new(big.Int).Div(num, den)
	return target
}

