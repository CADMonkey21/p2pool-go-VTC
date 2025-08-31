package p2p

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
	"github.com/CADMonkey21/p2pool-go-VTC/wire"
)

const (
	defaultMaxPeers = 35
)

type PeerManager struct {
	peers          map[string]*Peer
	possiblePeers  map[string]bool
	activeNetwork  p2pnet.Network
	shareChain     *work.ShareChain
	peersMutex     sync.RWMutex
	synced         atomic.Value
	syncedChan     chan struct{} // Channel to signal when synced
	syncSignalSent bool          // Flag to ensure the channel is closed only once
}

func NewPeerManager(net p2pnet.Network, sc *work.ShareChain) *PeerManager {
	pm := &PeerManager{
		peers:          make(map[string]*Peer),
		possiblePeers:  make(map[string]bool),
		activeNetwork:  net,
		shareChain:     sc,
		syncedChan:     make(chan struct{}),
		syncSignalSent: false,
	}
	pm.synced.Store(false)
	for _, h := range net.SeedHosts {
		pm.AddPossiblePeer(h)
	}
	go pm.peerConnectorLoop()
	go pm.shareRequester()
	return pm
}

// ForceSyncState allows external packages (like main) to set the sync status.
func (pm *PeerManager) ForceSyncState(state bool) {
	pm.synced.Store(state)
	logging.Debugf("P2P: Sync state forced to %v", state)
}

// SyncedChannel returns a read-only channel that is closed when the initial sync is complete.
func (pm *PeerManager) SyncedChannel() <-chan struct{} {
	return pm.syncedChan
}

func (pm *PeerManager) IsSynced() bool {
	return pm.synced.Load().(bool)
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

		remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		if pm.isAlreadyConnected(remoteIP) {
			logging.Warnf("P2P: Rejecting new connection from %s, peer already connected.", remoteIP)
			conn.Close()
			continue
		}

		logging.Infof("P2P: New INCOMING connection from %s", conn.RemoteAddr().String())
		go pm.handleNewPeer(conn)
	}
}

func (pm *PeerManager) isAlreadyConnected(ip string) bool {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()
	for _, p := range pm.peers {
		if p.RemoteIP.String() == ip {
			return true
		}
	}
	return false
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
			Parents: 1000,
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
		logging.Debugf("P2P: Broadcasting '%s' message to %d peers", msg.Command(), len(pm.peers))
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
		if peerCount == 0 && pm.IsSynced() {
			logging.Warnf("P2P: Node has lost sync with the network (no peers).")
			pm.synced.Store(false)
		}
		pm.peersMutex.RUnlock()

		logging.Debugf("Number of active peers: %d", peerCount)

		if peerCount < 8 {
			pm.peersMutex.Lock()
			var peerToTry string
			for p := range pm.possiblePeers {
				if _, active := pm.peers[p]; !active {
					peerToTry = p
					break
				}
			}
			delete(pm.possiblePeers, peerToTry)
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

	if pm.isAlreadyConnected(host) {
		logging.Debugf("P2P: Aborting connection to %s, peer already connected.", host)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		logging.Warnf("Invalid port for peer %s: %v", p, err)
		pm.AddPossiblePeer(p)
		return
	}

	remoteAddr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", remoteAddr, 10*time.Second)
	if err != nil {
		logging.Warnf("Failed to connect to %s: %v", remoteAddr, err)
		pm.AddPossiblePeer(p)
		return
	}

	pm.handleNewPeer(conn)
}

func (pm *PeerManager) handleNewPeer(conn net.Conn) {
	peer, err := NewPeer(conn, pm.activeNetwork, pm.shareChain, pm)
	if err != nil {
		logging.Warnf("P2P: Handshake with %s failed: %v", conn.RemoteAddr(), err)
		return
	}

	originalAddr := conn.RemoteAddr().String()

	pm.peersMutex.Lock()
	pm.peers[originalAddr] = peer
	pm.peersMutex.Unlock()

	defer func() {
		pm.peersMutex.Lock()
		delete(pm.peers, originalAddr)
		pm.peersMutex.Unlock()
		pm.AddPossiblePeer(originalAddr)
		logging.Warnf("Peer %s has disconnected.", peer.RemoteIP)
	}()

	pm.handlePeerMessages(peer)
}

func (pm *PeerManager) handlePeerMessages(p *Peer) {
	for msg := range p.Connection.Incoming {
		switch t := msg.(type) {
		case *wire.MsgPing:
			p.Connection.Outgoing <- &wire.MsgPong{}

		case *wire.MsgVerAck:
			// No action needed.

		case *wire.MsgAddrs:
			logging.Infof("Received addrs message from %s. Discovering %d new potential peers.", p.RemoteIP, len(t.Addresses))
			for _, addrRecord := range t.Addresses {
				ip := addrRecord.Address.String()
				port := addrRecord.Port
				peerAddr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
				pm.AddPossiblePeer(peerAddr)
			}
		case *wire.MsgShares:
			logging.Infof("Received %d new shares from %s to process.", len(t.Shares), p.RemoteIP)
			pm.shareChain.AddShares(t.Shares)
			pm.relayToOthers(msg, p)

			pm.peersMutex.Lock()
			if len(pm.shareChain.GetNeededHashes()) == 0 && !pm.syncSignalSent {
				logging.Infof("P2P: Node is now synced with the network.")
				pm.synced.Store(true)
				close(pm.syncedChan) // Signal that sync is complete
				pm.syncSignalSent = true
			}
			pm.peersMutex.Unlock()

		case *wire.MsgGetShares:
			logging.Debugf("Received get_shares request from %s for %d hashes", p.RemoteIP, len(t.Hashes))
			var responseShares []wire.Share
			for _, h := range t.Hashes {
				share := pm.shareChain.GetShare(h.String())
				if share != nil {
					responseShares = append(responseShares, *share)
					cs := pm.shareChain.AllShares[h.String()]

					// Respect the number of parents the peer is requesting, up to a sane limit.
					limit := int(t.Parents)
					if limit > 5000 {
						limit = 5000 // Prevent abuse from malicious peers requesting the whole chain.
					}

					for i := 0; i < limit && cs != nil && cs.Previous != nil; i++ {
						cs = cs.Previous
						responseShares = append(responseShares, *cs.Share)
					}
				}
			}

			if len(responseShares) > 0 {
				logging.Debugf("Found %d requested shares, sending to peer %s", len(responseShares), p.RemoteIP)
				p.Connection.Outgoing <- &wire.MsgShares{Shares: responseShares}
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
