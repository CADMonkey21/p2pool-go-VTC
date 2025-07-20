package p2p

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-vtc/net"
	"github.com/CADMonkey21/p2pool-go-vtc/work"
	"github.com/CADMonkey21/p2pool-go-vtc/wire"
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
		pm.AddPossiblePeer(h)
	}
	go pm.peerConnectorLoop()
	go pm.shareRequester()
	return pm
}

// ListenForPeers starts a listener to accept incoming P2P connections.
func (pm *PeerManager) ListenForPeers() {
	addr := fmt.Sprintf(":%d", pm.activeNetwork.P2PPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logging.Fatalf("P2P: Failed to start listener on %s: %v", addr, err)
		return
	}
	defer listener.Close()
	logging.Infof("P2P: Listening for incoming peers on port %s", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			logging.Warnf("P2P: Failed to accept new connection: %v", err)
			continue
		}

		logging.Infof("P2P: New INCOMING connection from %s", conn.RemoteAddr().String())
		go pm.handleNewPeer(conn)
	}
}

// ***** THIS IS THE MODIFIED FUNCTION *****
func (pm *PeerManager) shareRequester() {
	for neededHash := range pm.shareChain.NeedShareChannel {
		// Generate a random ID for this request for legacy peer compatibility
		var idBytes [4]byte
		_, err := rand.Read(idBytes[:])
		if err != nil {
			logging.Warnf("Could not generate random ID for get_shares: %v", err)
			continue // Skip this request if we can't make a proper ID
		}
		randomID := binary.LittleEndian.Uint32(idBytes[:])

		logging.Debugf("Broadcasting request for needed share %s", neededHash.String()[:12])
		msg := &wire.MsgGetShares{
			Hashes:  []*chainhash.Hash{neededHash},
			Parents: 10, // Ask for parents to help resolve the share chain
			ID:      randomID,
			Stops:   &chainhash.Hash{},
		}
		pm.Broadcast(msg)
	}
}

func (pm *PeerManager) AddPossiblePeer(addr string) {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()
	if addr == "" {
		return
	}
	if _, exists := pm.peers[addr]; !exists && !pm.possiblePeers[addr] {
		pm.possiblePeers[addr] = true
	}
}

func (pm *PeerManager) Broadcast(msg wire.P2PoolMessage) {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()

	if len(pm.peers) > 0 {
		logging.Debugf("P2P: Broadcasting '%s' message to %d peers", msg.Command(), len(pm.peers))
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
		pm.peersMutex.RUnlock()

		logging.Debugf("Number of active peers: %d", peerCount)

		if peerCount < 8 {
			pm.peersMutex.Lock()
			var peerToTry string
			for p := range pm.possiblePeers {
				if _, active := pm.peers[p]; !active {
					peerToTry = p
					delete(pm.possiblePeers, p)
					break
				}
				delete(pm.possiblePeers, p)
			}
			pm.peersMutex.Unlock()

			if peerToTry != "" {
				go pm.TryPeer(peerToTry)
			}
		}
	}
}

func (pm *PeerManager) TryPeer(p string) {
	logging.Debugf("Trying OUTGOING connection to peer %s", p)

	host, portStr, err := net.SplitHostPort(p)
	if err != nil {
		host = p
		portStr = fmt.Sprintf("%d", pm.activeNetwork.StandardP2PPort)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		logging.Warnf("Invalid port for peer %s: %v", p, err)
		return
	}

	remoteAddr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	pm.peersMutex.RLock()
	_, active := pm.peers[remoteAddr]
	pm.peersMutex.RUnlock()
	if active {
		return
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
			logging.Infof("Received addrs message from %s. Discovering %d new potential peers.", p.RemoteIP, len(t.Addresses))
			for _, addrRecord := range t.Addresses {
				ip := addrRecord.Address.Address.String()
				port := addrRecord.Address.Port
				peerAddr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
				pm.AddPossiblePeer(peerAddr)
			}
		case *wire.MsgShares:
			logging.Infof("Received shares message from %s with %d shares.", p.RemoteIP, len(t.Shares))
			// Shares from the network are untrusted and MUST be validated.
			pm.shareChain.AddShares(t.Shares, false)
		case *wire.MsgGetShares:
			logging.Debugf("Received get_shares request from %s for %d hashes", p.RemoteIP, len(t.Hashes))
			for _, h := range t.Hashes {
				if share := pm.shareChain.GetShare(h.String()); share != nil {
					logging.Debugf("Found share %s, sending to peer", h.String()[:12])
					p.Connection.Outgoing <- &wire.MsgShares{Shares: []wire.Share{*share}}
				}
			}
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
