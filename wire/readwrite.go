package wire

import (
	"encoding/binary"
	"io"
	"net"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var nullHash chainhash.Hash

func ReadVarInt(r io.Reader) (uint64, error) {
	var d uint8
	err := binary.Read(r, binary.LittleEndian, &d)
	if err != nil {
		return 0, err
	}
	var rv uint64
	switch d {
	case 0xff:
		err = binary.Read(r, binary.LittleEndian, &rv)
	case 0xfe:
		var v uint32
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
	case 0xfd:
		var v uint16
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
	default:
		rv = uint64(d)
	}
	return rv, err
}

func WriteVarInt(w io.Writer, val uint64) error {
	if val < 0xfd {
		return binary.Write(w, binary.LittleEndian, uint8(val))
	} else if val <= 0xffff {
		binary.Write(w, binary.LittleEndian, uint8(0xfd))
		return binary.Write(w, binary.LittleEndian, uint16(val))
	} else if val <= 0xffffffff {
		binary.Write(w, binary.LittleEndian, uint8(0xfe))
		return binary.Write(w, binary.LittleEndian, uint32(val))
	} else {
		binary.Write(w, binary.LittleEndian, uint8(0xff))
		return binary.Write(w, binary.LittleEndian, val)
	}
}

func ReadVarString(r io.Reader) (string, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", nil
	}
	buf := make([]byte, count)
	_, err = io.ReadFull(r, buf)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func WriteVarString(w io.Writer, s string) error {
	err := WriteVarInt(w, uint64(len(s)))
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(s))
	return err
}

func ReadIPAddr(r io.Reader) (net.IP, error) {
	b := make([]byte, 16)
	_, err := io.ReadFull(r, b)
	return net.IP(b), err
}

func WriteIPAddr(w io.Writer, ip net.IP) error {
	_, err := w.Write(ip.To16())
	return err
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

func ReadChainHash(r io.Reader) (*chainhash.Hash, error) {
	b := make([]byte, 32)
	_, err := io.ReadFull(r, b)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(b)
}

func WriteChainHash(w io.Writer, h *chainhash.Hash) error {
	if h == nil {
		h = &nullHash
	}
	_, err := w.Write(h.CloneBytes())
	return err
}

func ReadChainHashList(r io.Reader) ([]*chainhash.Hash, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	hashes := make([]*chainhash.Hash, count)
	for i := uint64(0); i < count; i++ {
		hashes[i], err = ReadChainHash(r)
		if err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

func WriteChainHashList(w io.Writer, l []*chainhash.Hash) error {
	err := WriteVarInt(w, uint64(len(l)))
	if err != nil {
		return err
	}
	for _, h := range l {
		err = WriteChainHash(w, h)
		if err != nil {
			return err
		}
	}
	return nil
}

func ReadShares(r io.Reader) ([]Share, error) {
	shares := make([]Share, 0)
	count, err := ReadVarInt(r)
	if err != nil {
		return shares, err
	}
	if count > 0 {
		logging.Debugf("Deserializing %d shares (placeholder logic)", count)
		// Placeholder: Read a large chunk of data to consume the message payload
		// This is temporary until full deserialization is implemented.
		dummy := make([]byte, 1024*20) // Read up to 20KB to be safe
		r.Read(dummy)
	}
	return shares, nil
}

func WriteShares(w io.Writer, shares []Share) error {
	// Placeholder
	return nil
}
