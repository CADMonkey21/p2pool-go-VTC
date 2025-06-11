package p2p

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash" // <-- This line is now corrected
	"github.com/gertjaap/p2pool-go/logging"
	p2poolnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/wire"
)

type Peer struct {
	Connection  *wire.P2PoolConnection
	RemoteIP    net.IP
	RemotePort  int
	Network     p2poolnet.Network
	versionInfo *wire.MsgVersion
	connected   bool
	connMutex   sync.RWMutex
}

func NewPeer(conn net.Conn, n p2poolnet.Network) (*Peer, error) {
	p := Peer{
		Network:    n,
		RemoteIP:   conn.RemoteAddr().(*net.TCPAddr).IP,
		RemotePort: conn.RemoteAddr().(*net.TCPAddr).Port,
		Connection: wire.NewP2PoolConnection(conn, n),
		connected:  false,
	}

	err := p.Handshake()
	if err != nil {
		p.Connection.Close()
		return nil, err
	}

	p.connMutex.Lock()
	p.connected = true
	p.connMutex.Unlock()

	go p.monitorDisconnect()
	go p.PingLoop()

	return &p, nil
}

func (p *Peer) monitorDisconnect() {
	<-p.Connection.Disconnected
	p.connMutex.Lock()
	p.connected = false
	p.connMutex.Unlock()
	logging.Warnf("Peer %s has disconnected.", p.RemoteIP)
}

func (p *Peer) IsConnected() bool {
	p.connMutex.RLock()
	defer p.connMutex.RUnlock()
	return p.connected
}

func (p *Peer) Close() {
	p.connMutex.Lock()
	p.connected = false
	p.connMutex.Unlock()
	p.Connection.Close()
}

func (p *Peer) BestShare() *chainhash.Hash {
	if p.versionInfo == nil {
		return nil
	}
	return p.versionInfo.BestShareHash
}

func (p *Peer) PingLoop() {
	for {
		if !p.IsConnected() {
			return
		}
		time.Sleep(time.Second * 15)
		if !p.IsConnected() {
			return
		}
		p.Connection.Outgoing <- &wire.MsgPing{}
	}
}

func (p *Peer) Handshake() error {
	localAddr := p.Connection.GetLocalAddr().(*net.TCPAddr)

	versionMsg := &wire.MsgVersion{
		Version:  p.Network.ProtocolVersion,
		Services: 1,
		AddrTo: wire.P2PoolAddress{
			Services: 1,
			Address:  p.RemoteIP,
			Port:     int16(p.Network.StandardP2PPort),
		},
		AddrFrom: wire.P2PoolAddress{
			Services: 1,
			Address:  localAddr.IP,
			Port:     int16(p.Network.P2PPort),
		},
		Nonce:      int64(rand.Uint64()),
		SubVersion: "p2pool-go/0.1.0",
		Mode:       0,
		BestShareHash: &chainhash.Hash{},
	}

	logging.Debugf("Sending version message: %+v", versionMsg)
	p.Connection.Outgoing <- versionMsg

	select {
	case msg := <-p.Connection.Incoming:
		var ok bool
		p.versionInfo, ok = msg.(*wire.MsgVersion)
		if !ok {
			return fmt.Errorf("first message received from peer was not version message, but %T", msg)
		}
		logging.Infof("Handshake successful with %s! Peer is on protocol version %d", p.RemoteIP, p.versionInfo.Version)
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timeout waiting for version message from peer")
	case <-p.Connection.Disconnected:
		return fmt.Errorf("peer disconnected during handshake")
	}
	return nil
}
