package wire

import (
	"bytes"
	"encoding/binary"
	"net"
)

type P2PoolAddress struct {
	Services uint64
	Address  net.IP // This is net.IP
	Port     int16
}

func (addr *P2PoolAddress) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, addr.Services)
	// Use the corrected WriteIPAddr which takes net.IP
	err := WriteIPAddr(&buf, addr.Address)
	if err != nil {
		return nil, err
	}
	binary.Write(&buf, binary.BigEndian, addr.Port)
	return buf.Bytes(), nil
}

// NewP2PoolAddress creates a new P2PoolAddress from a TCP connection and services.
// No changes needed here, as it correctly uses net.IP.
func NewP2PoolAddress(c *net.TCPConn, services uint64) *P2PoolAddress {
	addr := P2PoolAddress{
		Services: services,
		Address:  c.RemoteAddr().(*net.TCPAddr).IP,
		Port:     int16(c.RemoteAddr().(*net.TCPAddr).Port),
	}
	return &addr
}
