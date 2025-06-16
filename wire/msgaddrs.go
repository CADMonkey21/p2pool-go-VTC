package wire

import (
	"bytes"
)

var _ P2PoolMessage = &MsgAddrs{}

type Addr struct {
	Address P2PoolAddress
	Service uint64
}

type MsgAddrs struct {
	Addresses []Addr
}

func (m *MsgAddrs) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	count, err := ReadVarInt(r)
	if err != nil {
		return err
	}

	m.Addresses = make([]Addr, count)
	for i := uint64(0); i < count; i++ {
		m.Addresses[i].Address, err = ReadP2PoolAddress(r)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *MsgAddrs) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	WriteVarInt(&buf, uint64(len(m.Addresses)))
	for _, a := range m.Addresses {
		addrBytes, _ := a.Address.ToBytes()
		buf.Write(addrBytes)
	}
	return buf.Bytes(), nil
}

func (m *MsgAddrs) Command() string {
	return "addrs"
}
