package p2p

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging" // <-- Corrected import path
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

	go pm.peerLoop()
	return pm
}

func (pm *PeerManager) peerLoop() {
	for {
		pm.peersMutex.RLock()
		peerCount := len(pm.peers)
		possiblePeerCount := len(pm.possiblePeers)
		pm.peersMutex.RUnlock()

		logging.Debugf("Number of active peers: %d", peerCount)

		if peerCount < 8 && possiblePeerCount > 0 {
			var peerToTry string
			pm.peersMutex.Lock()
			for p := range pm.possiblePeers {
				peerToTry = p
				break
			}
			pm.peersMutex.Unlock()

			if peerToTry != "" {
				go pm.TryPeer(peerToTry)
			}
		}
		time.Sleep(5 * time.Second)
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

	logging.Debugf("Trying peer %s", p)

	remotePort := pm.activeNetwork.StandardP2PPort
	var remoteAddr string
	if strings.Contains(p, ":") {
		remoteAddr = fmt.Sprintf("[%s]:%d", p, remotePort)
	} else {
		remoteAddr = fmt.Sprintf("%s:%d", p, remotePort)
	}

	conn, err := net.DialTimeout("tcp", remoteAddr, 10*time.Second)
	if err != nil {
		logging.Warnf("Failed to connect to %s: %v", remoteAddr, err)
		return
	}

	peer, err := NewPeer(conn, pm.activeNetwork)
	if err != nil {
		logging.Warnf("Handshake with %s failed: %v", remoteAddr, err)
		return
	}

	pm.peersMutex.Lock()
	pm.peers[p] = peer
	pm.peersMutex.Unlock()

	go pm.handlePeerMessages(peer)
}

func (pm *PeerManager) handlePeerMessages(p *Peer) {
	for msg := range p.Connection.Incoming {
		switch t := msg.(type) {
		case *wire.MsgAddrs:
			logging.Infof("Received addrs message with %d new peers from %s", len(t.Addresses), p.RemoteIP)
			pm.AddPossiblePeers(t.Addresses)
		default:
			logging.Debugf("Received unhandled message of type %T", t)
		}
	}
}

func (pm *PeerManager) AddPossiblePeers(addrs []wire.Addr) {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()

	for _, addr := range addrs {
		// The Addr type contains a P2PoolAddress, which in turn contains the net.IP
		ip := addr.Address.Address

		if ip.IsLoopback() {
			continue
		}

		peerAddress := ip.String()

		if _, exists := pm.peers[peerAddress]; !exists {
			if _, exists := pm.possiblePeers[peerAddress]; !exists {
				logging.Debugf("Adding possible peer: %s", peerAddress)
				pm.possiblePeers[peerAddress] = true
			}
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
