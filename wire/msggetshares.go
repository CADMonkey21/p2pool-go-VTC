package wire

import (
	"bytes"
	"encoding/binary"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgGetShares{}

type MsgGetShares struct {
	Hashes  []*chainhash.Hash
	Parents uint32
	ID      uint32
	Stops   *chainhash.Hash
}

func (m *MsgGetShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	if m.Hashes, err = ReadChainHashList(r); err != nil {
		return err
	}
	if r.Len() >= 8 {
		if err = binary.Read(r, binary.LittleEndian, &m.Parents); err != nil {
			return err
		}
		if err = binary.Read(r, binary.LittleEndian, &m.ID); err != nil {
			return err
		}
	}
	if r.Len() > 0 {
		m.Stops, err = ReadChainHash(r)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *MsgGetShares) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteChainHashList(&buf, m.Hashes); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, m.Parents); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, m.ID); err != nil {
		return nil, err
	}
	if err := WriteChainHash(&buf, m.Stops); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (m *MsgGetShares) Command() string {
	return "get_shares"
}
