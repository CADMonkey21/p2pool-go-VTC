package wire

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgVersion{}

type MsgVersion struct {
	Version       uint32
	Services      uint64
	Timestamp     int64
	AddrTo        P2PoolAddress
	AddrFrom      P2PoolAddress
	Nonce         int64
	SubVersion    string
	Mode          int32
	BestShareHash *chainhash.Hash
}

func (m *MsgVersion) Command() string {
	return "version"
}

func (m *MsgVersion) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	err := binary.Read(r, binary.LittleEndian, &m.Version)
	if err != nil {
		return err
	}
	err = binary.Read(r, binary.LittleEndian, &m.Services)
	if err != nil {
		return err
	}
	err = binary.Read(r, binary.LittleEndian, &m.Timestamp)
	if err != nil {
		return err
	}
	addrTo, err := ReadP2PoolAddress(r)
	if err != nil {
		return err
	}
	m.AddrTo = *addrTo

	addrFrom, err := ReadP2PoolAddress(r)
	if err != nil {
		return err
	}
	m.AddrFrom = *addrFrom

	err = binary.Read(r, binary.LittleEndian, &m.Nonce)
	if err != nil {
		return err
	}
	subBytes, err := ReadVarString(r)
	if err != nil {
		return err
	}
	m.SubVersion = string(subBytes)
	err = binary.Read(r, binary.LittleEndian, &m.Mode)
	if err != nil {
		return err
	}
	m.BestShareHash, err = ReadChainHash(r)
	return err
}

func (m *MsgVersion) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := binary.Write(&buf, binary.LittleEndian, m.Version)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&buf, binary.LittleEndian, m.Services)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&buf, binary.LittleEndian, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	err = WriteIPAddr(&buf, &m.AddrTo)
	if err != nil {
		return nil, err
	}
	err = WriteIPAddr(&buf, &m.AddrFrom)
	if err != nil {
		return nil, err
	}
	err = binary.Write(&buf, binary.LittleEndian, m.Nonce)
	if err != nil {
		return nil, err
	}
	err = WriteVarString(&buf, []byte(m.SubVersion))
	if err != nil {
		return nil, err
	}
	err = binary.Write(&buf, binary.LittleEndian, m.Mode)
	if err != nil {
		return nil, err
	}
	err = WriteChainHash(&buf, m.BestShareHash)
	return buf.Bytes(), err
}
