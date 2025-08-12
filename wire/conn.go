package wire

import (
	"encoding/gob"
	"io"
	"math/big"
	"net"
	"sync"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
)

type P2PoolConnection struct {
	Connection   net.Conn
	Network      p2pnet.Network
	Disconnected chan bool
	Incoming     chan P2PoolMessage
	Outgoing     chan P2PoolMessage
	closeOnce    sync.Once
	gobEncoder   *gob.Encoder
	gobDecoder   *gob.Decoder
}

func NewP2PoolConnection(conn net.Conn, n p2pnet.Network) *P2PoolConnection {
	p := &P2PoolConnection{
		Connection:   conn,
		Network:      n,
		Disconnected: make(chan bool),
		Incoming:     make(chan P2PoolMessage, 100),
		Outgoing:     make(chan P2PoolMessage, 100),
		gobEncoder:   gob.NewEncoder(conn),
		gobDecoder:   gob.NewDecoder(conn),
	}
	// Register all message types with gob
	gob.Register(&chainhash.Hash{})
	gob.Register(&big.Int{})
	gob.Register(&MsgVersion{})
	gob.Register(&MsgVerAck{})
	gob.Register(&MsgPing{})
	gob.Register(&MsgPong{})
	gob.Register(&MsgAddrs{})
	gob.Register(&MsgGetShares{})
	gob.Register(&MsgShares{})
	gob.Register(&MsgBestBlock{})

	go p.readLoop()
	go p.writeLoop()
	return p
}

// CORRECTED Close function to ensure all channels are properly shut down.
func (p *P2PoolConnection) Close() {
	p.closeOnce.Do(func() {
		p.Connection.Close()
		close(p.Disconnected)
		// CRITICAL FIX: Closing the Incoming channel is what allows the
		// peer message handler to exit, which in turn triggers the
		// deferred cleanup logic in the PeerManager.
		close(p.Incoming)
	})
}

func (p *P2PoolConnection) readLoop() {
	for {
		var msg P2PoolMessage
		err := p.gobDecoder.Decode(&msg)
		if err != nil {
			if err != io.EOF {
				logging.Errorf("Gob decoding error: %v", err)
			}
			p.Close()
			return
		}
		p.Incoming <- msg
	}
}

func (p *P2PoolConnection) writeLoop() {
	for msg := range p.Outgoing {
		err := p.gobEncoder.Encode(&msg)
		if err != nil {
			logging.Errorf("Failed to write message %s: %v", msg.Command(), err)
		}
	}
}
