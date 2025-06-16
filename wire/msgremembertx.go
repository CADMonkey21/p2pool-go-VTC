package wire

import (
	"bytes"
)

var _ P2PoolMessage = &MsgRememberTx{}

type MsgRememberTx struct {
	ShareVersion uint64
	TxHashes     [][]byte
}

func (m *MsgRememberTx) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.ShareVersion, err = ReadVarInt(r)
	if err != nil {
		return err
	}
	count, err := ReadVarInt(r)
	if err != nil {
		return err
	}
	m.TxHashes = make([][]byte, count)
	for i := uint64(0); i < count; i++ {
		m.TxHashes[i] = make([]byte, 32)
		r.Read(m.TxHashes[i])
	}
	return nil
}

func (m *MsgRememberTx) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	WriteVarInt(&buf, m.ShareVersion)
	WriteVarInt(&buf, uint64(len(m.TxHashes)))
	for _, h := range m.TxHashes {
		buf.Write(h)
	}
	return buf.Bytes(), nil
}

func (m *MsgRememberTx) Command() string {
	return "remember_tx"
}
