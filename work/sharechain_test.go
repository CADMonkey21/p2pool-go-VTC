package work

import (
	"fmt"
	"testing"
	"time"

	"bytes"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func newTestShareChain() *ShareChain {
	nullHash, _ := chainhash.NewHash(make([]byte, 32))
	return &ShareChain{
		shares: make(map[string]*Share), verified: make(map[string]bool), received: make(map[string]time.Time), cumWork: make(map[string]float64), chainLen: make(map[string]int), children: make(map[string][]string),
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

func TestTimestampValidation(t *testing.T) {
	sc := NewShareChain(nil)
	now := time.Now().Unix()

	// Future share (> 2h ahead) should be rejected by AddShares
	hFuture := testHash('f', 1)
	shFuture := Share{Hash: hFuture}
	shFuture.ShareInfo.Timestamp = int32(now + MaxShareTimeFutureSecs + 60)
	shFuture.MinHeader.Timestamp = uint32(shFuture.ShareInfo.Timestamp)
	if added := sc.AddShares([]Share{shFuture}); added != 0 {
		t.Fatalf("expected future share to be rejected, got added=%d", added)
	}
	if sc.HasShare(hFuture.String()) {
		t.Fatalf("expected future share not to be stored in sharechain")
	}

	// CreateShare should reject nTime below job.MinTime or > 2h in the future
	job := &BlockTemplate{
		MinTime: now - 60,
		CurTime: now,
		Bits:    "1d00ffff",
	}
	pastHex := fmt.Sprintf("%08x", uint32(now-120))
	if _, err := CreateShare(job, "00000001", "00000002", pastHex, "00000001", "vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l", sc, 1.0, nil, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected CreateShare to reject nTime below job.MinTime")
	}
	futHex := fmt.Sprintf("%08x", uint32(now+MaxShareTimeFutureSecs+120))
	if _, err := CreateShare(job, "00000001", "00000002", futHex, "00000001", "vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l", sc, 1.0, nil, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected CreateShare to reject nTime too far in the future")
	}
}

// testShare returns a share on top of parent that passes IsValid without Verthash:
// its PoW hash is 1 and its target declares diff.
func testShare(tag byte, i int, parent *chainhash.Hash, diff float64) Share {
	var pow chainhash.Hash
	pow[31] = 1
	now := int32(time.Now().Unix())
	sh := Share{Hash: testHash(tag, i), POWHash: &pow, Target: DiffToTarget(diff), Type: ShareVersionV2}
	sh.ShareInfo.ShareData.PreviousShareHash = parent
	sh.ShareInfo.ShareData.PubKeyHash = make([]byte, 20)
	sh.ShareInfo.Timestamp = now
	sh.MinHeader.Timestamp = uint32(now)
	em := ComputeShareMetadataCommitment(
		sh.Type,
		sh.ShareInfo.ShareData.PreviousShareHash,
		sh.Target,
		sh.ShareInfo.ShareData.PubKeyHashVersion,
		sh.ShareInfo.ShareData.PubKeyHash,
		sh.ShareInfo.ShareData.Subsidy,
		sh.ShareInfo.ShareData.Donation,
		sh.ShareInfo.ShareData.StaleInfo,
		sh.ShareInfo.AbsHeight,
	)
	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(&wire.TxIn{SignatureScript: []byte{0x01, 0x00}})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: em})
	var buf bytes.Buffer
	_ = tx.Serialize(&buf)
	sh.ShareInfo.ShareData.CoinBase = buf.Bytes()
	return sh
}

func TestAddSharesOutcomes(t *testing.T) {
	sc := newTestShareChain()
	root := testShare(1, 0, nil, 1)
	child := testShare(1, 1, root.Hash, 1)
	orphan := testShare(2, 0, testHash(9, 0), 1)
	badPoW := testShare(3, 0, nil, 1)
	var high chainhash.Hash
	high[0] = 0xff
	badPoW.POWHash = &high

	got, added := sc.AddSharesOutcomes([]Share{root, child, orphan, badPoW, root})
	want := []ShareOutcome{ShareAccepted, ShareAccepted, SharePending, ShareInvalid, ShareDuplicate}
	if added != 2 {
		t.Fatalf("added %d, want 2", added)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("share %d: outcome %d, want %d", i, got[i], want[i])
		}
	}
	if len(sc.received) != 1 || !sc.received[orphan.Hash.String()].After(time.Time{}) {
		t.Fatalf("received tracks %d shares, want only the pending one", len(sc.received))
	}
	if sc.AddShares([]Share{testShare(4, 0, nil, 1)}) != 1 {
		t.Fatal("AddShares did not report the verified share")
	}
}

func TestAddSharesOutcomesConsensusReject(t *testing.T) {
	sc := newTestShareChain()
	// The missing parent leaves the PPLNS window incomplete, so the coinbase is not checked.
	chain := addTestChain(sc, 1, TargetLookbehind, testHash(9, 0))
	for _, h := range chain {
		sc.verified[h] = true
	}
	sc.recomputeWorkAndTipLocked()
	tip, _ := chainhash.NewHashFromStr(chain[len(chain)-1])

	// The chain expects about MinPoolShareDiff; a share claiming far more is invalid.
	got, _ := sc.AddSharesOutcomes([]Share{testShare(2, 0, tip, 100)})
	if got[0] != ShareInvalid {
		t.Fatalf("outcome %d, want ShareInvalid", got[0])
	}
	if len(sc.received) != 0 || sc.HasShare(testHash(2, 0).String()) {
		t.Fatal("rejected share is still held")
	}
	got, _ = sc.AddSharesOutcomes([]Share{testShare(2, 1, tip, sc.GetExpectedShareDifficulty(tip))})
	if got[0] != ShareAccepted {
		t.Fatalf("outcome %d, want ShareAccepted", got[0])
	}
}

func TestPruneUnverified(t *testing.T) {
	sc := newTestShareChain()
	now := time.Now()
	old := now.Add(-unverifiedShareTTL)
	// A verified chain whose oldest shares are unverified for want of a parent.
	main := addTestChain(sc, 1, 10, testHash(9, 0))
	for i, h := range main {
		if i < 3 {
			sc.received[h] = old
		} else {
			sc.verified[h] = true
		}
	}
	// Unverified shares that build on a missing parent, some old and some new.
	orphan := addTestChain(sc, 2, 4, testHash(9, 1))
	for i, h := range orphan {
		sc.received[h] = old
		if i >= 2 {
			sc.received[h] = now
		}
	}
	sc.recomputeWorkAndTipLocked()

	sc.pruneUnverified(now)
	for _, h := range main {
		if !sc.HasShare(h) {
			t.Fatal("pruned a share a verified share builds on")
		}
	}
	for i, h := range orphan {
		if sc.HasShare(h) != (i >= 2) {
			t.Fatalf("orphan %d: held %v, want %v", i, sc.HasShare(h), i >= 2)
		}
	}
	if len(sc.received) != 5 || sc.changes != 1 {
		t.Fatalf("received %d, changes %d; want 5 and 1", len(sc.received), sc.changes)
	}
	sc.pruneUnverified(now)
	if sc.changes != 1 {
		t.Fatal("prune with nothing to drop counted a change")
	}
}

func TestPruneUnverifiedCap(t *testing.T) {
	sc := newTestShareChain()
	now := time.Now()
	n := maxUnverifiedShares + 5
	chain := addTestChain(sc, 1, n, testHash(9, 0))
	for i, h := range chain {
		sc.received[h] = now.Add(time.Duration(i-n) * time.Millisecond)
	}
	sc.recomputeWorkAndTipLocked()

	sc.pruneUnverified(now)
	if len(sc.received) != maxUnverifiedShares || len(sc.shares) != maxUnverifiedShares {
		t.Fatalf("held %d unverified shares, want %d", len(sc.received), maxUnverifiedShares)
	}
	for i := 0; i < 5; i++ {
		if sc.HasShare(chain[i]) {
			t.Fatalf("share %d received earliest was kept", i)
		}
	}
	if !sc.HasShare(chain[5]) {
		t.Fatal("dropped more than the oldest shares")
	}
}
