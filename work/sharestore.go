package work

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"go.etcd.io/bbolt"
)

const (
	shareDBFile   = "sharechain.db"
	shareJSONFile = "sharechain.json"
)

var bucketShares = []byte("shares")

func (sc *ShareChain) markDirtyLocked(h string) {
	if sc.dirty == nil {
		sc.dirty = make(map[string]struct{})
	}
	sc.dirty[h] = struct{}{}
}

// Load opens sharechain.db and loads its shares as verified. A sharechain.json left
// by an older version is imported once and renamed to sharechain.json.migrated.
func (sc *ShareChain) Load() error {
	return sc.load(shareDBFile, shareJSONFile)
}

func (sc *ShareChain) load(dbPath, jsonPath string) error {
	sc.commitMu.Lock()
	defer sc.commitMu.Unlock()
	if sc.db != nil {
		return fmt.Errorf("sharechain store already open")
	}
	_, statErr := os.Stat(dbPath)
	fresh := os.IsNotExist(statErr)
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketShares)
		return err
	}); err != nil {
		db.Close()
		return err
	}
	sc.db = db
	if fresh {
		if err := sc.migrateJSONLocked(jsonPath); err != nil {
			sc.db = nil
			db.Close()
			os.Remove(dbPath)
			return err
		}
	}
	var fileShares []Share
	if err := db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketShares).ForEach(func(k, v []byte) error {
			var sh Share
			if err := json.Unmarshal(v, &sh); err != nil {
				return fmt.Errorf("decode share %x: %w", k, err)
			}
			fileShares = append(fileShares, sh)
			return nil
		})
	}); err != nil {
		sc.db = nil
		db.Close()
		return err
	}
	sort.Slice(fileShares, func(i, j int) bool {
		return fileShares[i].ShareInfo.Timestamp < fileShares[j].ShareInfo.Timestamp
	})
	sc.loadTrusted(fileShares)
	logging.Infof("Loaded %d shares from disk", len(fileShares))
	return nil
}

// migrateJSONLocked copies the shares in a sharechain.json into the empty store.
// Older versions saved every share and reloaded them as trusted, so all are imported.
func (sc *ShareChain) migrateJSONLocked(jsonPath string) error {
	b, err := os.ReadFile(jsonPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var fileShares []Share
	if len(b) > 0 {
		if err := json.Unmarshal(b, &fileShares); err != nil {
			return fmt.Errorf("decode %s: %w", jsonPath, err)
		}
	}
	puts := make(map[string]*Share, len(fileShares))
	for i := range fileShares {
		sh := &fileShares[i]
		if sh.Hash == nil {
			_ = sh.CalculateHash()
		}
		if sh.Hash != nil {
			puts[sh.Hash.String()] = sh
		}
	}
	if err := sc.writeLocked(puts, nil); err != nil {
		return err
	}
	if err := os.Rename(jsonPath, jsonPath+".migrated"); err != nil {
		return err
	}
	logging.Infof("Migrated %d shares from %s to %s", len(puts), jsonPath, sc.db.Path())
	return nil
}

// Commit writes the shares verified or removed since the last commit to sharechain.db.
// Only verified shares are stored, since Load trusts everything it reads back.
func (sc *ShareChain) Commit() error {
	sc.commitMu.Lock()
	defer sc.commitMu.Unlock()
	if sc.db == nil {
		return fmt.Errorf("sharechain store not open")
	}
	sc.mutex.Lock()
	if len(sc.dirty) == 0 {
		sc.mutex.Unlock()
		return nil
	}
	dirty := sc.dirty
	sc.dirty = nil
	puts := make(map[string]*Share)
	var dels []string
	for h := range dirty {
		if s := sc.shares[h]; s != nil && sc.verified[h] {
			puts[h] = s
		} else {
			dels = append(dels, h)
		}
	}
	sc.mutex.Unlock()

	if err := sc.writeLocked(puts, dels); err != nil {
		sc.mutex.Lock()
		for h := range dirty {
			sc.markDirtyLocked(h)
		}
		sc.mutex.Unlock()
		return err
	}
	return nil
}

// writeLocked stores puts and deletes dels in one transaction. commitMu must be held.
func (sc *ShareChain) writeLocked(puts map[string]*Share, dels []string) error {
	type kv struct{ k, v []byte }
	enc := make([]kv, 0, len(puts))
	for h, s := range puts {
		k, err := shareKey(h)
		if err != nil {
			return err
		}
		v, err := json.Marshal(s)
		if err != nil {
			return err
		}
		enc = append(enc, kv{k, v})
	}
	return sc.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketShares)
		for _, e := range enc {
			if err := b.Put(e.k, e.v); err != nil {
				return err
			}
		}
		for _, h := range dels {
			k, err := shareKey(h)
			if err != nil {
				return err
			}
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func shareKey(h string) ([]byte, error) {
	hash, err := chainhash.NewHashFromStr(h)
	if err != nil {
		return nil, err
	}
	return hash[:], nil
}

// Close closes sharechain.db. Call Commit first to save pending changes.
func (sc *ShareChain) Close() error {
	sc.commitMu.Lock()
	defer sc.commitMu.Unlock()
	if sc.db == nil {
		return nil
	}
	err := sc.db.Close()
	sc.db = nil
	return err
}

// ExportShares returns all verified shares currently in memory, sorted oldest-first.
func (sc *ShareChain) ExportShares(limit int) []Share {
	sc.mutex.RLock()
	out := make([]Share, 0, len(sc.verified))
	for h := range sc.verified {
		if s := sc.shares[h]; s != nil {
			out = append(out, *s)
		}
	}
	sc.mutex.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].ShareInfo.Timestamp < out[j].ShareInfo.Timestamp
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// DumpShareDB reads all shares directly from sharechain.db in read-only mode when the node is stopped.
func DumpShareDB() ([]Share, error) {
	db, err := bbolt.Open(shareDBFile, 0600, &bbolt.Options{ReadOnly: true, Timeout: 1 * time.Second})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var shares []Share
	err = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketShares)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var sh Share
			if err := json.Unmarshal(v, &sh); err != nil {
				return fmt.Errorf("decode share %x: %w", k, err)
			}
			shares = append(shares, sh)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(shares, func(i, j int) bool {
		return shares[i].ShareInfo.Timestamp < shares[j].ShareInfo.Timestamp
	})
	return shares, nil
}
