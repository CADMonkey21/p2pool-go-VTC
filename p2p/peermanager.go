package p2p

import (
	"fmt"
	"net"
	"strconv" // Added for strconv.Atoi
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/work" // Added for work.ShareChain
	// "github.com/gertjaap/p2pool-go/util" // Removed unused import
	"github.com/gertjaap/p2pool-go/wire"
)

type PeerManager struct {
	peers         map[string]*Peer
	possiblePeers map[string]bool // Stores "IP:Port" strings
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
		pm.AddPossiblePeer(h) // Use new helper to add initial seeds
	}
	go pm.peerConnectorLoop()
	return pm
}

// AddPossiblePeer adds a new peer address to the list of possible peers.
func (pm *PeerManager) AddPossiblePeer(addr string) {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()
	if addr == "" {
		return
	}
	// Avoid adding duplicate or self addresses (if public IP is known)
	if _, exists := pm.peers[addr]; !exists && !pm.possiblePeers[addr] {
		// Basic check to avoid adding own public IP:Port if known, needs refinement.
		// For now, relies on config peers not being own address
		pm.possiblePeers[addr] = true
	}
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
		pm.peersMutex.RUnlock()

		logging.Debugf("Number of active peers: %d", peerCount)

		if peerCount < 8 { // Target 8 active peers
			pm.peersMutex.Lock()
			var peerToTry string
			// Find a peer to try from possiblePeers
			for p := range pm.possiblePeers {
				// Ensure it's not already an active peer
				if _, active := pm.peers[p]; !active {
					peerToTry = p
					delete(pm.possiblePeers, p) // Remove it once we try it
					break
				}
				delete(pm.possiblePeers, p) // Remove if already active, or empty
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
		// If no port, use standard P2P port
		host = p
		portStr = fmt.Sprintf("%d", pm.activeNetwork.StandardP2PPort)
	}
	port, err := strconv.Atoi(portStr) // Corrected: use strconv.Atoi
	if err != nil {
		logging.Warnf("Invalid port for peer %s: %v", p, err)
		return
	}

	remoteAddr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	// Check if already an active peer or in possiblePeers (should have been removed by now, but defensive)
	pm.peersMutex.RLock()
	_, active := pm.peers[remoteAddr]
	pm.peersMutex.RUnlock()
	if active {
		return // Already connected
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
	
	pm.handlePeerMessages(peer) // Start message handling for this peer

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
			// Process incoming addrs message to discover new peers
			logging.Infof("Received addrs message from %s. Discovering %d new potential peers.", p.RemoteIP, len(t.Addresses))
			for _, addrRecord := range t.Addresses {
				// addrRecord is of type wire.Addr. Its Address field is wire.P2PoolAddress
				ip := addrRecord.Address.Address.String() // Corrected: Access net.IP via .Address.Address
				port := addrRecord.Address.Port           // Corrected: Access Port via .Address.Port
				// Convert to "IP:Port" string
				peerAddr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
				pm.AddPossiblePeer(peerAddr) // Add to possible peers
			}
		case *wire.MsgAddrMe:
			// Process incoming addrme message to learn about sender's public IP
			logging.Infof("Received addrme message from %s, port %d. Considering for peer discovery.", p.RemoteIP, t.Port)
			// The peer is telling us its own perceived public IP. We add it to possible peers.
			peerAddr := net.JoinHostPort(p.RemoteIP.String(), fmt.Sprintf("%d", t.Port))
			pm.AddPossiblePeer(peerAddr)
		case *wire.MsgShares:
			logging.Infof("Received shares message from %s with %d shares.", p.RemoteIP, len(t.Shares))
			// Here, you would implement the logic to add these shares to your shareChain.
			// This part is the core of the sharechain build from network, which we discussed is complex.
			// Example placeholder (you need to implement proper sharechain resolution here):
			if len(t.Shares) > 0 {
				pm.shareChain.AddShares(t.Shares) // Add shares to disconnected list for resolution
				// After adding shares, your shareChain.Resolve() method should try to connect them.
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

func (pm *PeerManager) AskForShare(s *chainhash.Hash) {
	logging.Debugf("TODO: Ask network for share %s", s.String())
}
