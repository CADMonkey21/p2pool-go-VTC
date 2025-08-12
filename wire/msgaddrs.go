package wire

import (
	"bytes"
)

var _ P2PoolMessage = &MsgAddrs{}

type MsgAddrs struct {
	Addresses []*P2PoolAddress
}

func (m *MsgAddrs) Command() string {
	return "addrs"
}

func (m *MsgAddrs) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteVarInt(&buf, uint64(len(m.Addresses)))
	if err != nil {
		return nil, err
	}
	for _, addr := range m.Addresses {
		err = WriteIPAddr(&buf, addr)
		if err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (m *MsgAddrs) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	count, err := ReadVarInt(r)
	if err != nil {
		return err
	}
	m.Addresses = make([]*P2PoolAddress, count)
	for i := uint64(0); i < count; i++ {
		m.Addresses[i], err = ReadP2PoolAddress(r)
		if err != nil {
			return err
		}
	}
	return nil
}
