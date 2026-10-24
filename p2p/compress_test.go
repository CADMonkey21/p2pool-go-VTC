package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// testShares returns a JSON array of n share-like objects.
func testShares(tag string, n int) []byte {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"tag":%q,"Hash":"%064x","CoinBase":"AQAAAAEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/////w%d"}`, tag, i, i)
	}
	b.WriteString("]")
	return []byte(b.String())
}

// makeOld removes the zstd protocols from n, as on a node that predates them.
func makeOld(n *Node) {
	n.Host.RemoveStreamHandler(SyncZProtocolID)
	n.Host.RemoveStreamHandler(ShareReqZProtocolID)
}

// recordProtocols wraps n's sync and share request handlers to report the
// protocol each inbound stream negotiated.
func recordProtocols(n *Node) chan protocol.ID {
	got := make(chan protocol.ID, 16)
	for pid, h := range map[protocol.ID]network.StreamHandler{
		SyncProtocolID: n.handleSyncStream, SyncZProtocolID: n.handleSyncStream,
		ShareReqProtocolID: n.handleShareReqStream, ShareReqZProtocolID: n.handleShareReqStream,
	} {
		if !hasHandler(n, pid) {
			continue
		}
		h := h
		n.Host.SetStreamHandler(pid, func(s network.Stream) {
			got <- s.Protocol()
			h(s)
		})
	}
	return got
}

func hasHandler(n *Node, pid protocol.ID) bool {
	for _, p := range n.Host.Mux().Protocols() {
		if p == pid {
			return true
		}
	}
	return false
}

func connect(t *testing.T, a, b *Node) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Host.Connect(ctx, peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Network().ListenAddresses()}); err != nil {
		t.Fatal(err)
	}
}

func setSyncData(n *Node, shares []byte) chan []byte {
	got := make(chan []byte, 4)
	n.GetSyncDataFunc = func(bool) ([]byte, []byte) { return shares, []byte(`[]`) }
	n.OnSyncDataFunc = func(s, _ []byte) { got <- s }
	n.GetSharesByHashFunc = func([]string, int, []string) []byte { return shares }
	return got
}

func expectProtocol(t *testing.T, got chan protocol.ID, want protocol.ID) {
	t.Helper()
	select {
	case p := <-got:
		if p != want {
			t.Fatalf("negotiated %s, want %s", p, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no stream for %s", want)
	}
}

func expectShares(t *testing.T, got chan []byte, want []byte) {
	t.Helper()
	select {
	case s := <-got:
		var a, b []json.RawMessage
		if json.Unmarshal(s, &a) != nil || json.Unmarshal(want, &b) != nil || len(a) != len(b) || !bytes.Equal(a[0], b[0]) {
			t.Fatalf("received %d bytes of shares, want the %d sent", len(s), len(want))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no shares received")
	}
}

func TestZstdProtocolNegotiation(t *testing.T) {
	for _, c := range []struct {
		name         string
		oldResponder bool
		want         protocol.ID
		wantReq      protocol.ID
	}{
		{"new to new", false, SyncZProtocolID, ShareReqZProtocolID},
		{"new to old", true, SyncProtocolID, ShareReqProtocolID},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, b := newTestNode(t), newTestNode(t)
			if c.oldResponder {
				makeOld(b)
			}
			protos := recordProtocols(b)
			aShares, bShares := testShares("a", 2000), testShares("b", 2000)
			aGot, bGot := setSyncData(a, aShares), setSyncData(b, bShares)
			connect(t, a, b)

			a.SyncWithPeer(b.Host.ID())
			expectProtocol(t, protos, c.want)
			expectShares(t, bGot, aShares)
			expectShares(t, aGot, bShares)

			n, err := a.fetchShares(b.Host.ID(), ShareReq{Hashes: []string{"x"}})
			if err != nil || n != 2000 {
				t.Fatalf("share request got %d shares, err %v", n, err)
			}
			expectProtocol(t, protos, c.wantReq)
			expectShares(t, aGot, bShares)
		})
	}
}

// TestZstdOldDialer speaks the 1.0.0 protocols as nodes that predate zstd do.
func TestZstdOldDialer(t *testing.T) {
	a, b := newTestNode(t), newTestNode(t)
	protos := recordProtocols(b)
	aShares, bShares := testShares("a", 500), testShares("b", 500)
	setSyncData(a, aShares)
	bGot := setSyncData(b, bShares)
	connect(t, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := a.Host.NewStream(ctx, b.Host.ID(), SyncProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(s).Encode(&SyncPayload{Shares: aShares, Blocks: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	s.CloseWrite()
	var resp SyncPayload
	if err := json.NewDecoder(s).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	s.Close()
	expectProtocol(t, protos, SyncProtocolID)
	expectShares(t, bGot, aShares)
	if !bytes.Equal(resp.Shares, bShares) {
		t.Fatal("old dialer got a different sync response")
	}

	s, err = a.Host.NewStream(ctx, b.Host.ID(), ShareReqProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(s).Encode(ShareReq{Hashes: []string{"x"}})
	s.CloseWrite()
	var shares []json.RawMessage
	if err := json.NewDecoder(s).Decode(&shares); err != nil || len(shares) != 500 {
		t.Fatalf("old share request got %d shares, err %v", len(shares), err)
	}
	s.Close()
	expectProtocol(t, protos, ShareReqProtocolID)
}

func zstdBomb(t *testing.T, opts ...zstd.EOption) []byte {
	t.Helper()
	payload := append([]byte(`{"shares":"`), bytes.Repeat([]byte("a"), maxZstdPayload+1024)...)
	payload = append(payload, `"}`...)
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf, opts...)
	if err != nil {
		t.Fatal(err)
	}
	zw.Write(payload)
	zw.Close()
	return buf.Bytes()
}

func TestZstdDecompressionBombRejected(t *testing.T) {
	var ok map[string]string
	if err := decodeJSON(bytes.NewReader(encodePayload([]byte(`{"shares":"x"}`), true)), true, &ok); err != nil || ok["shares"] != "x" {
		t.Fatalf("small payload: %v %v", ok, err)
	}
	for name, bomb := range map[string][]byte{
		"oversized output": zstdBomb(t, zstd.WithWindowSize(1<<20)),
		"oversized window": zstdBomb(t, zstd.WithWindowSize(2*maxZstdPayload)),
		"declared size":    encodePayload(append(append([]byte(`{"shares":"`), bytes.Repeat([]byte("a"), maxZstdPayload)...), `"}`...), true),
	} {
		if len(bomb) > 1<<20 {
			t.Fatalf("%s: bomb is %d bytes", name, len(bomb))
		}
		var v map[string]string
		if err := decodeJSON(bytes.NewReader(bomb), true, &v); err == nil {
			t.Errorf("%s: decoded %d bytes past the limit", name, len(v["shares"]))
		}
	}

	// A node that receives a bomb on the sync protocol drops it.
	a, b := newTestNode(t), newTestNode(t)
	setSyncData(a, []byte(`[]`))
	bGot := setSyncData(b, []byte(`[]`))
	connect(t, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := a.Host.NewStream(ctx, b.Host.ID(), SyncZProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	s.Write(zstdBomb(t))
	s.CloseWrite()
	var resp SyncPayload
	if err := decodeJSON(s, true, &resp); err == nil {
		t.Fatal("node answered a sync request it could not have decoded")
	}
	s.Close()
	select {
	case <-bGot:
		t.Fatal("node accepted a decompression bomb")
	case <-time.After(500 * time.Millisecond):
	}
}
