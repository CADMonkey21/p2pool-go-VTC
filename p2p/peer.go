package p2p

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-vtc/net"
	"github.com/CADMonkey21/p2pool-go-vtc/util"
	"github.com/CADMonkey21/p2pool-go-vtc/wire"
)

var publicIP net.IP
var publicIPMutex sync.Once

func getPublicIP() net.IP {
	publicIPMutex.Do(func() {
		ip, err := util.GetPublicIP()
		if err != nil {
			publicIP = nil
		} else {
			publicIP = ip
		}
	})
	return publicIP
}

type Peer struct {
	Connection  *wire.P2PoolConnection
	RemoteIP    net.IP
	RemotePort  int
	Network     p2pnet.Network
	versionInfo *wire.MsgVersion
	connected   bool
	connMutex   sync.RWMutex
}

func NewPeer(conn net.Conn, n p2pnet.Network) (*Peer, error) {
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
	if p.versionInfo == nil || p.versionInfo.BestShareHash == nil {
		return &chainhash.Hash{}
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

// Handshake implements the final, correct, p2pool-compatible handshake.
func (p *Peer) Handshake() error {
	localAddr := p.Connection.Connection.LocalAddr().(*net.TCPAddr)
	
	addrFrom := localAddr.IP
	if !p.RemoteIP.IsLoopback() && getPublicIP() != nil {
		addrFrom = getPublicIP()
	}

	versionMsg := &wire.MsgVersion{
		Version:       p.Network.ProtocolVersion,
		Services:      0,
		AddrTo:        wire.P2PoolAddress{Services: 0, Address: p.RemoteIP, Port: int16(p.RemotePort)},
		AddrFrom:      wire.P2PoolAddress{Services: 0, Address: addrFrom, Port: int16(p.Network.P2PPort)},
		Nonce:         int64(rand.Uint64()),
		SubVersion:    "p2pool-go/0.1.0",
		Mode:          1,
		BestShareHash: &chainhash.Hash{},
	}

	// 1. Send our version message.
	logging.Debugf("Sending version message to %s", p.RemoteIP)
	p.Connection.Outgoing <- versionMsg

	// 2. Wait for their version message.
	logging.Debugf("Waiting for version message from peer %s", p.RemoteIP)
	select {
	case msg := <-p.Connection.Incoming:
		var ok bool
		p.versionInfo, ok = msg.(*wire.MsgVersion)
		if !ok {
			return fmt.Errorf("first message from peer was not version, but %T", msg)
		}
		logging.Debugf("Received version message from %s (v: %d, sub: %s)", p.RemoteIP, p.versionInfo.Version, p.versionInfo.SubVersion)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting for peer's version message")
	case <-p.Connection.Disconnected:
		return fmt.Errorf("peer disconnected during handshake")
	}

	// 3. Send our verack.
	logging.Debugf("Sending verack to %s", p.RemoteIP)
	p.Connection.Outgoing <- &wire.MsgVerAck{}

	// 4. Handshake is now COMPLETE. Do not wait for their verack.
	logging.Infof("Handshake successful with %s! Peer is on protocol version %d", p.RemoteIP, p.versionInfo.Version)
	return nil
}

