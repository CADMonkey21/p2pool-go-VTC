package work

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.etcd.io/bbolt"
)

func openTestStore(t *testing.T, dir string) *ShareChain {
	t.Helper()
	sc := newTestShareChain()
	if err := sc.load(filepath.Join(dir, shareDBFile), filepath.Join(dir, shareJSONFile)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc
}

func verifyTestShares(sc *ShareChain, hashes []string) {
	for _, h := range hashes {
		sc.verified[h] = true
		sc.markDirtyLocked(h)
	}
	sc.recomputeWorkAndTipLocked()
}

func storedHashes(t *testing.T, sc *ShareChain) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	err := sc.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketShares).ForEach(func(k, v []byte) error {
			var sh Share
			if err := json.Unmarshal(v, &sh); err != nil {
				return err
			}
			out[sh.Hash.String()] = append([]byte(nil), v...)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestShareStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sc := openTestStore(t, dir)
	chain := addTestChain(sc, 1, 10, nil)
	pending := addTestChain(sc, 2, 1, testHash(9, 0))
	verifyTestShares(sc, chain)
	sc.markDirtyLocked(pending[0])
	if err := sc.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}

	got := openTestStore(t, dir)
	if len(got.shares) != len(chain) {
		t.Fatalf("loaded %d shares, want the %d verified ones", len(got.shares), len(chain))
	}
	if got.shares[pending[0]] != nil {
		t.Fatal("unverified share was persisted")
	}
	for _, h := range chain {
		if !got.verified[h] {
			t.Fatalf("share %s not loaded as verified", h)
		}
		a, b := sc.shares[h], got.shares[h]
		if a.ShareInfo.Timestamp != b.ShareInfo.Timestamp || !a.ShareInfo.ShareData.PreviousShareHash.IsEqual(b.ShareInfo.ShareData.PreviousShareHash) {
			t.Fatalf("share %s did not round-trip", h)
		}
	}
	if got.TipHash.String() != chain[len(chain)-1] || got.chainLen[chain[len(chain)-1]] != len(chain) {
		t.Fatalf("tip %s len %d, want %s len %d", got.TipHash, got.chainLen[got.TipHash.String()], chain[len(chain)-1], len(chain))
	}
}

func TestShareStoreCommitWritesDeltas(t *testing.T) {
	sc := openTestStore(t, t.TempDir())
	chain := addTestChain(sc, 1, 10, nil)
	verifyTestShares(sc, chain)
	if err := sc.Commit(); err != nil {
		t.Fatal(err)
	}

	// Overwrite a stored share; a commit that rewrote the whole chain would restore it.
	sentinel := []byte(`{"sentinel":true}`)
	key, _ := shareKey(chain[3])
	if err := sc.db.Update(func(tx *bbolt.Tx) error { return tx.Bucket(bucketShares).Put(key, sentinel) }); err != nil {
		t.Fatal(err)
	}
	readSentinel := func() []byte {
		var v []byte
		sc.db.View(func(tx *bbolt.Tx) error {
			v = append([]byte(nil), tx.Bucket(bucketShares).Get(key)...)
			return nil
		})
		return v
	}

	if err := sc.Commit(); err != nil {
		t.Fatal(err)
	}
	if string(readSentinel()) != string(sentinel) {
		t.Fatal("commit with no changes rewrote shares")
	}

	more := addTestChain(sc, 2, 2, testHash(1, 9))
	verifyTestShares(sc, more)
	delete(sc.shares, chain[0])
	delete(sc.verified, chain[0])
	sc.markDirtyLocked(chain[0])
	if len(sc.dirty) != 3 {
		t.Fatalf("%d dirty shares, want 3", len(sc.dirty))
	}
	if err := sc.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(sc.dirty) != 0 {
		t.Fatalf("%d dirty shares after commit", len(sc.dirty))
	}
	if string(readSentinel()) != string(sentinel) {
		t.Fatal("commit rewrote an unchanged share")
	}
	key0, _ := shareKey(chain[0])
	var gone bool
	var n int
	sc.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketShares)
		gone = b.Get(key0) == nil
		n = b.Stats().KeyN
		return nil
	})
	if !gone {
		t.Fatal("removed share still stored")
	}
	if n != 11 {
		t.Fatalf("%d stored shares, want 11", n)
	}
}

func TestShareStorePruneMarksDirty(t *testing.T) {
	sc := openTestStore(t, t.TempDir())
	chain := addTestChain(sc, 1, 3, nil)
	verifyTestShares(sc, chain)
	if err := sc.Commit(); err != nil {
		t.Fatal(err)
	}
	sc.shares[chain[0]].ShareInfo.Timestamp = 1
	sc.PruneOldShares()
	if err := sc.Commit(); err != nil {
		t.Fatal(err)
	}
	stored := storedHashes(t, sc)
	if _, ok := stored[chain[0]]; ok || len(stored) != 2 {
		t.Fatalf("stored %d shares after pruning, want 2 without the pruned one", len(stored))
	}
}

func TestShareStoreMigratesJSON(t *testing.T) {
	dir := t.TempDir()
	src := newTestShareChain()
	chain := addTestChain(src, 1, 5, nil)
	var fileShares []Share
	for _, h := range chain {
		fileShares = append(fileShares, *src.shares[h])
	}
	b, _ := json.Marshal(fileShares)
	jsonPath := filepath.Join(dir, shareJSONFile)
	if err := os.WriteFile(jsonPath, b, 0644); err != nil {
		t.Fatal(err)
	}

	sc := openTestStore(t, dir)
	if len(sc.shares) != len(chain) || sc.TipHash.String() != chain[len(chain)-1] {
		t.Fatalf("migrated %d shares tip %s, want %d tip %s", len(sc.shares), sc.TipHash, len(chain), chain[len(chain)-1])
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Fatalf("%s still present after migration", jsonPath)
	}
	if _, err := os.Stat(jsonPath + ".migrated"); err != nil {
		t.Fatal(err)
	}
	if len(storedHashes(t, sc)) != len(chain) {
		t.Fatal("migrated shares not stored")
	}
	sc.Close()

	// A sharechain.json next to an existing store is not imported again.
	if err := os.WriteFile(jsonPath, []byte(`[]`), 0644); err != nil {
		t.Fatal(err)
	}
	again := openTestStore(t, dir)
	if len(again.shares) != len(chain) {
		t.Fatalf("reopened store holds %d shares, want %d", len(again.shares), len(chain))
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatal("sharechain.json beside an existing store was touched")
	}
}
