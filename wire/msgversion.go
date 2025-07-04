package wire

import (
	"bytes"
	"encoding/binary"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgVersion{}

type MsgVersion struct {
	Version       int32
	Services      uint64
	AddrTo        P2PoolAddress
	AddrFrom      P2PoolAddress
	Nonce         int64
	SubVersion    string // Changed to string to match original declaration
	Mode          int32
	BestShareHash *chainhash.Hash
}

func (m *MsgVersion) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	
	binary.Write(&buf, binary.LittleEndian, m.Version)
	binary.Write(&buf, binary.LittleEndian, m.Services)
	
	addrToBytes, _ := m.AddrTo.ToBytes()
	buf.Write(addrToBytes)

	addrFromBytes, _ := m.AddrFrom.ToBytes()
	buf.Write(addrFromBytes)

	binary.Write(&buf, binary.LittleEndian, m.Nonce)
	WriteVarString(&buf, []byte(m.SubVersion)) // Convert string to []byte for WriteVarString
	binary.Write(&buf, binary.LittleEndian, m.Mode)
	if m.BestShareHash != nil {
		buf.Write(m.BestShareHash.CloneBytes())
	} else {
		buf.Write(make([]byte, 32))
	}
	
	return buf.Bytes(), nil
}

func (m *MsgVersion) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	
	binary.Read(r, binary.LittleEndian, &m.Version)
	binary.Read(r, binary.LittleEndian, &m.Services)
	
	m.AddrTo, _ = ReadP2PoolAddress(r)
	m.AddrFrom, _ = ReadP2PoolAddress(r)

	binary.Read(r, binary.LittleEndian, &m.Nonce)
	subVersionBytes, _ := ReadVarString(r) // Read as []byte
	m.SubVersion = string(subVersionBytes) // Convert []byte to string for assignment
	binary.Read(r, binary.LittleEndian, &m.Mode)
	m.BestShareHash, _ = ReadChainHash(r)
	
	return nil
}

func (m *MsgVersion) Command() string {
	return "version"
}
