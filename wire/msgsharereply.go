package wire

import (
	"bytes"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var _ P2PoolMessage = &MsgShareReply{}

type MsgShareReplyResult uint64

const (
	MsgShareReplyResultGood    = MsgShareReplyResult(0)
	MsgShareReplyResultTooLong = MsgShareReplyResult(1)
	MsgShareReplyResultUnk2    = MsgShareReplyResult(2)
	MsgShareReplyResultUnk3    = MsgShareReplyResult(3)
	MsgShareReplyResultUnk4    = MsgShareReplyResult(4)
	MsgShareReplyResultUnk5    = MsgShareReplyResult(5)
	MsgShareReplyResultUnk6    = MsgShareReplyResult(6)
)

type MsgShareReply struct {
	ID     *chainhash.Hash
	Result MsgShareReplyResult
	Shares []Share
}

func (m *MsgShareReply) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.ID, err = ReadChainHash(r)
	if err != nil {
		return err
	}
	result, err := ReadVarInt(r)
	if err != nil {
		return err
	}
	m.Result = MsgShareReplyResult(result)
	m.Shares, err = ReadShares(r)
	return err
}

func (m *MsgShareReply) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	var err error
	err = WriteChainHash(&buf, m.ID)
	if err != nil {
		return nil, err
	}
	err = WriteVarInt(&buf, uint64(m.Result))
	if err != nil {
		return nil, err
	}
	err = WriteShares(&buf, m.Shares)
	return buf.Bytes(), err
}

func (m *MsgShareReply) Command() string {
	return "sharereply"
}
