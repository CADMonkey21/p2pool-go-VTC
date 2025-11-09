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

	// [FIX] Corrected the typo in this import path
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/config" // [NEW] Import config
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
	"github.com/CADMonkey21/p2pool-go-VTC/wire"
)

const (
	defaultMaxPeers    = 35
	peerBackoffSeconds = 300 // 5 minutes
	peerWarningMinutes = 5   // [NEW] How often to show "no peers" warning
)

type PeerManager struct {
	peers                map[string]*Peer
	possiblePeers        map[string]bool
	connectingPeers      map[string]bool      // NEW: Track peers currently being connected to
	backoffPeers         map[string]time.Time // [NEW] Track peers that failed to connect
	lastNoPeersWarning   time.Time            // [NEW] Timestamp for rate-limiting warnings
	activeNetwork        p2pnet.Network
	shareChain           *work.ShareChain
	peersMutex           sync.RWMutex
	synced               atomic.Value
	syncedChan           chan struct{}
	syncSignalSent       bool
	syncMutex            sync.Mutex
}

func NewPeerManager(net p2pnet.Network, sc *work.ShareChain) *PeerManager {
	pm := &PeerManager{
		peers:           make(map[string]*Peer),
		possiblePeers:   make(map[string]bool),
		connectingPeers: make(map[string]bool),     // NEW: Initialize the map
		backoffPeers:    make(map[string]time.Time), // [NEW] Initialize the map
		activeNetwork:   net,
		shareChain:      sc,
		syncedChan:      make(chan struct{}),
	}
	pm.synced.Store(false)
	for _, h := range net.SeedHosts {
		pm.AddPossiblePeer(h)
	}
	go pm.peerConnectorLoop()
	go pm.shareRequester()
	return pm
}

func (pm *PeerManager) ForceSyncState(state bool) {
	pm.syncMutex.Lock()
	defer pm.syncMutex.Unlock()

	if state && !pm.IsSynced() {
		if !pm.syncSignalSent {
			logging.Infof("P2P: Node is now synced with the network.")
			pm.synced.Store(true)
			close(pm.syncedChan)
			pm.syncSignalSent = true
		}
	} else if !state {
		pm.synced.Store(state)
		if pm.syncSignalSent {
			pm.syncedChan = make(chan struct{})
			pm.syncSignalSent = false
		}
	}
}

func (pm *PeerManager) SyncedChannel() <-chan struct{} {
	return pm.syncedChan
}

func (pm *PeerManager) IsSynced() bool {
	val := pm.synced.Load()
	if val == nil {
		return false
	}
	return val.(bool)
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

// isAlreadyConnected now checks active and connecting peers.
func (pm *PeerManager) isAlreadyConnected(ip string) bool {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()
	for _, p := range pm.peers {
		if p.RemoteIP.String() == ip {
			return true
		}
	}
	if _, connecting := pm.connectingPeers[ip]; connecting {
		return true
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
		//
		// [FIX] Added check for config.Active.SoloMode
		// We only check for lost sync if we are NOT in solo mode.
		//
		if peerCount == 0 && pm.IsSynced() && !config.Active.SoloMode {
			logging.Warnf("P2P: Node has lost sync with the network (no peers).")
			pm.ForceSyncState(false)
		}
		pm.peersMutex.RUnlock()

		logging.Debugf("Number of active peers: %d", peerCount)

		//
		// [FIX] Also, don't try to connect to peers if we are in solo mode.
		//
		if peerCount < 8 && !config.Active.SoloMode {
			pm.peersMutex.Lock()
			var peerToTry string
			possiblePeerCount := len(pm.possiblePeers)
			backoffPeerCount := len(pm.backoffPeers)

			for p := range pm.possiblePeers {
				host, _, _ := net.SplitHostPort(p)
				// Don't try to connect if we are already connected or connecting
				if _, active := pm.peers[p]; active || pm.connectingPeers[host] {
					continue
				}

				// [NEW] Check backoff list
				if backoffTime, inBackoff := pm.backoffPeers[host]; inBackoff {
					backoffDuration := time.Duration(peerBackoffSeconds) * time.Second
					if time.Since(backoffTime) < backoffDuration {
						logging.Debugf("P2P: Skipping peer %s, in backoff for %v", host, (backoffDuration)-time.Since(backoffTime))
						continue // Still in backoff
					}
					// Backoff expired, remove from list and try again
					logging.Debugf("P2P: Backoff expired for peer %s, retrying...", host)
					delete(pm.backoffPeers, host)
				}

				peerToTry = p
				break
			}
			pm.peersMutex.Unlock()

			if peerToTry != "" {
				go pm.TryPeer(peerToTry)
			} else {
				// [NEW] No peer found to try. Log a warning to the user.
				pm.peersMutex.Lock()
				if time.Since(pm.lastNoPeersWarning) > (peerWarningMinutes * time.Minute) {
					if possiblePeerCount == 0 {
						logging.Warnf("P2P: No peers found. Mining is paused.")
						logging.Warnf("P2P: Please add peers to your 'config.yaml' or run with -solo flag for testing.")
					} else if backoffPeerCount >= possiblePeerCount {
						logging.Warnf("P2P: All known peers are offline. Mining is paused.")
						logging.Warnf("P2P: Waiting for peers to come back online...")
					}
					pm.lastNoPeersWarning = time.Now()
				}
				pm.peersMutex.Unlock()
			}
		}
	}
}

func (pm *PeerManager) TryPeer(p string) {
	host, portStr, err := net.SplitHostPort(p)
	if err != nil {
		host = p
		portStr = fmt.Sprintf("%d", pm.activeNetwork.StandardP2PPort)
	}

	if pm.isAlreadyConnected(host) {
		logging.Debugf("P2P: Aborting connection to %s, peer already connected or connecting.", host)
		return
	}

	logging.Debugf("Trying OUTGOING connection to peer %s", p)

	// Mark this peer as "connecting"
	pm.peersMutex.Lock()
	pm.connectingPeers[host] = true
	pm.peersMutex.Unlock()

	// Ensure we remove from connectingPeers on any exit path
	defer func() {
		pm.peersMutex.Lock()
		delete(pm.connectingPeers, host)
		pm.peersMutex.Unlock()
	}()

	port, err := strconv.Atoi(portStr)
	if err != nil {
		logging.Warnf("Invalid port for peer %s: %v", p, err)
		return
	}

	remoteAddr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", remoteAddr, 10*time.Second)
	if err != nil {
		logging.Warnf("P2P: Failed to connect to %s: %v", remoteAddr, err)
		// [NEW] Add to backoff list
		pm.peersMutex.Lock()
		pm.backoffPeers[host] = time.Now()
		pm.peersMutex.Unlock()
		logging.Warnf("P2P: Adding peer %s to backoff list for %d seconds.", host, peerBackoffSeconds) // User-requested log
		return
	}

	pm.handleNewPeer(conn)
}

func (pm *PeerManager) handleNewPeer(conn net.Conn) {
	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

	peer, err := NewPeer(conn, pm.activeNetwork, pm.shareChain, pm)
	if err != nil {
		logging.Warnf("P2P: Handshake with %s failed: %v", conn.RemoteAddr(), err)
		return
	}

	originalAddr := conn.RemoteAddr().String()

	pm.peersMutex.Lock()
	pm.peers[originalAddr] = peer
	delete(pm.connectingPeers, remoteIP) // Successfully connected, remove from connecting list
	delete(pm.backoffPeers, remoteIP)    // [NEW] Remove from backoff list on successful connect
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

			if len(pm.shareChain.GetNeededHashes()) == 0 {
				pm.ForceSyncState(true)
			}

		case *wire.MsgGetShares:
			logging.Debugf("Received get_shares request from %s for %d hashes", p.RemoteIP, len(t.Hashes))
			var responseShares []wire.Share
			for _, h := range t.Hashes {
				share := pm.shareChain.GetShare(h.String())
				if share != nil {
					responseShares = append(responseShares, *share)
					cs := pm.shareChain.AllShares[h.String()]

					limit := int(t.Parents)
					if limit > 5000 {
						limit = 5000
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
