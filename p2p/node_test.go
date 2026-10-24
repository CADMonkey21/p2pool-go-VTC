package p2p

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

func TestListenMultiaddr(t *testing.T) {
	cases := []struct {
		bind     string
		want     string
		loopback bool
	}{
		{"", "/ip4/0.0.0.0/tcp/9348", false},
		{"0.0.0.0", "/ip4/0.0.0.0/tcp/9348", false},
		{"127.0.0.1", "/ip4/127.0.0.1/tcp/9348", true},
		{" 192.168.1.5 ", "/ip4/192.168.1.5/tcp/9348", false},
		{"::", "/ip6/::/tcp/9348", false},
		{"::1", "/ip6/::1/tcp/9348", true},
		{"[::1]", "/ip6/::1/tcp/9348", true},
		{"2001:db8::1", "/ip6/2001:db8::1/tcp/9348", false},
		{"::ffff:127.0.0.1", "/ip4/127.0.0.1/tcp/9348", true},
	}
	for _, c := range cases {
		got, ip, err := listenMultiaddr(c.bind, 9348)
		if err != nil {
			t.Errorf("listenMultiaddr(%q): unexpected error %v", c.bind, err)
			continue
		}
		if got != c.want || ip.IsLoopback() != c.loopback {
			t.Errorf("listenMultiaddr(%q) = %s (loopback=%v), want %s (loopback=%v)", c.bind, got, ip.IsLoopback(), c.want, c.loopback)
		}
	}
	for _, bad := range []string{"localhost", "1.2.3", "p2poolvtc.com", "127.0.0.1:9348"} {
		if _, _, err := listenMultiaddr(bad, 9348); err == nil {
			t.Errorf("listenMultiaddr(%q): expected error", bad)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newTestNode starts a node in its own directory so each gets a distinct peer.key.
func newTestNode(t *testing.T) *Node {
	t.Helper()
	wd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	n, err := NewNode(freePort(t), "127.0.0.1", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestLoopbackNodeOutboundOnly checks that a node bound to loopback advertises
// no addresses, runs the DHT as a client, and can still dial out, sync, pull
// templates and receive GossipSub shares.
func TestLoopbackNodeOutboundOnly(t *testing.T) {
	full := newTestNode(t)
	light := newTestNode(t)

	for _, n := range []*Node{full, light} {
		if addrs := n.Host.Addrs(); len(addrs) != 0 {
			t.Errorf("loopback node advertises %v, want none", addrs)
		}
		if mode := n.DHT.Mode(); mode != dht.ModeClient {
			t.Errorf("DHT mode = %v, want client", mode)
		}
	}

	synced := make(chan struct{}, 1)
	full.GetSyncDataFunc = func(bool) ([]byte, []byte) { return []byte(`["full-share"]`), []byte(`[]`) }
	full.OnSyncDataFunc = func([]byte, []byte) {}
	full.GetTemplateFunc = func() []byte { return []byte(`{"height":1}`) }
	light.GetSyncDataFunc = func(bool) ([]byte, []byte) { return []byte(`[]`), []byte(`[]`) }
	light.OnSyncDataFunc = func(shares, _ []byte) {
		if bytes.Contains(shares, []byte("full-share")) {
			select {
			case synced <- struct{}{}:
			default:
			}
		}
	}
	gotTemplate := make(chan []byte, 1)
	light.OnTemplateFunc = func(_ string, _ string, b []byte) { gotTemplate <- b }

	var bootstrap string
	for _, a := range full.Host.Network().ListenAddresses() {
		if strings.Contains(a.String(), "/tcp/") {
			bootstrap = fmt.Sprintf("%s/p2p/%s", a, full.Host.ID())
		}
	}
	if err := light.Bootstrap([]string{bootstrap}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-synced:
	case <-time.After(15 * time.Second):
		t.Fatal("light node did not sync sharechain from full node")
	}

	// The light node told the full node no listen addresses, so the full node
	// must not hand it out over peer exchange.
	waitFor(t, "identify", func() bool {
		protos, _ := full.Host.Peerstore().GetProtocols(light.Host.ID())
		return len(protos) > 0
	})
	if a := full.Host.Peerstore().Addrs(light.Host.ID()); len(a) != 0 {
		t.Errorf("full node learned light node addrs %v, want none", a)
	}
	for _, a := range full.peerAddrs() {
		if strings.Contains(a, light.Host.ID().String()) {
			t.Errorf("full node advertises outbound-only peer via PEX: %s", a)
		}
	}

	light.PullTemplateFromPeers()
	select {
	case b := <-gotTemplate:
		if string(b) != `{"height":1}` {
			t.Errorf("template = %s", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("light node did not pull template")
	}

	// Gossip delivery depends on subscription exchange and heartbeats, so keep
	// publishing until the light node sees a share.
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := full.BroadcastShare([]byte("share")); err != nil {
			t.Fatal(err)
		}
		select {
		case msg := <-light.IncomingShares:
			if string(msg) != "share" {
				t.Errorf("gossip message = %q", msg)
			}
			return
		case <-tick.C:
		case <-deadline:
			t.Fatal("light node did not receive gossiped share")
		}
	}
}
