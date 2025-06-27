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

type ShareChain struct {
	SharesChannel         chan []wire.Share
	NeedShareChannel      chan *chainhash.Hash
	Tip                   *ChainShare
	Tail                  *ChainShare
	AllShares             map[string]*ChainShare
	AllSharesByPrev       map[string]*ChainShare
	disconnectedShares    []*wire.Share
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
		disconnectedShares:    make([]*wire.Share, 0),
		allSharesLock:         sync.Mutex{},
		AllSharesByPrev:       map[string]*ChainShare{},
		AllShares:             map[string]*ChainShare{},
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
		share := s[i] // Create a new variable for the loop to avoid pointer issues

		if share.Hash != nil && share.IsValid() {
			sc.allSharesLock.Lock()
			_, ok := sc.AllShares[share.Hash.String()]
			if !ok {
				logging.Infof("ShareChain: Adding valid share %s to disconnected list", share.Hash.String()[:12])
				sc.disconnectedShares = append(sc.disconnectedShares, &share)
			}
			sc.allSharesLock.Unlock()
		} else {
			if share.POWHash != nil && share.Hash != nil {
				target := blockchain.CompactToBig(share.ShareInfo.Bits)
				if target.Sign() < 0 {
					target.Abs(target)
				}
				logging.Debugf("share %s fails PoW – target %064x vs hash %064x",
					share.Hash.String()[:12], target.Bytes(), blockchain.HashToBig(share.POWHash).Bytes())
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
	// Return a zero hash if there's no tip yet.
	return &chainhash.Hash{}
}

func (sc *ShareChain) Resolve(skipCommit bool) {
	logging.Debugf("Resolving sharechain with %d disconnected shares", len(sc.disconnectedShares))
	var changedInLoop = true
	for changedInLoop {
		changedInLoop = false
		newDisconnected := []*wire.Share{}

		for _, share := range sc.disconnectedShares {
			if share.ShareInfo.ShareData.PreviousShareHash == nil {
				newDisconnected = append(newDisconnected, share)
				continue
			}

			prevHashStr := share.ShareInfo.ShareData.PreviousShareHash.String()

			sc.allSharesLock.Lock()
			parent, exists := sc.AllShares[prevHashStr]
			isGenesis := sc.Tip == nil && len(sc.AllShares) == 0
			sc.allSharesLock.Unlock()

			if isGenesis {
				logging.Infof("Accepting share %s as genesis", share.Hash.String()[:12])
				newChainShare := &ChainShare{Share: share}
				sc.allSharesLock.Lock()
				sc.AllShares[share.Hash.String()] = newChainShare
				sc.AllSharesByPrev[prevHashStr] = newChainShare
				sc.Tip = newChainShare
				sc.Tail = newChainShare
				sc.allSharesLock.Unlock()
				changedInLoop = true
				continue
			}

			if exists {
				logging.Infof("Linking share %s to parent %s", share.Hash.String()[:12], prevHashStr[:12])
				newChainShare := &ChainShare{Share: share, Previous: parent}
				parent.Next = newChainShare

				sc.allSharesLock.Lock()
				sc.AllShares[share.Hash.String()] = newChainShare
				sc.AllSharesByPrev[prevHashStr] = newChainShare

				// If the parent was the tip, this is the new tip
				if sc.Tip == parent {
					sc.Tip = newChainShare
					target := blockchain.CompactToBig(share.ShareInfo.Bits)
					if target.Sign() < 0 {
						target.Abs(target)
					}
					logging.Infof("Accepted share %s becomes new tip. Height: %d, Target: %064x",
						share.Hash.String()[:12], share.ShareInfo.AbsHeight, target.Bytes())
				}
				sc.allSharesLock.Unlock()
				changedInLoop = true
			} else {
				newDisconnected = append(newDisconnected, share)
			}
		}
		sc.disconnectedShares = newDisconnected
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
			return nil // It's okay if the file doesn't exist
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
