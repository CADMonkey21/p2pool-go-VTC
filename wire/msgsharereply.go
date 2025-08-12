package wire

import (
	"bytes"
)

var _ P2PoolMessage = &MsgShareReply{}

type MsgShareReply struct {
	Shares []Share
	Result uint32
	ID     uint32
}

func (m *MsgShareReply) Command() string {
	return "sharereply"
}

func (m *MsgShareReply) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	shares, err := ReadShares(r)
	if err != nil {
		return err
	}
	m.Shares = shares
	// Ignoring Result and ID for now
	return nil
}

func (m *MsgShareReply) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteShares(&buf, m.Shares)
	// Ignoring Result and ID
	return buf.Bytes(), err
}
