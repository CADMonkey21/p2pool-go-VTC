package work

import (
	"fmt"
	"math"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/wire"
)

const (
	maxOrphanAge     = 40  // How many resolve cycles an orphan can live before being purged
	requestEvery     = 10  // Repeat parent request every N cycles
	maxResolvePasses = 100 // Safety break to prevent infinite loops on corrupted data
)

// orphanInfo holds a share and its age in resolve cycles.
type orphanInfo struct {
	share *wire.Share
	age   uint8
}

// ChainStats holds calculated statistics for the sharechain.
type ChainStats struct {
	SharesTotal   int
	SharesOrphan  int
	SharesDead    int
	Efficiency    float64
	PoolHashrate  float64
	TimeToBlock   float64
	CurrentPayout float64 // Payout is a placeholder for now, as PPLNS is a major feature.
}

type ShareChain struct {
	SharesChannel         chan []wire.Share
	NeedShareChannel      chan *chainhash.Hash
	Tip                   *ChainShare
	Tail                  *ChainShare
	AllShares             map[string]*ChainShare
	AllSharesByPrev       map[string]*ChainShare
	disconnectedShares    map[string]*orphanInfo
	requestedParents      map[string]time.Time // Cache for pending parent requests
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
		requestedParents:      make(map[string]time.Time),
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
			logging.Debugf("Ignoring share with Bits == 0")
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

// GetShare retrieves a single share from the chain by its hash.
func (sc *ShareChain) GetShare(hashStr string) *wire.Share {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()
	if cs, ok := sc.AllShares[hashStr]; ok {
		return cs.Share
	}
	return nil
}

func (sc *ShareChain) GetTipHash() *chainhash.Hash {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	if sc.Tip != nil {
		return sc.Tip.Share.Hash
	}
	return &chainhash.Hash{}
}

// GetStats calculates and returns statistics for the share chain.
func (sc *ShareChain) GetStats() ChainStats {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	stats := ChainStats{}
	stats.SharesTotal = len(sc.AllShares)
	stats.SharesOrphan = len(sc.disconnectedShares)

	// Use a 30-minute window for pool hashrate calculation
	lookbackDuration := 30 * time.Minute
	startTime := time.Now().Add(-lookbackDuration)

	var totalWork = new(big.Int)
	var deadShares = 0

	current := sc.Tip
	for current != nil && time.Unix(int64(current.Share.ShareInfo.Timestamp), 0).After(startTime) {
		if current.Share.ShareInfo.ShareData.StaleInfo != wire.StaleInfoNone {
			deadShares++
		}

		// Difficulty = max_target / share_target
		// Work is proportional to difficulty
		maxTarget := new(big.Int)
		maxTarget.SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

		shareTarget := blockchain.CompactToBig(current.Share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			shareTarget.SetInt64(1)
		}

		difficulty := new(big.Int).Div(maxTarget, shareTarget)
		totalWork.Add(totalWork, difficulty)

		current = current.Previous
	}

	stats.SharesDead = deadShares

	if stats.SharesTotal > 0 {
		stats.Efficiency = 100 * (1 - (float64(stats.SharesDead+stats.SharesOrphan) / float64(stats.SharesTotal)))
	}

	// Pool Hashrate = (Total Work * 2^32) / Time Period
	totalWorkFloat := new(big.Float).SetInt(totalWork)
	pow32 := new(big.Float).SetFloat64(math.Pow(2, 32))
	workTerm := new(big.Float).Mul(totalWorkFloat, pow32)

	poolRate, _ := new(big.Float).Quo(workTerm, new(big.Float).SetFloat64(lookbackDuration.Seconds())).Float64()
	stats.PoolHashrate = poolRate

	// Expected time to block (in seconds) = Network Difficulty * 2^32 / Pool Hashrate
	// For now, we'll estimate network difficulty from the tip share. A real implementation would get this from RPC.
	if sc.Tip != nil {
		netTarget := blockchain.CompactToBig(sc.Tip.Share.MinHeader.Bits)
		if netTarget.Sign() > 0 {
			maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
			netDiff := new(big.Int).Div(maxTarget, netTarget)

			netDiffFloat := new(big.Float).SetInt(netDiff)
			netWorkTerm := new(big.Float).Mul(netDiffFloat, pow32)

			if stats.PoolHashrate > 0 {
				timeToBlock, _ := new(big.Float).Quo(netWorkTerm, new(big.Float).SetFloat64(stats.PoolHashrate)).Float64()
				stats.TimeToBlock = timeToBlock
			}
		}
	}

	// Placeholder for Payout
	stats.CurrentPayout = 0.0

	return stats
}

func (sc *ShareChain) Resolve(skipCommit bool) {
	sc.disconnectedShareLock.Lock()
	defer sc.disconnectedShareLock.Unlock()

	logging.Debugf("Resolving sharechain with %d disconnected shares", len(sc.disconnectedShares))

	passCount := 0
	for changedInLoop := true; changedInLoop; {
		// Anti-hang safeguard
		passCount++
		if passCount > maxResolvePasses {
			logging.Warnf("Resolve loop exceeded max passes, breaking to prevent hang.")
			break
		}

		changedInLoop = false

		for hashStr, orphan := range sc.disconnectedShares {
			if orphan.share.ShareInfo.ShareData.PreviousShareHash == nil {
				continue
			}
			prevHashStr := orphan.share.ShareInfo.ShareData.PreviousShareHash.String()
			sc.allSharesLock.Lock()
			parent, exists := sc.AllShares[prevHashStr]
			sc.allSharesLock.Unlock()

			if exists {
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

		sc.allSharesLock.Lock()
		if !changedInLoop && sc.Tip == nil && len(sc.disconnectedShares) > 0 {
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
				changedInLoop = true
				delete(sc.disconnectedShares, bestOrphanHash)
			}
		}
		sc.allSharesLock.Unlock()

		if !changedInLoop {
			for hashStr, orphan := range sc.disconnectedShares {
				orphan.age++
				if orphan.age > maxOrphanAge {
					logging.Debugf("Purging stale orphan share %s", hashStr[:12])
					delete(sc.disconnectedShares, hashStr)
				} else {
					if lastReq, ok := sc.requestedParents[hashStr]; !ok || time.Since(lastReq) > 30*time.Second {
						if orphan.share.ShareInfo.ShareData.PreviousShareHash != nil {
							select {
							case sc.NeedShareChannel <- orphan.share.ShareInfo.ShareData.PreviousShareHash:
								logging.Debugf("Requesting missing parent %s for share %s", orphan.share.ShareInfo.ShareData.PreviousShareHash.String()[:12], hashStr[:12])
								sc.requestedParents[hashStr] = time.Now()
							default:
								// channel full
							}
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
