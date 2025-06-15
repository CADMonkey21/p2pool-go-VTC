package p2p

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/work"
	"github.com/gertjaap/p2pool-go/wire"
)

type PeerManager struct {
	peers         map[string]*Peer
	possiblePeers map[string]bool
	activeNetwork p2pnet.Network
	shareChain    *work.ShareChain
	peersMutex    sync.RWMutex
}

func NewPeerManager(net p2pnet.Network, sc *work.ShareChain) *PeerManager {
	pm := &PeerManager{
		peers:         make(map[string]*Peer),
		possiblePeers: make(map[string]bool),
		activeNetwork: net,
		shareChain:    sc,
	}
	for _, h := range net.SeedHosts {
		pm.possiblePeers[h] = true
	}
	go pm.peerConnectorLoop()
	return pm
}

func (pm *PeerManager) Broadcast(msg wire.P2PoolMessage) {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()

	if len(pm.peers) > 0 {
		logging.Infof("P2P: Broadcasting '%s' message to %d peers", msg.Command(), len(pm.peers))
		for _, p := range pm.peers {
			if p.IsConnected() {
				p.Connection.Outgoing <- msg
			}
		}
	}
}

func (pm *PeerManager) peerConnectorLoop() {
	ticker := time.NewTicker(10 * time.Second)
	for {
		<-ticker.C
		pm.peersMutex.RLock()
		peerCount := len(pm.peers)
		possiblePeerCount := len(pm.possiblePeers)
		pm.peersMutex.RUnlock()

		logging.Debugf("Number of active peers: %d", peerCount)

		if peerCount < 8 && possiblePeerCount > 0 {
			var peerToTry string
			pm.peersMutex.Lock()
			for p := range pm.possiblePeers {
				if p == "" {
					delete(pm.possiblePeers, p)
					continue
				}
				peerToTry = p
				break
			}
			pm.peersMutex.Unlock()

			if peerToTry != "" {
				go pm.TryPeer(peerToTry)
			}
		}
	}
}

func (pm *PeerManager) TryPeer(p string) {
	pm.peersMutex.Lock()
	delete(pm.possiblePeers, p)
	if _, ok := pm.peers[p]; ok {
		pm.peersMutex.Unlock()
		return
	}
	pm.peersMutex.Unlock()

	logging.Debugf("Trying OUTGOING connection to peer %s", p)

	remotePort := pm.activeNetwork.StandardP2PPort
	remoteAddr := fmt.Sprintf("%s:%d", p, remotePort)
	if strings.Contains(p, ":") {
		remoteAddr = fmt.Sprintf("[%s]:%d", p, remotePort)
	}

	conn, err := net.DialTimeout("tcp", remoteAddr, 10*time.Second)
	if err != nil {
		logging.Warnf("Failed to connect to %s: %v", remoteAddr, err)
		return
	}

	pm.handleNewPeer(conn)
}

func (pm *PeerManager) handleNewPeer(conn net.Conn) {
	peer, err := NewPeer(conn, pm.activeNetwork)
	if err != nil {
		logging.Warnf("P2P: Handshake with %s failed: %v", conn.RemoteAddr(), err)
		return
	}
	
	peerKey := conn.RemoteAddr().String()

	pm.peersMutex.Lock()
	pm.peers[peerKey] = peer
	pm.peersMutex.Unlock()
	
	pm.handlePeerMessages(peer)

	pm.peersMutex.Lock()
	delete(pm.peers, peerKey)
	pm.peersMutex.Unlock()
}

func (pm *PeerManager) handlePeerMessages(p *Peer) {
	for msg := range p.Connection.Incoming {
		switch t := msg.(type) {
		case *wire.MsgPing:
			logging.Debugf("Received ping from %s, sending pong", p.RemoteIP)
			p.Connection.Outgoing <- &wire.MsgPong{}
		case *wire.MsgAddrs:
			logging.Infof("Received addrs message from %s (ignoring for now)", p.RemoteIP)
		default:
			logging.Debugf("Received unhandled message of type %T from %s", t, p.RemoteIP)
		}
	}
}

func (pm *PeerManager) GetPeerCount() int {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()
	return len(pm.peers)
}

func (pm *PeerManager) AskForShare(s *chainhash.Hash) {
	logging.Debugf("TODO: Ask network for share %s", s.String())
}

