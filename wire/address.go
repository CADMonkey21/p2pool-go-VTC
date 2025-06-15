package wire

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
)

type P2PoolAddress struct {
	Services uint64
	Address  net.IP
	Port     int16
}

func (addr *P2PoolAddress) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, addr.Services)
	WriteIPAddr(&buf, addr.Address)
	binary.Write(&buf, binary.BigEndian, addr.Port)
	return buf.Bytes(), nil
}

func NewP2PoolAddress(c *net.TCPConn, services uint64) *P2PoolAddress {
	addr := P2PoolAddress{
		Services: services,
		Address:  c.RemoteAddr().(*net.TCPAddr).IP,
		Port:     int16(c.RemoteAddr().(*net.TCPAddr).Port),
	}
	return &addr
}

func ReadP2PoolAddress(r io.Reader) (P2PoolAddress, error) {
	addr := P2PoolAddress{}
	var err error

	err = binary.Read(r, binary.LittleEndian, &addr.Services)
	if err != nil {
		return addr, err
	}
	addr.Address, err = ReadIPAddr(r)
	if err != nil {
		return addr, err
	}
	err = binary.Read(r, binary.BigEndian, &addr.Port)
	if err != nil {
		return addr, err
	}

	return addr, nil
}

