package wire

import (
	"bytes"
	"encoding/binary"
	// "io" // This line is now removed

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// MsgVersion implements the Message interface and represents a p2pool version message.
type MsgVersion struct {
	Version       int32
	Services      uint64
	AddrTo        P2PoolAddress
	AddrFrom      P2PoolAddress
	Nonce         int64
	SubVersion    string
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
	WriteVarString(&buf, m.SubVersion)
	binary.Write(&buf, binary.LittleEndian, m.Mode)
	buf.Write(m.BestShareHash.CloneBytes())
	
	return buf.Bytes(), nil
}

func (m *MsgVersion) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	
	binary.Read(r, binary.LittleEndian, &m.Version)
	binary.Read(r, binary.LittleEndian, &m.Services)
	
	m.AddrTo, _ = ReadP2PoolAddress(r)
	m.AddrFrom, _ = ReadP2PoolAddress(r)

	binary.Read(r, binary.LittleEndian, &m.Nonce)
	m.SubVersion, _ = ReadVarString(r)
	binary.Read(r, binary.LittleEndian, &m.Mode)
	m.BestShareHash, _ = ReadChainHash(r)
	
	return nil
}

func (m *MsgVersion) Command() string {
	return "version"
}
