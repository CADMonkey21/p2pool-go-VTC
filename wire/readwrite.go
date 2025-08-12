package wire

import (
	"encoding/binary"
	"io"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func ReadFixedBytes(r io.Reader, size int) ([]byte, error) {
	b := make([]byte, size)
	_, err := io.ReadFull(r, b)
	return b, err
}

func WriteFixedBytes(w io.Writer, b []byte, size int) error {
	if len(b) != size {
		b = make([]byte, size)
	}
	_, err := w.Write(b)
	return err
}

func ReadVarInt(r io.Reader) (uint64, error) {
	var n uint64
	err := binary.Read(r, binary.LittleEndian, &n)
	return n, err
}

func WriteVarInt(w io.Writer, n uint64) error {
	return binary.Write(w, binary.LittleEndian, n)
}

func ReadVarString(r io.Reader) ([]byte, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	return ReadFixedBytes(r, int(length))
}

func WriteVarString(w io.Writer, b []byte) error {
	err := WriteVarInt(w, uint64(len(b)))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}


func ReadChainHash(r io.Reader) (*chainhash.Hash, error) {
	b, err := ReadFixedBytes(r, 32)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(b)
}

func WriteChainHash(w io.Writer, h *chainhash.Hash) error {
	return WriteFixedBytes(w, h.CloneBytes(), 32)
}

func ReadPossiblyNoneHash(r io.Reader) (*chainhash.Hash, error) {
	isNone, err := ReadFixedBytes(r, 1)
	if err != nil {
		return nil, err
	}
	if isNone[0] == 0x01 {
		return ReadChainHash(r)
	}
	return &chainhash.Hash{}, nil
}

func WritePossiblyNoneHash(w io.Writer, h *chainhash.Hash) error {
	if h != nil && *h != (chainhash.Hash{}) {
		w.Write([]byte{0x01})
		return WriteChainHash(w, h)
	}
	w.Write([]byte{0x00})
	return nil
}

func ReadP2PoolAddress(r io.Reader) (*P2PoolAddress, error) {
	var addr P2PoolAddress
	err := binary.Read(r, binary.BigEndian, &addr.Services)
	if err != nil {
		return nil, err
	}
	ip, err := ReadFixedBytes(r, 16)
	if err != nil {
		return nil, err
	}
	addr.Address = ip
	err = binary.Read(r, binary.BigEndian, &addr.Port)
	return &addr, err
}

func WriteIPAddr(w io.Writer, addr *P2PoolAddress) error {
	err := binary.Write(w, binary.BigEndian, addr.Services)
	if err != nil {
		return err
	}
	err = WriteFixedBytes(w, addr.Address, 16)
	if err != nil {
		return err
	}
	return binary.Write(w, binary.BigEndian, addr.Port)
}

func ReadChainHashList(r io.Reader) ([]*chainhash.Hash, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	list := make([]*chainhash.Hash, count)
	for i := uint64(0); i < count; i++ {
		list[i], err = ReadChainHash(r)
		if err != nil {
			return nil, err
		}
	}
	return list, nil
}

func WriteChainHashList(w io.Writer, list []*chainhash.Hash) error {
	err := WriteVarInt(w, uint64(len(list)))
	if err != nil {
		return err
	}
	for _, h := range list {
		err = WriteChainHash(w, h)
		if err != nil {
			return err
		}
	}
	return nil
}

func ReadShares(r io.Reader) ([]Share, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	shares := make([]Share, count)
	return shares, nil
}

func WriteShares(w io.Writer, shares []Share) error {
	return WriteVarInt(w, uint64(len(shares)))
}
