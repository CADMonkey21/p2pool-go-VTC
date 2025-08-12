package wire

import (
	"bytes"
	"io"
	"net"
)

type P2PoolAddress struct {
	Services uint64
	Address  net.IP
	Port     int16
}

func (pa *P2PoolAddress) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteIPAddr(&buf, pa)
	return buf.Bytes(), err
}

func (pa *P2PoolAddress) FromBytes(r io.Reader) error {
	addr, err := ReadP2PoolAddress(r)
	if err != nil {
		return err
	}
	*pa = *addr
	return nil
}
