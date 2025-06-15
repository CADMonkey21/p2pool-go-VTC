package wire

import (
	"bytes"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// This file defines the 'get_shares' message, which is used by a node
// to request a list of share hashes from a peer.

var _ P2PoolMessage = &MsgGetShares{}

type MsgGetShares struct {
	Hashes []*chainhash.Hash
	Stops  *chainhash.Hash
}

func (m *MsgGetShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Hashes, err = ReadChainHashList(r)
	if err != nil {
		return err
	}
	m.Stops, err = ReadChainHash(r)
	if err != nil {
		return err
	}

	return nil
}

func (m *MsgGetShares) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	var err error

	err = WriteChainHashList(&buf, m.Hashes)
	if err != nil {
		return nil, err
	}
	err = WriteChainHash(&buf, m.Stops)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (m *MsgGetShares) Command() string {
	return "get_shares"
}
