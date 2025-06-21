package wire

import (
	"bytes"
	"encoding/binary"
)

var _ P2PoolMessage = &MsgAddrs{}

// Addr now includes the timestamp that prefixes each address in an 'addrs' message.
// The 'Service' field was redundant and has been removed.
type Addr struct {
	Timestamp uint64
	Address   P2PoolAddress
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
		// Read the 8-byte timestamp first, as required by the protocol.
		err = binary.Read(r, binary.LittleEndian, &m.Addresses[i].Timestamp)
		if err != nil {
			return err
		}

		// Then read the address payload.
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
		// Write the 8-byte timestamp first.
		binary.Write(&buf, binary.LittleEndian, a.Timestamp)

		// Then write the address payload.
		addrBytes, _ := a.Address.ToBytes()
		buf.Write(addrBytes)
	}
	return buf.Bytes(), nil
}

func (m *MsgAddrs) Command() string {
	return "addrs"
}
