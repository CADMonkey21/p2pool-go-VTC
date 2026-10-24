package p2p

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/libp2p/go-libp2p/core/peer"
)

// The share download loop follows p2pool's download_shares: while the sharechain is
// missing parents, ask a random peer for one of them, with a random number of its
// ancestors so a gap larger than one reply is eventually crossed.
const (
	maxDownloadParents = 500
	maxStopHashes      = 100
	// downloadRetryDelay is the wait after an empty reply or failed request, and
	// between checks for missing parents while there are none.
	downloadRetryDelay = 1 * time.Second
	// A hash still missing this long after a reply that held shares is requested again.
	downloadRequeryDelay = 45 * time.Second
)

type shareDownloader struct {
	missing func() []string
	stops   func() []string
	peers   func() []peer.ID
	fetch   func(pid peer.ID, req ShareReq) (int, error)
	intn    func(n int) int
	now     func() time.Time
	// answered holds hashes recently answered with shares. One that stays missing
	// was rejected, so it waits downloadRequeryDelay instead of being refetched.
	answered map[string]time.Time
}

// step makes at most one share request and returns how long to wait before the next.
func (d *shareDownloader) step() time.Duration {
	now := d.now()
	for h, t := range d.answered {
		if now.Sub(t) >= downloadRequeryDelay {
			delete(d.answered, h)
		}
	}
	var want []string
	for _, h := range d.missing() {
		if _, ok := d.answered[h]; !ok {
			want = append(want, h)
		}
	}
	if len(want) == 0 {
		return downloadRetryDelay
	}
	peers := d.peers()
	if len(peers) == 0 {
		return downloadRetryDelay
	}
	h := want[d.intn(len(want))]
	pid := peers[d.intn(len(peers))]
	stops := d.stops()
	if len(stops) > maxStopHashes {
		stops = stops[:maxStopHashes]
	}
	req := ShareReq{Hashes: []string{h}, Parents: d.intn(maxDownloadParents), Stops: stops}
	got, err := d.fetch(pid, req)
	if err != nil {
		logging.Debugf("P2P: Share request for %s to peer %s failed: %v", h[:12], pid, err)
		return downloadRetryDelay
	}
	if got == 0 {
		return downloadRetryDelay
	}
	d.answered[h] = now
	return 0
}

// DownloadShares requests missing parent shares from peers until the node closes.
func (n *Node) DownloadShares() {
	if n.GetMissingSharesFunc == nil || n.GetStopHashesFunc == nil || n.OnSyncDataFunc == nil {
		return
	}
	d := &shareDownloader{
		missing:  n.GetMissingSharesFunc,
		stops:    n.GetStopHashesFunc,
		peers:    n.shareReqPeers,
		fetch:    n.fetchShares,
		intn:     rand.Intn,
		now:      time.Now,
		answered: make(map[string]time.Time),
	}
	for {
		wait := d.step()
		if wait == 0 {
			if n.ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-n.ctx.Done():
			return
		case <-n.downloadWake:
		case <-time.After(wait):
		}
	}
}

// wakeDownload makes the download loop check for missing parents now.
func (n *Node) wakeDownload() {
	select {
	case n.downloadWake <- struct{}{}:
	default:
	}
}

// shareReqPeers returns the connected peers that serve share requests.
func (n *Node) shareReqPeers() []peer.ID {
	var out []peer.ID
	for _, pid := range n.Host.Network().Peers() {
		if ok, _ := n.Host.Peerstore().SupportsProtocols(pid, ShareReqZProtocolID, ShareReqProtocolID); len(ok) > 0 {
			out = append(out, pid)
		}
	}
	return out
}

// fetchShares sends req to pid, hands any shares in the reply to OnSyncDataFunc and
// returns how many the reply held.
func (n *Node) fetchShares(pid peer.ID, req ShareReq) (int, error) {
	ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
	defer cancel()
	s, err := n.Host.NewStream(ctx, pid, ShareReqZProtocolID, ShareReqProtocolID)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(30 * time.Second))
	if err := writeJSON(s, req); err != nil {
		return 0, err
	}
	_ = s.CloseWrite()
	var shares []json.RawMessage
	if err := readJSON(s, &shares); err != nil {
		return 0, err
	}
	if len(shares) > 0 {
		raw, _ := json.Marshal(shares)
		n.OnSyncDataFunc(raw, nil)
		n.wakeDownload()
	}
	return len(shares), nil
}
