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
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
	"github.com/CADMonkey21/p2pool-go-VTC/wire"
)

type PeerManager struct {
	peers           map[string]*Peer
	possiblePeers   map[string]bool
	pendingPeers    map[string]bool // Add a map to track pending outbound connections
	activeNetwork   p2pnet.Network
	shareChain      *work.ShareChain
	peersMutex      sync.RWMutex
}

func NewPeerManager(net p2pnet.Network, sc *work.ShareChain) *PeerManager {
	pm := &PeerManager{
		peers:           make(map[string]*Peer),
		possiblePeers:   make(map[string]bool),
		pendingPeers:    make(map[string]bool), // Initialize the new map
		activeNetwork:   net,
		shareChain:      sc,
	}
	sc.SetPeerManager(pm)
	for _, h := range net.SeedHosts {
		pm.AddPossiblePeer(h)
	}
	go pm.peerConnectorLoop()
	go pm.shareRequester()
	return pm
}

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
		go pm.handleNewPeer(conn, conn.RemoteAddr().String())
	}
}

func (pm *PeerManager) shareRequester() {
	for neededHash := range pm.shareChain.NeedShareChannel {
		var idBytes [4]byte
		_, err := rand.Read(idBytes[:])
		if err != nil {
			logging.Warnf("Could not generate random ID for get_shares: %v", err)
			continue
		}
		randomID := binary.LittleEndian.Uint32(idBytes[:])

		logging.Debugf("Broadcasting request for needed share %s with ID %d", neededHash.String()[:12], randomID)
		msg := &wire.MsgGetShares{
			Hashes:  []*chainhash.Hash{neededHash},
			Parents: 10,
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
	if _, exists := pm.peers[addr]; !exists {
		pm.possiblePeers[addr] = true
	}
}

func (pm *PeerManager) Broadcast(msg wire.P2PoolMessage) {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()

	if len(pm.peers) > 0 {
		// logging.Debugf("P2P: Broadcasting '%s' message to %d peers", msg.Command(), len(pm.peers))
		for _, p := range pm.peers {
			if p.IsConnected() {
				p.Connection.Outgoing <- msg
			}
		}
	}
}

func (pm *PeerManager) relayToOthers(msg wire.P2PoolMessage, sender *Peer) {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()
	for _, peer := range pm.peers {
		if peer != sender && peer.IsConnected() {
			peer.Connection.Outgoing <- msg
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
				_, isActive := pm.peers[p]
				_, isPending := pm.pendingPeers[p] // Check if we are already trying to connect
				if !isActive && !isPending {
					peerToTry = p
					break
				}
			}
			
			// Mark the peer as pending BEFORE starting the connection attempt.
			if peerToTry != "" {
				pm.pendingPeers[peerToTry] = true
			}
			pm.peersMutex.Unlock()

			if peerToTry != "" {
				go pm.TryPeer(peerToTry)
			}
		}
	}
}

func (pm *PeerManager) TryPeer(p string) {
	// Ensure the pending status is removed when this function exits.
	defer func() {
		pm.peersMutex.Lock()
		delete(pm.pendingPeers, p)
		pm.peersMutex.Unlock()
	}()

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

	go pm.handleNewPeer(conn, p)
}

func (pm *PeerManager) handleNewPeer(conn net.Conn, originalAddr string) {
	peer, err := NewPeer(conn, pm.activeNetwork, pm.shareChain)
	if err != nil {
		logging.Warnf("P2P: Handshake with %s failed: %v", conn.RemoteAddr(), err)
		return
	}
	
	peerKey := conn.RemoteAddr().String()

	pm.peersMutex.Lock()
	pm.peers[peerKey] = peer
	pm.peersMutex.Unlock()

	defer func() {
		pm.peersMutex.Lock()
		delete(pm.peers, peerKey)
		pm.peersMutex.Unlock()
		pm.AddPossiblePeer(originalAddr)
		logging.Warnf("Peer %s has disconnected.", peerKey)
	}()

	pm.handlePeerMessages(peer)
}

func (pm *PeerManager) handlePeerMessages(p *Peer) {
	// Trigger for initial sync
	logging.Infof("Requesting share chain tip from new peer %s", p.RemoteIP.String())
	initialGetShares := &wire.MsgGetShares{
		Hashes:  []*chainhash.Hash{},
		Parents: 0,
		ID:      0,
		Stops:   &chainhash.Hash{},
	}
	p.Connection.Outgoing <- initialGetShares

	for msg := range p.Connection.Incoming {
		switch t := msg.(type) {
		case *wire.MsgPing:
			p.Connection.Outgoing <- &wire.MsgPong{}

		case *wire.MsgVerAck:
			// No action needed.

		case *wire.MsgAddrs:
			logging.Infof("Received addrs message from %s. Discovering %d new potential peers.", p.RemoteIP.String(), len(t.Addresses))
			for _, addrRecord := range t.Addresses {
				ip := addrRecord.Address.String()
				port := addrRecord.Port
				peerAddr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
				pm.AddPossiblePeer(peerAddr)
			}
		case *wire.MsgShares:
			logging.Infof("Received %d new shares from %s to process.", len(t.Shares), p.RemoteIP.String())
			pm.shareChain.AddShares(t.Shares, false)
			// relayToOthers is removed to prevent broadcast loops.

		case *wire.MsgGetShares:
			logging.Debugf("Received get_shares request from %s for %d hashes", p.RemoteIP.String(), len(t.Hashes))
			var responseShares []wire.Share

			if len(t.Hashes) == 0 {
				tip := pm.shareChain.Tip
				if tip != nil {
					cs := tip
					for i := 0; i < 500 && cs != nil; i++ {
						responseShares = append(responseShares, *cs.Share)
						cs = cs.Previous
					}
				}
			} else {
				for _, h := range t.Hashes {
					share := pm.shareChain.GetShare(h.String())
					if share != nil {
						responseShares = append(responseShares, *share)
						cs := pm.shareChain.AllShares[h.String()]
						for i := 0; i < 50 && cs != nil && cs.Previous != nil; i++ {
							cs = cs.Previous
							responseShares = append(responseShares, *cs.Share)
						}
					}
				}
			}

			if len(responseShares) > 0 {
				logging.Debugf("Found %d requested shares, sending to peer %s", len(responseShares), p.RemoteIP.String())
				p.Connection.Outgoing <- &wire.MsgShares{Shares: responseShares}
			}
		default:
			logging.Debugf("Received unhandled message of type %T from %s", t, p.RemoteIP.String())
		}
	}
}

func (pm *PeerManager) GetPeerCount() int {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()
	return len(pm.peers)
}
