package work

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// fullRecompute is the original whole-DAG recompute, kept as the oracle for the
// incremental work and tip.
func fullRecompute(sc *ShareChain) {
	vis := make(map[string]uint8, len(sc.shares))
	var dfs func(string) (float64, int)
	dfs = func(h string) (float64, int) {
		if vis[h] == 2 {
			return sc.cumWork[h], sc.chainLen[h]
		}
		sh := sc.shares[h]
		if sh == nil || vis[h] == 1 {
			return 0, 0
		}
		vis[h] = 1
		var pw float64
		var pl int
		if p := sh.ShareInfo.ShareData.PreviousShareHash; p != nil && !p.IsEqual(sc.genesisHash) {
			if sc.shares[p.String()] != nil {
				pw, pl = dfs(p.String())
			}
		}
		d := sh.getTargetDiff()
		inc := 1
		if sh.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
			d = 0
			inc = 0
		} else if d < MinPoolShareDiff {
			d = MinPoolShareDiff
		}
		sc.cumWork[h], sc.chainLen[h] = pw+d, pl+inc
		vis[h] = 2
		return sc.cumWork[h], sc.chainLen[h]
	}
	var bestH *chainhash.Hash
	var bestW float64
	var bestS string
	for h, sh := range sc.shares {
		w, _ := dfs(h)
		if sc.verified[h] && sh != nil && sh.Hash != nil && sh.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			if bestH == nil || w > bestW+1e-9 || (w >= bestW-1e-9 && h > bestS) {
				bestH, bestW, bestS = sh.Hash, w, h
			}
		}
	}
	if bestH != nil {
		sc.TipHash = bestH
	}
}

// checkWork fails unless sc's work, lengths, tip and children index match the
// oracle run from the tip sc had before the operation.
func checkWork(t *testing.T, sc *ShareChain, prevTip *chainhash.Hash, op string) {
	t.Helper()
	o := &ShareChain{shares: sc.shares, verified: sc.verified, cumWork: map[string]float64{}, chainLen: map[string]int{}, genesisHash: sc.genesisHash, TipHash: prevTip}
	fullRecompute(o)
	if len(sc.cumWork) != len(sc.shares) || len(sc.chainLen) != len(sc.shares) {
		t.Fatalf("%s: %d work and %d length entries for %d shares", op, len(sc.cumWork), len(sc.chainLen), len(sc.shares))
	}
	for h := range sc.shares {
		if sc.cumWork[h] != o.cumWork[h] || sc.chainLen[h] != o.chainLen[h] {
			t.Fatalf("%s: share %s has work %v length %d, want %v %d", op, h[:8], sc.cumWork[h], sc.chainLen[h], o.cumWork[h], o.chainLen[h])
		}
	}
	if sc.TipHash.String() != o.TipHash.String() {
		t.Fatalf("%s: tip %s, want %s", op, sc.TipHash.String()[:8], o.TipHash.String()[:8])
	}
	n := 0
	for p, cs := range sc.children {
		if len(cs) == 0 {
			t.Fatalf("%s: empty children entry", op)
		}
		for _, c := range cs {
			sh := sc.shares[c]
			if sh == nil || sc.prevKeyLocked(sh) != p {
				t.Fatalf("%s: stale children entry %s", op, c[:8])
			}
		}
		n += len(cs)
	}
	linked := 0
	for _, sh := range sc.shares {
		if sc.prevKeyLocked(sh) != "" {
			linked++
		}
	}
	if n != linked {
		t.Fatalf("%s: %d children entries for %d linked shares", op, n, linked)
	}
}

func randHash(rng *rand.Rand) *chainhash.Hash {
	var b [32]byte
	rng.Read(b[:])
	h, _ := chainhash.NewHash(b[:])
	return h
}

func TestIncrementalWorkMatchesFullRecompute(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runWorkScenario(t, seed, seed%4 == 0) })
	}
}

func runWorkScenario(t *testing.T, seed int64, randDiffs bool) {
	rng := rand.New(rand.NewSource(seed))
	sc := newTestShareChain()
	dyadic := []float64{0, 0.25, 0.5, 1, 1.5, 2, 4.75}
	ts := int32(ConsensusV2ActivationTime - 1e6)
	var made, undelivered []*Share
	newShare := func() *Share {
		sh := &Share{Hash: randHash(rng)}
		switch r := rng.Intn(100); {
		case r < 8:
		case r < 12:
			sh.ShareInfo.ShareData.PreviousShareHash = sc.genesisHash
		case r < 17 || len(made) == 0:
			sh.ShareInfo.ShareData.PreviousShareHash = randHash(rng)
		default:
			k := len(made) - 1 - rng.Intn(min(len(made), 12))
			sh.ShareInfo.ShareData.PreviousShareHash = made[k].Hash
		}
		d := dyadic[rng.Intn(len(dyadic))]
		if randDiffs {
			d = rng.Float64() * 3
		}
		if d > 0 {
			sh.Target, sh.targetDiff = big.NewInt(1), d
		}
		if r := rng.Intn(100); r < 8 {
			sh.ShareInfo.ShareData.StaleInfo = StaleInfoOrphan
		} else if r < 12 {
			sh.ShareInfo.ShareData.StaleInfo = StaleInfoDOA
		}
		ts++
		sh.ShareInfo.Timestamp = ts
		made = append(made, sh)
		undelivered = append(undelivered, sh)
		return sh
	}
	held := func(f func(h string) bool) []string {
		var out []string
		for h := range sc.shares {
			if f(h) {
				out = append(out, h)
			}
		}
		return out
	}
	pick := func(hs []string, n int) []string {
		rng.Shuffle(len(hs), func(i, j int) { hs[i], hs[j] = hs[j], hs[i] })
		return hs[:min(n, len(hs))]
	}
	everyShare := func(string) bool { return true }
	unverified := func(h string) bool { return !sc.verified[h] }
	peak := 0
	for step := 0; step < 400; step++ {
		prevTip := sc.TipHash
		var op string
		switch r := rng.Intn(100); {
		case r < 45:
			op = "add"
			for i := rng.Intn(6); i >= 0; i-- {
				newShare()
			}
			var batch []string
			for i := 1 + rng.Intn(8); i > 0 && len(undelivered) > 0; i-- {
				// Mostly the newest first, so children often arrive before parents.
				k := len(undelivered) - 1 - rng.Intn(min(len(undelivered), 4))
				if rng.Intn(4) == 0 {
					k = rng.Intn(len(undelivered))
				}
				sh := undelivered[k]
				undelivered = append(undelivered[:k], undelivered[k+1:]...)
				if h := sh.Hash.String(); sc.shares[h] == nil {
					sc.shares[h] = sh
					batch = append(batch, h)
				}
			}
			sc.addWorkLocked(batch)
		case r < 60:
			op = "verifyPending"
			sc.verifyPending()
		case r < 72:
			op = "trust"
			for _, h := range pick(held(unverified), 1+rng.Intn(3)) {
				sc.markVerifiedLocked(h)
			}
		case r < 80:
			// As in verifyPending: unlink rejected shares, verify others, then drop
			// the rejected shares' work.
			op = "reject"
			var rej []string
			for _, h := range pick(held(unverified), 1+rng.Intn(3)) {
				if sc.shares[h] == nil || sc.verified[h] {
					continue
				}
				if rng.Intn(3) == 0 {
					sc.markVerifiedLocked(h)
					continue
				}
				undelivered = append(undelivered, sc.shares[h])
				sc.unlinkShareLocked(h)
				rej = append(rej, h)
			}
			if len(rej) > 0 {
				sc.dropWorkLocked(rej, false)
			}
		case r < 90:
			op = "remove"
			hs := pick(held(everyShare), 1+rng.Intn(4))
			for _, h := range hs {
				if rng.Intn(2) == 0 {
					undelivered = append(undelivered, sc.shares[h])
				}
			}
			sc.removeSharesLocked(hs)
		case r < 96:
			op = "prune"
			if hs := held(everyShare); len(hs) > 0 {
				cut := int64(sc.shares[pick(hs, 1)[0]].ShareInfo.Timestamp)
				if rng.Intn(2) == 0 {
					cut = int64(ts) - int64(rng.Intn(40))
				}
				sc.pruneLocked(cut)
			}
		default:
			op = "recompute"
			sc.recomputeWorkAndTipLocked()
		}
		checkWork(t, sc, prevTip, op)
		peak = max(peak, len(sc.shares))
	}
	if peak < 30 {
		t.Fatalf("held at most %d shares", peak)
	}
}

func TestIncrementalWorkLateParent(t *testing.T) {
	sc := newTestShareChain()
	ts := int32(ConsensusV2ActivationTime - 1e6)
	var shares []*Share
	var parent *chainhash.Hash
	for i := 0; i < 50; i++ {
		sh := &Share{Hash: testHash(1, i)}
		sh.ShareInfo.ShareData.PreviousShareHash = parent
		sh.ShareInfo.Timestamp = ts + int32(i)
		shares = append(shares, sh)
		parent = sh.Hash
	}
	// Children arrive first, newest to oldest, one share per add.
	for i := len(shares) - 1; i >= 0; i-- {
		prevTip := sc.TipHash
		h := shares[i].Hash.String()
		sc.shares[h] = shares[i]
		sc.addWorkLocked([]string{h})
		sc.verifyPending()
		checkWork(t, sc, prevTip, "add")
	}
	tip := shares[len(shares)-1].Hash.String()
	if sc.chainLen[tip] != 50 || sc.TipHash.String() != tip || len(sc.verified) != 50 {
		t.Fatalf("got length %d, tip %s, %d verified; want 50, the newest share, 50", sc.chainLen[tip], sc.TipHash.String()[:8], len(sc.verified))
	}
	prevTip := sc.TipHash
	sc.removeSharesLocked([]string{shares[10].Hash.String()})
	checkWork(t, sc, prevTip, "remove")
	if sc.chainLen[tip] != 39 {
		t.Fatalf("after removing share 10: tip length %d, want 39", sc.chainLen[tip])
	}
}

// buildBenchChain returns a chain of n verified shares with diffs varying around 1.
func buildBenchChain(n int) *ShareChain {
	sc := newTestShareChain()
	ts := int32(ConsensusV2ActivationTime - 1e6)
	var parent *chainhash.Hash
	for i := 0; i < n; i++ {
		sh := &Share{Hash: testHash(1, i), Target: big.NewInt(1), targetDiff: 1 + float64(i%7)/8}
		sh.ShareInfo.ShareData.PreviousShareHash = parent
		sh.ShareInfo.Timestamp = ts + int32(i)
		h := sh.Hash.String()
		sc.shares[h] = sh
		sc.verified[h] = true
		parent = sh.Hash
	}
	sc.recomputeWorkAndTipLocked()
	return sc
}

func benchShare(sc *ShareChain, i int) (*Share, string) {
	tip := sc.shares[sc.TipHash.String()]
	sh := &Share{Hash: testHash(2, i), Target: big.NewInt(1), targetDiff: 1}
	sh.ShareInfo.ShareData.PreviousShareHash = tip.Hash
	sh.ShareInfo.Timestamp = tip.ShareInfo.Timestamp + 1
	return sh, sh.Hash.String()
}

// BenchmarkAddShare adds one share on the tip of a 72h chain of 15s shares. Full is
// the old path, two whole-DAG recomputes per add; incremental is the new add and
// verifyPending.
func BenchmarkAddShare(b *testing.B) {
	const n = 72 * 3600 / 15
	b.Run("full", func(b *testing.B) {
		sc := buildBenchChain(n)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sh, h := benchShare(sc, i)
			sc.shares[h] = sh
			fullRecompute(sc)
			sc.verified[h] = true
			fullRecompute(sc)
		}
	})
	b.Run("incremental", func(b *testing.B) {
		sc := buildBenchChain(n)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sh, h := benchShare(sc, i)
			sc.shares[h] = sh
			sc.addWorkLocked([]string{h})
			sc.verifyPending()
		}
	})
}
