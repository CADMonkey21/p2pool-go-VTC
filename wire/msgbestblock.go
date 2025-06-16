package wire

import (
	"bytes"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgBestBlock{}

type MsgBestBlock struct {
	TxHash *chainhash.Hash
}

func (m *MsgBestBlock) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.TxHash, err = ReadChainHash(r)
	if err != nil {
		m.TxHash = &nullHash
	}
	return nil
}

func (m *MsgBestBlock) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	if m.TxHash == nil {
		m.TxHash = &nullHash
	}
	WriteChainHash(&buf, m.TxHash)
	return buf.Bytes(), nil
}

func (m *MsgBestBlock) Command() string {
	return "best_block"
}

