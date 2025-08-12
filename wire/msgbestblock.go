package wire

import (
	"bytes"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgBestBlock{}

type MsgBestBlock struct {
	BestBlock *chainhash.Hash
}

func (m *MsgBestBlock) Command() string {
	return "best_block"
}

func (m *MsgBestBlock) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.BestBlock, err = ReadChainHash(r)
	if m.BestBlock == nil {
		m.BestBlock = &nullHash
	}
	return err
}

func (m *MsgBestBlock) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	if m.BestBlock == nil {
		m.BestBlock = &nullHash
	}
	err := WriteChainHash(&buf, m.BestBlock)
	return buf.Bytes(), err
}
