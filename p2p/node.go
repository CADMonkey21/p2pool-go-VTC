package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
)

const P2PNetworkTopic = "vtc-p2pool-shares-v1"
const SyncProtocolID = protocol.ID("/p2pool/sync/1.0.0")
const ShareReqProtocolID = protocol.ID("/p2pool/sharereq/1.0.0")
const TemplateProtocolID = protocol.ID("/p2pool/template/1.0.0")
const BlockRelayProtocolID = protocol.ID("/p2pool/blockrelay/1.0.0")
const PeerExProtocolID = protocol.ID("/p2pool/peers/1.0.0")
const BlockCheckProtocolID = protocol.ID("/p2pool/blockcheck/1.0.0")
const HeaderProofProtocolID = protocol.ID("/p2pool/headerproof/1.0.0")

type ShareReq struct {
	Hashes  []string `json:"hashes"`
	Parents int      `json:"parents"`
}

const peerKeyFile = "peer.key"

type SyncPayload struct {
	Shares   json.RawMessage `json:"shares"`
	Blocks   json.RawMessage `json:"blocks"`
	FullNode bool            `json:"full_node,omitempty"`
}

type Node struct {
	Host                 host.Host
	DHT                  *dht.IpfsDHT
	PubSub               *pubsub.PubSub
	Topic                *pubsub.Topic
	Sub                  *pubsub.Subscription
	ctx                  context.Context
	cancel               context.CancelFunc
	IncomingShares       chan []byte
	bootstrapPeers       []string
	syncMutex            sync.Mutex
	GetSyncDataFunc      func() ([]byte, []byte)
	OnSyncDataFunc       func(sharesJSON []byte, blocksJSON []byte)
	GetSharesByHashFunc  func(hashes []string, parents int) []byte
	GetTemplateFunc      func() []byte
	OnTemplateFunc       func(peerID string, voterKey string, tmplJSON []byte)
	OnRawBlockFunc       func(peerID string, rawHex string) bool
	CheckBlockFunc       func(blockHash string) int
	GetRawHeaderFunc     func(blockHash string) string
	GetChallengeHashFunc func() string
	peerMu               sync.RWMutex
	peerFullMode         map[string]bool
	peerVerifiedFull     map[string]bool
}

type mdnsNotifee struct {
	n   *Node
	h   host.Host
	ctx context.Context
}

func (m *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == m.h.ID() {
		return
	}
	logging.Infof("P2P mDNS: Discovered local LAN peer %s, connecting...", pi.ID)
	if err := m.h.Connect(m.ctx, pi); err != nil {
		logging.Warnf("P2P mDNS: Failed to connect to local peer %s: %v", pi.ID, err)
	} else {
		logging.Successf("P2P mDNS: Connected to local LAN peer %s!", pi.ID)
		go m.n.SyncWithPeer(pi.ID)
	}
}

func loadOrCreateIdentity() (crypto.PrivKey, error) {
	if data, err := os.ReadFile(peerKeyFile); err == nil {
		if priv, err := crypto.UnmarshalPrivateKey(data); err == nil {
			return priv, nil
		}
	}

	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}

	if data, err := crypto.MarshalPrivateKey(priv); err == nil {
		_ = os.WriteFile(peerKeyFile, data, 0600)
	}
	return priv, nil
}

// listenMultiaddr builds the libp2p TCP listen multiaddr for an IPv4 or IPv6
// bind address (brackets around IPv6 are optional). An empty address means 0.0.0.0.
func listenMultiaddr(bindAddr string, port int) (string, net.IP, error) {
	bindAddr = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(bindAddr), "["), "]")
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}
	ip := net.ParseIP(bindAddr)
	if ip == nil {
		return "", nil, fmt.Errorf("invalid P2P bind address %q: must be an IPv4 or IPv6 address", bindAddr)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("/ip4/%s/tcp/%d", ip4, port), ip4, nil
	}
	return fmt.Sprintf("/ip6/%s/tcp/%d", ip, port), ip, nil
}

// NewNode starts the libp2p host on bindAddr:listenPort. When bindAddr is a
// loopback address the node runs outbound-only: it opens no non-loopback
// sockets (no UPnP, mDNS or UDP transports), advertises no addresses and uses
// the DHT in client mode, so peers never try to dial it.
func NewNode(listenPort int, bindAddr string, disableUPnP bool) (*Node, error) {
	if listenPort <= 0 {
		listenPort = 4001
	}
	listenAddr, bindIP, err := listenMultiaddr(bindAddr, listenPort)
	if err != nil {
		return nil, err
	}
	outboundOnly := bindIP.IsLoopback()

	ctx, cancel := context.WithCancel(context.Background())

	privKey, err := loadOrCreateIdentity()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load or generate peer identity: %w", err)
	}

	opts := []libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
	}
	if outboundOnly {
		opts = append(opts,
			// TCP only: QUIC/WebRTC/WebTransport dials would bind UDP sockets on all interfaces.
			libp2p.Transport(tcp.NewTCPTransport),
			// Loopback addresses are useless to remote peers; advertise nothing.
			libp2p.AddrsFactory(func([]multiaddr.Multiaddr) []multiaddr.Multiaddr { return nil }),
		)
	} else if !disableUPnP {
		opts = append(opts, libp2p.NATPortMap())
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	dhtMode := dht.ModeServer
	if outboundOnly {
		dhtMode = dht.ModeClient
	}
	kaddht, err := dht.New(ctx, h, dht.Mode(dhtMode))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create dht: %w", err)
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create gossipsub: %w", err)
	}

	topic, err := ps.Join(P2PNetworkTopic)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to join topic: %w", err)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	node := &Node{
		Host:             h,
		DHT:              kaddht,
		PubSub:           ps,
		Topic:            topic,
		Sub:              sub,
		ctx:              ctx,
		cancel:           cancel,
		IncomingShares:   make(chan []byte, 1000),
		peerFullMode:     make(map[string]bool),
		peerVerifiedFull: make(map[string]bool),
	}

	h.SetStreamHandler(SyncProtocolID, node.handleSyncStream)
	h.SetStreamHandler(ShareReqProtocolID, node.handleShareReqStream)
	h.SetStreamHandler(TemplateProtocolID, node.handleTemplateStream)
	h.SetStreamHandler(BlockRelayProtocolID, node.handleBlockRelayStream)
	h.SetStreamHandler(PeerExProtocolID, node.handlePeerExStream)
	h.SetStreamHandler(BlockCheckProtocolID, node.handleBlockCheckStream)
	h.SetStreamHandler(HeaderProofProtocolID, node.handleHeaderProofStream)

	// mDNS listens on UDP 5353 on every interface, so it is skipped for loopback binds.
	if !outboundOnly {
		mdnsService := mdns.NewMdnsService(h, P2PNetworkTopic, &mdnsNotifee{n: node, h: h, ctx: ctx})
		if err := mdnsService.Start(); err != nil {
			logging.Warnf("P2P: mDNS discovery could not start: %v", err)
		}
	}

	logging.Infof("P2P Node started! Persistent Peer ID: %s", h.ID())
	if outboundOnly {
		logging.Infof("P2P: Listening on loopback %s only; outbound-only mode (no mDNS, UPnP or advertised addresses, DHT client)", listenAddr)
	}
	for _, addr := range h.Addrs() {
		logging.Infof("P2P Connect Multiaddr: %s/p2p/%s", addr.String(), h.ID())
	}

	go node.listenLoop()
	go node.periodicSyncLoop()

	return node, nil
}

func (n *Node) handleSyncStream(s network.Stream) {
	defer s.Close()
	if n.GetSyncDataFunc == nil || n.OnSyncDataFunc == nil {
		return
	}

	_ = s.SetDeadline(time.Now().Add(60 * time.Second))

	var incoming SyncPayload
	if err := json.NewDecoder(s).Decode(&incoming); err != nil {
		return
	}
	n.setPeerFullMode(s.Conn().RemotePeer().String(), incoming.FullNode)
	go n.OnSyncDataFunc(incoming.Shares, incoming.Blocks)

	sharesJSON, blocksJSON := n.GetSyncDataFunc()
	outgoing := SyncPayload{
		Shares:   sharesJSON,
		Blocks:   blocksJSON,
		FullNode: n.GetTemplateFunc != nil,
	}
	if err := json.NewEncoder(s).Encode(&outgoing); err != nil {
		logging.Debugf("P2P Sync: Failed sending sync response to peer %s: %v", s.Conn().RemotePeer(), err)
		return
	}
	_ = s.CloseWrite()
	time.Sleep(500 * time.Millisecond)
}

func (n *Node) SyncWithPeer(pid peer.ID) {
	if pid == n.Host.ID() || n.GetSyncDataFunc == nil || n.OnSyncDataFunc == nil {
		return
	}
	if !n.syncMutex.TryLock() {
		return
	}
	defer n.syncMutex.Unlock()

	ctx, cancel := context.WithTimeout(n.ctx, 60*time.Second)
	defer cancel()

	s, err := n.Host.NewStream(ctx, pid, SyncProtocolID)
	if err != nil {
		logging.Debugf("P2P Sync: Peer %s stream open err: %v", pid, err)
		return
	}
	defer s.Close()

	_ = s.SetDeadline(time.Now().Add(60 * time.Second))

	sharesJSON, blocksJSON := n.GetSyncDataFunc()
	outgoing := SyncPayload{
		Shares:   sharesJSON,
		Blocks:   blocksJSON,
		FullNode: n.GetTemplateFunc != nil,
	}
	if err := json.NewEncoder(s).Encode(&outgoing); err != nil {
		logging.Debugf("P2P Sync: Failed sending sync request to %s: %v", pid, err)
		return
	}
	_ = s.CloseWrite()

	var incoming SyncPayload
	if err := json.NewDecoder(s).Decode(&incoming); err != nil {
		logging.Debugf("P2P Sync: Failed decoding sync response from %s: %v", pid, err)
		return
	}
	n.setPeerFullMode(pid.String(), incoming.FullNode)

	n.OnSyncDataFunc(incoming.Shares, incoming.Blocks)
}

func (n *Node) periodicSyncLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			peers := n.Host.Network().Peers()
			if len(peers) == 0 && len(n.bootstrapPeers) > 0 {
				go n.Bootstrap(n.bootstrapPeers)
				continue
			}
			for _, pid := range peers {
				go n.SyncWithPeer(pid)
				go n.ExchangePeers(pid)
			}
		}
	}
}

func (n *Node) Bootstrap(bootstrapPeers []string) error {
	if len(bootstrapPeers) > 0 && len(n.bootstrapPeers) == 0 {
		n.bootstrapPeers = bootstrapPeers
	}
	if err := n.DHT.Bootstrap(n.ctx); err != nil {
		return err
	}

	seenPeers := make(map[peer.ID]bool)
	var wg sync.WaitGroup
	for _, peerAddr := range bootstrapPeers {
		if peerAddr == "" {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(peerAddr)
		if err != nil {
			logging.Warnf("P2P: Invalid bootstrap multiaddr '%s': %v", peerAddr, err)
			continue
		}
		peerinfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil || peerinfo == nil {
			logging.Warnf("P2P: Bootstrap address '%s' is missing /p2p/<PeerID>: %v", peerAddr, err)
			continue
		}
		if peerinfo.ID == n.Host.ID() || seenPeers[peerinfo.ID] || n.Host.Network().Connectedness(peerinfo.ID) == network.Connected {
			continue
		}
		seenPeers[peerinfo.ID] = true

		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			if err := n.Host.Connect(n.ctx, pi); err != nil {
				logging.Warnf("P2P: Failed to connect to bootstrap peer %s: %v", pi.ID, err)
			} else {
				logging.Successf("P2P: Connected to bootstrap node: %s", pi.ID)
				n.SyncWithPeer(pi.ID)
			}
		}(*peerinfo)
	}
	wg.Wait()
	return nil
}

func (n *Node) BroadcastShare(shareData []byte) error {
	return n.Topic.Publish(n.ctx, shareData)
}

func (n *Node) listenLoop() {
	for {
		msg, err := n.Sub.Next(n.ctx)
		if err != nil {
			if n.ctx.Err() != nil {
				return
			}
			logging.Warnf("P2P: Error reading GossipSub message: %v", err)
			continue
		}

		if msg.ReceivedFrom == n.Host.ID() {
			continue
		}

		n.IncomingShares <- msg.Data
	}
}

func (n *Node) Close() {
	n.cancel()
	if err := n.Host.Close(); err != nil {
		logging.Warnf("P2P: Error closing host: %v", err)
	}
}

func (n *Node) handleShareReqStream(s network.Stream) {
	defer s.Close()
	if n.GetSharesByHashFunc == nil {
		return
	}
	_ = s.SetDeadline(time.Now().Add(30 * time.Second))
	var req ShareReq
	if err := json.NewDecoder(s).Decode(&req); err != nil || len(req.Hashes) == 0 {
		return
	}
	resp := n.GetSharesByHashFunc(req.Hashes, req.Parents)
	_, _ = s.Write(resp)
	_ = s.CloseWrite()
}

func (n *Node) RequestShares(hashes []string, parents int) {
	if len(hashes) == 0 || n.OnSyncDataFunc == nil {
		return
	}
	for _, pid := range n.Host.Network().Peers() {
		ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
		s, err := n.Host.NewStream(ctx, pid, ShareReqProtocolID)
		if err != nil {
			cancel()
			continue
		}
		_ = json.NewEncoder(s).Encode(ShareReq{Hashes: hashes, Parents: parents})
		_ = s.CloseWrite()
		var raw json.RawMessage
		ok := json.NewDecoder(s).Decode(&raw) == nil && len(raw) > 4
		s.Close()
		cancel()
		if ok {
			n.OnSyncDataFunc(raw, nil)
			return
		}
	}
}
func (n *Node) setPeerFullMode(pid string, isFull bool) {
	n.peerMu.Lock()
	n.peerFullMode[pid] = isFull
	n.peerMu.Unlock()
}

func (n *Node) isPeerAllowedTemplate(pid string) bool {
	n.peerMu.RLock()
	isFull, known := n.peerFullMode[pid]
	n.peerMu.RUnlock()
	return !known || isFull
}

func (n *Node) voterKeyForPeer(pid peer.ID) string {
	conns := n.Host.Network().ConnsToPeer(pid)
	for _, c := range conns {
		parts := strings.Split(c.RemoteMultiaddr().String(), "/")
		if len(parts) >= 3 {
			switch parts[1] {
			case "ip4":
				ip := net.ParseIP(parts[2])
				if ip != nil && !ip.IsLoopback() && !ip.IsPrivate() {
					if ip4 := ip.To4(); ip4 != nil {
						return fmt.Sprintf("ip4:%d.%d.%d.0/24", ip4[0], ip4[1], ip4[2])
					}
				}
			case "ip6":
				ip := net.ParseIP(parts[2])
				if ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
					masked := ip.Mask(net.CIDRMask(48, 128))
					if masked != nil {
						return fmt.Sprintf("ip6:%s/48", masked.String())
					}
				}
			}
		}
	}
	return pid.String()
}

func (n *Node) handleHeaderProofStream(s network.Stream) {
	defer s.Close()
	if n.GetRawHeaderFunc == nil {
		return
	}
	_ = s.SetDeadline(time.Now().Add(8 * time.Second))
	var bh string
	if json.NewDecoder(s).Decode(&bh) == nil && len(bh) == 64 {
		_ = json.NewEncoder(s).Encode(n.GetRawHeaderFunc(bh))
	}
}

func (n *Node) verifyPeerHeaderChallenge(pid peer.ID) bool {
	pStr := pid.String()
	n.peerMu.RLock()
	if n.peerVerifiedFull[pStr] {
		n.peerMu.RUnlock()
		return true
	}
	n.peerMu.RUnlock()

	if n.GetChallengeHashFunc == nil {
		return true
	}
	chal := n.GetChallengeHashFunc()
	if chal == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(n.ctx, 6*time.Second)
	defer cancel()
	s, err := n.Host.NewStream(ctx, pid, HeaderProofProtocolID)
	if err != nil {
		return true
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(6 * time.Second))
	_ = json.NewEncoder(s).Encode(chal)
	_ = s.CloseWrite()
	var hexHdr string
	if json.NewDecoder(s).Decode(&hexHdr) != nil || len(hexHdr) != 160 {
		n.setPeerFullMode(pStr, false)
		return false
	}
	raw, err := hex.DecodeString(hexHdr)
	if err != nil || len(raw) != 80 || chainhash.DoubleHashH(raw).String() != chal {
		n.setPeerFullMode(pStr, false)
		return false
	}
	n.peerMu.Lock()
	n.peerVerifiedFull[pStr] = true
	n.peerMu.Unlock()
	return true
}

func (n *Node) handleTemplateStream(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(15 * time.Second))
	rPeer := s.Conn().RemotePeer()
	pid := rPeer.String()
	var raw json.RawMessage
	if json.NewDecoder(s).Decode(&raw) == nil && len(raw) > 4 && n.OnTemplateFunc != nil {
		if n.isPeerAllowedTemplate(pid) && n.verifyPeerHeaderChallenge(rPeer) {
			n.setPeerFullMode(pid, true)
			n.OnTemplateFunc(pid, n.voterKeyForPeer(rPeer), raw)
		}
	} else if n.GetTemplateFunc != nil {
		if b := n.GetTemplateFunc(); len(b) > 0 {
			_, _ = s.Write(b)
		}
	}
}
func (n *Node) BroadcastTemplate(b []byte) {
	for _, pid := range n.Host.Network().Peers() {
		go func(p peer.ID) {
			ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
			defer cancel()
			if s, err := n.Host.NewStream(ctx, p, TemplateProtocolID); err == nil {
				_, _ = s.Write(b)
				_ = s.CloseWrite()
				s.Close()
			}
		}(pid)
	}
}
func (n *Node) PullTemplateFromPeers() {
	if n.OnTemplateFunc == nil {
		return
	}
	for _, pid := range n.Host.Network().Peers() {
		if !n.isPeerAllowedTemplate(pid.String()) {
			continue
		}
		ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
		s, err := n.Host.NewStream(ctx, pid, TemplateProtocolID)
		if err != nil {
			cancel()
			continue
		}
		_, _ = s.Write([]byte("null\n"))
		_ = s.CloseWrite()
		var raw json.RawMessage
		if json.NewDecoder(s).Decode(&raw) == nil && len(raw) > 4 {
			if n.verifyPeerHeaderChallenge(pid) {
				n.setPeerFullMode(pid.String(), true)
				n.OnTemplateFunc(pid.String(), n.voterKeyForPeer(pid), raw)
			}
		}
		s.Close()
		cancel()
	}
}
func (n *Node) handleBlockCheckStream(s network.Stream) {
	defer s.Close()
	if n.CheckBlockFunc == nil {
		return
	}
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	var bh string
	if json.NewDecoder(s).Decode(&bh) == nil && len(bh) > 0 {
		_ = json.NewEncoder(s).Encode(n.CheckBlockFunc(bh))
	}
}
func (n *Node) QueryBlockConfirmations(blockHash string) int {
	best := 0
	for _, pid := range n.Host.Network().Peers() {
		if !n.isPeerAllowedTemplate(pid.String()) {
			continue
		}
		ctx, cancel := context.WithTimeout(n.ctx, 8*time.Second)
		s, err := n.Host.NewStream(ctx, pid, BlockCheckProtocolID)
		if err != nil {
			cancel()
			continue
		}
		_ = json.NewEncoder(s).Encode(blockHash)
		_ = s.CloseWrite()
		var confs int
		if json.NewDecoder(s).Decode(&confs) == nil {
			if confs > best {
				best = confs
			}
		}
		s.Close()
		cancel()
	}
	return best
}
func (n *Node) handleBlockRelayStream(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(30 * time.Second))
	var hexStr string
	if json.NewDecoder(s).Decode(&hexStr) == nil && len(hexStr) > 0 && n.OnRawBlockFunc != nil {
		if n.OnRawBlockFunc(s.Conn().RemotePeer().String(), hexStr) {
			_, _ = s.Write([]byte("OK"))
		}
	}
}
func (n *Node) RelaySolvedBlockHex(hexStr string) int {
	peers := n.Host.Network().Peers()
	sent := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, pid := range peers {
		wg.Add(1)
		go func(p peer.ID) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
			defer cancel()
			if s, err := n.Host.NewStream(ctx, p, BlockRelayProtocolID); err == nil {
				_ = json.NewEncoder(s).Encode(hexStr)
				_ = s.CloseWrite()
				buf := make([]byte, 8)
				nr, _ := s.Read(buf)
				s.Close()
				if nr >= 2 && string(buf[:2]) == "OK" {
					mu.Lock()
					sent++
					mu.Unlock()
				}
			}
		}(pid)
	}
	wg.Wait()
	return sent
}
func (n *Node) peerAddrs() []string {
	var out []string
	for _, c := range n.Host.Network().Conns() {
		// An inbound peer that advertised no listen addresses (e.g. an outbound-only
		// node bound to loopback) cannot be dialed back, so don't hand it out.
		if c.Stat().Direction == network.DirInbound && len(n.Host.Peerstore().Addrs(c.RemotePeer())) == 0 {
			continue
		}
		a := c.RemoteMultiaddr().String()
		if strings.HasPrefix(a, "/ip4/127.") || strings.HasPrefix(a, "/ip4/192.168.") || strings.HasPrefix(a, "/ip4/10.") {
			continue
		}
		if i := strings.Index(a, "/tcp/"); i > 0 {
			a = a[:i] + "/tcp/9348"
		}
		out = append(out, fmt.Sprintf("%s/p2p/%s", a, c.RemotePeer().String()))
	}
	return out
}
func (n *Node) handlePeerExStream(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	_ = json.NewEncoder(s).Encode(n.peerAddrs())
}
func (n *Node) ExchangePeers(pid peer.ID) {
	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()
	s, err := n.Host.NewStream(ctx, pid, PeerExProtocolID)
	if err != nil {
		return
	}
	defer s.Close()
	var addrs []string
	if json.NewDecoder(s).Decode(&addrs) == nil && len(addrs) > 0 {
		go n.Bootstrap(addrs)
	}
}
