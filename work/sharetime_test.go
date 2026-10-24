package work

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func timedChain(sc *ShareChain, tag byte, n int, parent *chainhash.Hash, start int64) []*Share {
	out := make([]*Share, n)
	for i := 0; i < n; i++ {
		h := testHash(tag, i)
		sh := &Share{Hash: h, POWHash: &chainhash.Hash{}, Target: DiffToTarget(1), Type: ShareVersionV2}
		sh.ShareInfo.ShareData.PreviousShareHash = parent
		sh.ShareInfo.Timestamp = int32(start + int64(15*i))
		sh.MinHeader.Timestamp = uint32(sh.ShareInfo.Timestamp)
		sc.shares[h.String()] = sh
		sc.verified[h.String()] = true
		out[i] = sh
		parent = h
	}
	return out
}

func TestCheckShareTime(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		ts   time.Time
		ok   bool
	}{
		{"now", now, true},
		{"just inside future limit", now.Add(MaxShareTimeFuture - time.Minute), true},
		{"past future limit", now.Add(MaxShareTimeFuture + time.Minute), false},
		{"older than retention", now.Add(-maxShareAge - time.Minute), false},
	} {
		sh := &Share{}
		sh.ShareInfo.Timestamp = int32(tc.ts.Unix())
		sh.MinHeader.Timestamp = uint32(now.Unix())
		if got := CheckShareTime(sh, now) == ""; got != tc.ok {
			t.Errorf("%s: accepted=%v, want %v", tc.name, got, tc.ok)
		}
		sh.ShareInfo.Timestamp, sh.MinHeader.Timestamp = int32(now.Unix()), uint32(tc.ts.Unix())
		if got := CheckShareTime(sh, now) == ""; got != tc.ok {
			t.Errorf("%s (header time): accepted=%v, want %v", tc.name, got, tc.ok)
		}
	}
}

func TestAddSharesRejectsBadTimesAndTargets(t *testing.T) {
	now := time.Now().Unix()
	for _, tc := range []struct {
		name string
		ts   int64
		diff float64
	}{
		{"future", now + 40*3600, 1},
		{"past", now - 80*3600, 1},
		{"easy target", now, MinPoolShareDiff / 2},
	} {
		sc := newTestShareChain()
		sh := Share{Hash: testHash(9, 0), Target: DiffToTarget(tc.diff)}
		sh.ShareInfo.Timestamp = int32(tc.ts)
		sh.MinHeader.Timestamp = uint32(tc.ts)
		if n := sc.AddShares([]Share{sh}); n != 0 || len(sc.shares) != 0 {
			t.Errorf("%s: share was stored", tc.name)
		}
	}
}

func TestCheckConsensusMedianTimePast(t *testing.T) {
	sc := newTestShareChain()
	chain := timedChain(sc, 1, 30, nil, ConsensusV3ActivationTime)
	tip := chain[29]
	sc.mutex.RLock()
	mtp, ok := sc.medianTimePastLocked(tip.Hash)
	sc.mutex.RUnlock()
	if !ok || mtp != int64(chain[24].ShareInfo.Timestamp) {
		t.Fatalf("median time past = %d, %v; want %d", mtp, ok, chain[24].ShareInfo.Timestamp)
	}
	child := func(ts int64, typ uint64) bool {
		sh := &Share{Hash: testHash(2, int(ts&0xffff)), POWHash: &chainhash.Hash{}, Target: DiffToTarget(1), Type: typ}
		sh.ShareInfo.ShareData.PreviousShareHash = tip.Hash
		sh.ShareInfo.Timestamp = int32(ts)
		sc.mutex.RLock()
		eff := sc.effectiveTimeLocked(sh, tip.Hash)
		sc.mutex.RUnlock()
		return sc.checkConsensus(sh, tip.Hash, sc.chainLen[tip.Hash.String()], eff)
	}
	if !child(mtp, ShareVersionV2) {
		t.Error("share at median time past rejected")
	}
	if child(mtp-1, ShareVersionV2) {
		t.Error("share before median time past accepted")
	}
	if child(V2MandatoryActivationTime-1, ShareVersionV1) {
		t.Error("backdated V1 share accepted under a post-V2 parent")
	}
}

func TestMedianTimePastRuleWaitsForActivation(t *testing.T) {
	sc := newTestShareChain()
	chain := timedChain(sc, 1, 30, nil, ConsensusV3ActivationTime-3600)
	tip := chain[29]
	sh := &Share{Hash: testHash(2, 0), POWHash: &chainhash.Hash{}, Target: DiffToTarget(1), Type: ShareVersionV2}
	sh.ShareInfo.ShareData.PreviousShareHash = tip.Hash
	sh.ShareInfo.Timestamp = int32(V2MandatoryActivationTime)
	sc.mutex.RLock()
	eff := sc.effectiveTimeLocked(sh, tip.Hash)
	sc.mutex.RUnlock()
	if !sc.checkConsensus(sh, tip.Hash, 30, eff) {
		t.Error("median time past rule applied before activation")
	}
}
