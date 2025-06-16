package wire

import (
	"bytes"
)

var _ P2PoolMessage = &MsgForgetTx{}

type MsgForgetTx struct {
	Hashes []byte
}

func (m *MsgForgetTx) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	count, err := ReadVarInt(r)
	if err != nil {
		return err
	}

	m.Hashes = make([]byte, count*32)
	r.Read(m.Hashes)

	return nil
}

func (m *MsgForgetTx) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	WriteVarInt(&buf, uint64(len(m.Hashes)/32))
	buf.Write(m.Hashes)
	return buf.Bytes(), nil
}

func (m *MsgForgetTx) Command() string {
	return "forget_tx"
}
