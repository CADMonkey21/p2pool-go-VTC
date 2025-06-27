package work

import (
	"fmt"
	"os"
	"sync"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/wire"
)

const (
	maxOrphanAge   = 40 // How many resolve cycles an orphan can live before being purged
	requestEvery   = 10 // Repeat parent request every N cycles
)

// orphanInfo holds a share and its age in resolve cycles.
type orphanInfo struct {
	share *wire.Share
	age   uint8
}

type ShareChain struct {
	SharesChannel         chan []wire.Share
	NeedShareChannel      chan *chainhash.Hash
	Tip                   *ChainShare
	Tail                  *ChainShare
	AllShares             map[string]*ChainShare
	AllSharesByPrev       map[string]*ChainShare
	disconnectedShares    map[string]*orphanInfo
	disconnectedShareLock sync.Mutex
	allSharesLock         sync.Mutex
}

type ChainShare struct {
	Share    *wire.Share
	Previous *ChainShare
	Next     *ChainShare
}

func NewShareChain() *ShareChain {
	sc := &ShareChain{
		disconnectedShares:    make(map[string]*orphanInfo),
		allSharesLock:         sync.Mutex{},
		AllSharesByPrev:       make(map[string]*ChainShare),
		AllShares:             make(map[string]*ChainShare),
		disconnectedShareLock: sync.Mutex{},
		SharesChannel:         make(chan []wire.Share, 10),
		NeedShareChannel:      make(chan *chainhash.Hash, 10),
	}
	go sc.ReadShareChan()
	return sc
}

func (sc *ShareChain) ReadShareChan() {
	for s := range sc.SharesChannel {
		sc.AddShares(s)
	}
}

// AddShares now correctly validates incoming shares before processing them.
func (sc *ShareChain) AddShares(s []wire.Share) {
	sc.disconnectedShareLock.Lock()
	defer sc.disconnectedShareLock.Unlock()

	for i := range s {
		share := s[i]

		// Early filter for shares that are guaranteed to be invalid
		if share.ShareInfo.Bits == 0 {
			continue
		}

		if share.Hash != nil && share.IsValid() {
			hashStr := share.Hash.String()
			sc.allSharesLock.Lock()
			_, inAllShares := sc.AllShares[hashStr]
			sc.allSharesLock.Unlock()

			_, inDisconnected := sc.disconnectedShares[hashStr]

			if !inAllShares && !inDisconnected {
				logging.Infof("ShareChain: Adding valid share %s to disconnected list", hashStr[:12])
				sc.disconnectedShares[hashStr] = &orphanInfo{share: &share, age: 0}
			}
		} else {
			if share.POWHash != nil && share.Hash != nil {
				target := blockchain.CompactToBig(share.ShareInfo.Bits)
				if target.Sign() < 0 {
					target.Abs(target)
				}
				logging.Debugf("share %s PoW – target %064x  hash %064x",
					share.Hash.String()[:12], target, blockchain.HashToBig(share.POWHash))
			} else {
				logging.Warnf("ShareChain: Ignoring invalid share (nil hash or PoWHash).")
			}
		}
	}
	sc.Resolve(false)
}

func (sc *ShareChain) GetTipHash() *chainhash.Hash {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	if sc.Tip != nil {
		return sc.Tip.Share.Hash
	}
	return &chainhash.Hash{}
}

func (sc *ShareChain) Resolve(skipCommit bool) {
	logging.Debugf("Resolving sharechain with %d disconnected shares", len(sc.disconnectedShares))
	for changedInLoop := true; changedInLoop; {
		changedInLoop = false

		// First pass: try to link everything
		for hashStr, orphan := range sc.disconnectedShares {
			prevHashStr := orphan.share.ShareInfo.ShareData.PreviousShareHash.String()
			sc.allSharesLock.Lock()
			parent, exists := sc.AllShares[prevHashStr]
			sc.allSharesLock.Unlock()

			if exists {
				// Parent found, link the share
				logging.Infof("Linking share %s to parent %s", hashStr[:12], prevHashStr[:12])
				newChainShare := &ChainShare{Share: orphan.share, Previous: parent}
				parent.Next = newChainShare

				sc.allSharesLock.Lock()
				sc.AllShares[hashStr] = newChainShare

				if sc.Tip == parent {
					sc.Tip = newChainShare
					target := blockchain.CompactToBig(orphan.share.ShareInfo.Bits)
					if target.Sign() < 0 {
						target.Abs(target)
					}
					logging.Infof("Accepted share %s becomes new tip. Height: %d, Target: %064x",
						hashStr[:12], orphan.share.ShareInfo.AbsHeight, target)
				}
				sc.allSharesLock.Unlock()

				delete(sc.disconnectedShares, hashStr)
				changedInLoop = true
			}
		}

		// If no changes were made and the chain is still empty, consider forcing a genesis
		sc.allSharesLock.Lock()
		if !changedInLoop && sc.Tip == nil && len(sc.disconnectedShares) > 0 {
			// Find the best candidate to be our new genesis
			var bestOrphan *orphanInfo
			var bestOrphanHash string
			for hash, orphan := range sc.disconnectedShares {
				if bestOrphan == nil || orphan.share.ShareInfo.AbsHeight > bestOrphan.share.ShareInfo.AbsHeight {
					bestOrphan = orphan
					bestOrphanHash = hash
				}
			}

			if bestOrphan != nil {
				logging.Warnf("Forcing genesis from best orphan share %s at height %d", bestOrphanHash[:12], bestOrphan.share.ShareInfo.AbsHeight)
				cs := &ChainShare{Share: bestOrphan.share}
				sc.AllShares[bestOrphanHash] = cs
				sc.Tip, sc.Tail = cs, cs
				changedInLoop = true // Restart the loop to link any children
				delete(sc.disconnectedShares, bestOrphanHash)
			}
		}
		sc.allSharesLock.Unlock()

		// If still no changes, age the remaining orphans
		if !changedInLoop {
			for hashStr, orphan := range sc.disconnectedShares {
				orphan.age++
				if orphan.age > maxOrphanAge { // Purge if it's been an orphan for too long
					logging.Debugf("Purging stale orphan share %s", hashStr[:12])
					delete(sc.disconnectedShares, hashStr)
				} else if orphan.age%requestEvery == 1 { // Periodically re-request the parent
					if orphan.share.ShareInfo.ShareData.PreviousShareHash != nil {
						select {
						case sc.NeedShareChannel <- orphan.share.ShareInfo.ShareData.PreviousShareHash:
							logging.Debugf("Requesting missing parent %s for share %s", orphan.share.ShareInfo.ShareData.PreviousShareHash.String()[:12], hashStr[:12])
						default:
							// channel full – drop the request; we'll try again next cycle
						}
					}
				}
			}
		}
	}
}

func (sc *ShareChain) Commit() error {
	f, err := os.Create("shares.dat")
	if err != nil {
		return err
	}
	defer f.Close()

	shares := make([]wire.Share, 0)
	current := sc.Tail
	for current != nil {
		shares = append(shares, *current.Share)
		current = current.Next
	}
	return wire.WriteShares(f, shares)
}

func (sc *ShareChain) Load() error {
	f, err := os.Open("shares.dat")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	shares, err := wire.ReadShares(f)
	if err != nil && err.Error() != "EOF" {
		return fmt.Errorf("error reading shares: %v", err)
	}

	sc.AddShares(shares)
	sc.Resolve(true)

	return nil
}
