package wire

import (
	"fmt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

var nullHash chainhash.Hash

// P2PoolMessage is the interface that all p2pool messages implement.
type P2PoolMessage interface {
	Command() string
	ToBytes() ([]byte, error)
	FromBytes([]byte) error
}

func MakeMessage(command string) (P2PoolMessage, error) {
	var msg P2PoolMessage
	switch command {
	case "version":
		msg = &MsgVersion{}
	case "verack":
		msg = &MsgVerAck{}
	case "ping":
		msg = &MsgPing{}
	case "pong":
		msg = &MsgPong{}
	case "addrs":
		msg = &MsgAddrs{}
	case "get_shares":
		msg = &MsgGetShares{}
	case "shares":
		msg = &MsgShares{}
	case "best_block":
		msg = &MsgBestBlock{}
	default:
		return nil, fmt.Errorf("unknown message type: %s", command)
	}
	return msg, nil
}
