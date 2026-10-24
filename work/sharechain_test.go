package work

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func newTestShareChain() *ShareChain {
	nullHash, _ := chainhash.NewHash(make([]byte, 32))
	return &ShareChain{
		shares: make(map[string]*Share), verified: make(map[string]bool), cumWork: make(map[string]float64), chainLen: make(map[string]int),
		genesisHash: nullHash,
		TipHash:     nullHash,
	}
}

func testHash(tag byte, i int) *chainhash.Hash {
	var b [32]byte
	b[0] = tag
	b[1], b[2], b[3] = byte(i), byte(i>>8), byte(i>>16)
	h, _ := chainhash.NewHash(b[:])
	return h
}

// addTestChain adds n shares linked oldest to newest on top of parent and returns
// their hashes, oldest first.
func addTestChain(sc *ShareChain, tag byte, n int, parent *chainhash.Hash) []string {
	now := int32(time.Now().Unix())
	out := make([]string, n)
	for i := 0; i < n; i++ {
		h := testHash(tag, i)
		sh := &Share{Hash: h}
		sh.ShareInfo.ShareData.PreviousShareHash = parent
		sh.ShareInfo.Timestamp = now - int32(n-i)
		sc.shares[h.String()] = sh
		out[i] = h.String()
		parent = h
	}
	return out
}

func replyHashes(shares []Share) []string {
	out := make([]string, len(shares))
	for i := range shares {
		out[i] = shares[i].Hash.String()
	}
	return out
}

func TestGetSharesByHashWithParentsWalk(t *testing.T) {
	sc := newTestShareChain()
	chain := addTestChain(sc, 1, 20, nil)

	got := replyHashes(sc.GetSharesByHashWithParents([]string{chain[19]}, 0, nil))
	if len(got) != 1 || got[0] != chain[19] {
		t.Fatalf("parents=0: got %d shares, want only the requested one", len(got))
	}

	got = replyHashes(sc.GetSharesByHashWithParents([]string{chain[19]}, 5, nil))
	if len(got) != 6 {
		t.Fatalf("parents=5: got %d shares, want 6", len(got))
	}
	for i, h := range got {
		if h != chain[19-i] {
			t.Fatalf("parents=5: share %d is not ancestor %d", i, i)
		}
	}

	// The walk ends at the oldest share held.
	if got := sc.GetSharesByHashWithParents([]string{chain[3]}, 100, nil); len(got) != 4 {
		t.Fatalf("walk past oldest share: got %d shares, want 4", len(got))
	}
	if got := sc.GetSharesByHashWithParents([]string{"unknown"}, 100, nil); len(got) != 0 {
		t.Fatalf("unknown hash: got %d shares, want 0", len(got))
	}
}

func TestGetSharesByHashWithParentsStops(t *testing.T) {
	sc := newTestShareChain()
	chain := addTestChain(sc, 1, 20, nil)

	got := replyHashes(sc.GetSharesByHashWithParents([]string{chain[19]}, 100, []string{chain[15], chain[2]}))
	if len(got) != 4 || got[3] != chain[16] {
		t.Fatalf("got %d shares, want 4 ending just above the first stop", len(got))
	}

	if got := sc.GetSharesByHashWithParents([]string{chain[19]}, 100, []string{chain[19]}); len(got) != 0 {
		t.Fatalf("requested hash in stops: got %d shares, want 0", len(got))
	}
}

func TestGetSharesByHashWithParentsMultipleHashes(t *testing.T) {
	sc := newTestShareChain()
	main := addTestChain(sc, 1, 20, nil)
	fork := addTestChain(sc, 2, 3, testHash(1, 9))

	// Walks share no shares: the second stops where it meets the first.
	got := replyHashes(sc.GetSharesByHashWithParents([]string{main[19], fork[2]}, 100, nil))
	if len(got) != 23 {
		t.Fatalf("got %d shares, want 23 distinct", len(got))
	}
}

func TestGetSharesByHashWithParentsCap(t *testing.T) {
	sc := newTestShareChain()
	chain := addTestChain(sc, 1, 1500, nil)

	if got := sc.GetSharesByHashWithParents([]string{chain[1499]}, 5000, nil); len(got) != MaxShareReply {
		t.Fatalf("one hash: got %d shares, want %d", len(got), MaxShareReply)
	}

	// Parents are split across the requested hashes.
	hashes := []string{chain[1499], chain[999], chain[499]}
	got := replyHashes(sc.GetSharesByHashWithParents(hashes, 5000, nil))
	if len(got) != MaxShareReply {
		t.Fatalf("three hashes: got %d shares, want %d", len(got), MaxShareReply)
	}
	if got[MaxShareReply/3+1] != chain[999] {
		t.Fatal("three hashes: second walk does not start at the second hash")
	}

	many := make([]string, 0, 1200)
	for i := 0; i < 1200; i++ {
		many = append(many, chain[i])
	}
	if got := sc.GetSharesByHashWithParents(many, 10, nil); len(got) != MaxShareReply {
		t.Fatalf("1200 hashes: got %d shares, want %d", len(got), MaxShareReply)
	}
}

func TestGetMissingParentHashes(t *testing.T) {
	sc := newTestShareChain()
	addTestChain(sc, 1, 20, nil)
	// A peer's newer chain whose parent is not held yet.
	gap := testHash(9, 0)
	addTestChain(sc, 2, 5, gap)
	sc.recomputeWorkAndTipLocked()

	for i := 0; i < 2; i++ {
		got := sc.GetMissingParentHashes(10)
		if len(got) != 1 || got[0] != gap.String() {
			t.Fatalf("call %d: got %v, want only the gap's hash", i, got)
		}
	}

	// A head already a full window plus lookbehind deep wants nothing below it.
	sc = newTestShareChain()
	addTestChain(sc, 1, DefaultPPLNSWindow+TargetLookbehind, gap)
	sc.recomputeWorkAndTipLocked()
	if got := sc.GetMissingParentHashes(10); len(got) != 0 {
		t.Fatalf("full-depth chain: got %v, want none", got)
	}
}

func TestGetStopHashes(t *testing.T) {
	sc := newTestShareChain()
	main := addTestChain(sc, 1, 20, nil)
	fork := addTestChain(sc, 2, 3, testHash(1, 9))
	short := addTestChain(sc, 3, 2, testHash(9, 0))
	sc.recomputeWorkAndTipLocked()

	got := sc.GetStopHashes(100)
	want := []string{main[19], main[9], fork[2], main[2], short[1], short[0]}
	if len(got) != len(want) {
		t.Fatalf("got %d stops, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stop %d: got %s, want %s", i, got[i][:8], want[i][:8])
		}
	}

	if got := sc.GetStopHashes(3); len(got) != 3 || got[0] != main[19] || got[1] != main[9] {
		t.Fatalf("max 3: got %d stops, want the best head and its ancestor first", len(got))
	}
}
