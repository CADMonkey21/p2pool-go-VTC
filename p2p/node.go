package p2p

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// P2PNetworkTopic is the GossipSub channel everyone subscribes to
const P2PNetworkTopic = "vtc-p2pool-shares-v1"

type Node struct {
	Host           host.Host
	DHT            *dht.IpfsDHT
	PubSub         *pubsub.PubSub
	Topic          *pubsub.Topic
	Sub            *pubsub.Subscription
	ctx            context.Context
	cancel         context.CancelFunc
	
	// Channel for your Work manager to read incoming network shares
	IncomingShares chan []byte 
}

// NewNode initializes the Libp2p stack
func NewNode(listenPort int) (*Node, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// 1. Create Libp2p Host (Attempts UPnP NAT traversal automatically)
	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.NATPortMap(), 
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	// 2. Initialize DHT (Kademlia) for Peer Discovery
	kaddht, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create dht: %w", err)
	}

	// 3. Initialize GossipSub for message routing
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create gossipsub: %w", err)
	}

	// 4. Join the Vertcoin pool topic
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

	log.Printf("P2P Node started! Your Peer ID: %s", h.ID())
	log.Printf("P2P Listening on: %s", listenAddr)

	// Start listening for network shares in the background
	go node.listenLoop()

	return node, nil
}

// Bootstrap connects to the DHT and known bootstrap peers
func (n *Node) Bootstrap(bootstrapPeers []string) error {
	if err := n.DHT.Bootstrap(n.ctx); err != nil {
		return err
	}

	var wg sync.WaitGroup
	for _, peerAddr := range bootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(peerAddr)
		if err != nil {
			log.Printf("Invalid bootstrap address %s: %v", peerAddr, err)
			continue
		}
		peerinfo, _ := peer.AddrInfoFromP2pAddr(ma)
		
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			if err := n.Host.Connect(n.ctx, pi); err != nil {
				log.Printf("Failed to connect to bootstrap %s: %v", pi.ID, err)
			} else {
				log.Printf("Connected to bootstrap node: %s", pi.ID)
			}
		}(*peerinfo)
	}
	wg.Wait()
	return nil
}

// BroadcastShare sends a JSON byte array of a share to all nodes
func (n *Node) BroadcastShare(shareData []byte) error {
	return n.Topic.Publish(n.ctx, shareData)
}

// listenLoop catches shares from peers and forwards them to your app logic
func (n *Node) listenLoop() {
	for {
		msg, err := n.Sub.Next(n.ctx)
		if err != nil {
			if n.ctx.Err() != nil {
				return 
			}
			log.Println("Error reading GossipSub message:", err)
			continue
		}

		// Prevent echoing our own shares back
		if msg.ReceivedFrom == n.Host.ID() {
			continue
		}

		n.IncomingShares <- msg.Data
	}
}

// Close gracefully shuts down the libp2p stack
func (n *Node) Close() {
	n.cancel()
	if err := n.Host.Close(); err != nil {
		log.Printf("Error closing host: %v", err)
	}
}
