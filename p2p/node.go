package p2p

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
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
	"github.com/multiformats/go-multiaddr"
)

const P2PNetworkTopic = "vtc-p2pool-shares-v1"
const SyncProtocolID = protocol.ID("/p2pool/sync/1.0.0")
const peerKeyFile = "peer.key"

type SyncPayload struct {
	Shares json.RawMessage `json:"shares"`
	Blocks json.RawMessage `json:"blocks"`
}

type Node struct {
	Host            host.Host
	DHT             *dht.IpfsDHT
	PubSub          *pubsub.PubSub
	Topic           *pubsub.Topic
	Sub             *pubsub.Subscription
	ctx             context.Context
	cancel          context.CancelFunc
	IncomingShares  chan []byte
	bootstrapPeers  []string
	syncMutex       sync.Mutex
	GetSyncDataFunc func() ([]byte, []byte)
	OnSyncDataFunc  func(sharesJSON []byte, blocksJSON []byte)
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

func NewNode(listenPort int) (*Node, error) {
	if listenPort <= 0 {
		listenPort = 4001
	}
	ctx, cancel := context.WithCancel(context.Background())

	privKey, err := loadOrCreateIdentity()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load or generate peer identity: %w", err)
	}

	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)
	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.NATPortMap(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	kaddht, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
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
		Host:           h,
		DHT:            kaddht,
		PubSub:         ps,
		Topic:          topic,
		Sub:            sub,
		ctx:            ctx,
		cancel:         cancel,
		IncomingShares: make(chan []byte, 1000),
	}

	h.SetStreamHandler(SyncProtocolID, node.handleSyncStream)

	mdnsService := mdns.NewMdnsService(h, P2PNetworkTopic, &mdnsNotifee{n: node, h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		logging.Warnf("P2P: mDNS discovery could not start: %v", err)
	}

	logging.Infof("P2P Node started! Persistent Peer ID: %s", h.ID())
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
	go n.OnSyncDataFunc(incoming.Shares, incoming.Blocks)

	sharesJSON, blocksJSON := n.GetSyncDataFunc()
	outgoing := SyncPayload{
		Shares: sharesJSON,
		Blocks: blocksJSON,
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
		Shares: sharesJSON,
		Blocks: blocksJSON,
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
			}
		}
	}
}

func (n *Node) Bootstrap(bootstrapPeers []string) error {
	if len(bootstrapPeers) > 0 {
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
		if peerinfo.ID == n.Host.ID() || seenPeers[peerinfo.ID] {
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
