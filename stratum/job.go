package stratum

import (
	"github.com/CADMonkey21/p2pool-go-VTC/work"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

type Job struct {
	ID            string
	BlockTemplate *work.BlockTemplate
	ExtraNonce1   string
	Difficulty    float64
	Coinb1        string
	Coinb2        string

	WitnessCommitment  []byte
	WTXIDMerkleRoot    *chainhash.Hash
	TXIDMerkleLink     []*chainhash.Hash
	CoinbaseMerkleLink []*chainhash.Hash
}
