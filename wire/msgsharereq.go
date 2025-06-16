package wire

import (
	"bytes"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgShareReq{}

type MsgShareReq struct {
	Hashes []*chainhash.Hash
	Stops  *chainhash.Hash
}

func (m *MsgShareReq) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Hashes, err = ReadChainHashList(r)
	if err != nil {
		return err
	}
	m.Stops, err = ReadChainHash(r)
	if err != nil {
		return err
	}
	return nil
}

func (m *MsgShareReq) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	WriteChainHashList(&buf, m.Hashes)
	WriteChainHash(&buf, m.Stops)
	return buf.Bytes(), nil
}

func (m *MsgShareReq) Command() string {
	return "sharereq"
}
