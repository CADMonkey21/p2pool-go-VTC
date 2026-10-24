package p2p

import (
	"sync"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// A peer is disconnected once it has sent at least peerScoreMinBad provably invalid
// shares and they are more than peerScoreBadRatio of all the shares it has sent.
const (
	peerScoreMinBad   = 5
	peerScoreBadRatio = 0.2
)

// IncomingShare is a gossiped share and the peer that relayed it.
type IncomingShare struct {
	Data []byte
	From peer.ID
}

type peerScore struct {
	good, bad    int
	disconnected bool
}

type peerScores struct {
	mu sync.Mutex
	m  map[peer.ID]*peerScore
}

// record counts a share from id and returns true the first time id crosses the
// misbehavior threshold.
func (s *peerScores) record(id peer.ID, bad bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := s.m[id]
	if ps == nil {
		ps = &peerScore{}
		s.m[id] = ps
	}
	if bad {
		ps.bad++
	} else {
		ps.good++
	}
	if ps.disconnected || ps.bad < peerScoreMinBad || float64(ps.bad) <= peerScoreBadRatio*float64(ps.good+ps.bad) {
		return false
	}
	ps.disconnected = true
	return true
}

func (s *peerScores) forget(id peer.ID) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

// RecordShareOutcome counts a share relayed by id as accepted or provably invalid,
// and disconnects id when its invalid shares cross the threshold. A peer's score
// starts over when it reconnects.
func (n *Node) RecordShareOutcome(id peer.ID, bad bool) {
	if id == "" || n.Host.Network().Connectedness(id) != network.Connected {
		return
	}
	if n.scores.record(id, bad) {
		logging.Warnf("P2P: Disconnecting peer %s for relaying invalid shares", id)
		_ = n.Host.Network().ClosePeer(id)
	}
}
