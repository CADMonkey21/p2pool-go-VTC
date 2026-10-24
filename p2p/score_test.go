package p2p

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestPeerScores(t *testing.T) {
	s := peerScores{m: make(map[peer.ID]*peerScore)}
	for i := 1; i < peerScoreMinBad; i++ {
		if s.record("a", true) {
			t.Fatalf("tripped after %d bad shares", i)
		}
	}
	if !s.record("a", true) {
		t.Fatal("did not trip at the bad share floor")
	}
	if s.record("a", true) {
		t.Fatal("tripped twice")
	}
	s.forget("a")
	if s.record("a", true) {
		t.Fatal("score did not start over")
	}

	// A peer with a good record trips only once bad shares pass the ratio.
	for i := 0; i < 30; i++ {
		s.record("b", false)
	}
	for bad := 1; bad <= 8; bad++ {
		// 7 of 37 is under 20%, 8 of 38 is over.
		if got := s.record("b", true); got != (bad == 8) {
			t.Fatalf("bad %d: tripped %v", bad, got)
		}
	}
}
