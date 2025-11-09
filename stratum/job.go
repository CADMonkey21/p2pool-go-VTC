package stratum

import (
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

type Job struct {
	ID            string
	BlockTemplate *work.BlockTemplate
	ExtraNonce1   string
	Difficulty    float64 // This is the new field

	// [NEW] Fields for pre-calculated data
	WitnessCommitment    []byte
	WTXIDMerkleRoot      *chainhash.Hash
	TXIDMerkleLink       []*chainhash.Hash
	CoinbaseMerkleLink []*chainhash.Hash
}
