package work

import (
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/rpc"
	"github.com/gertjaap/p2pool-go/wire"
)

const (
	maxOrphanAge     = 40
	maxResolvePasses = 100
)

type orphanInfo struct {
	share *wire.Share
	age   uint8
}

type ChainStats struct {
	SharesTotal       int
	SharesOrphan      int
	SharesDead        int
	Efficiency        float64
	PoolHashrate      float64
	NetworkHashrate   float64
	NetworkDifficulty float64
	TimeToBlock       float64
	CurrentPayout     float64
}

type ShareChain struct {
	SharesChannel         chan []wire.Share
	NeedShareChannel      chan *chainhash.Hash
	Tip                   *ChainShare
	Tail                  *ChainShare
	AllShares             map[string]*ChainShare
	AllSharesByPrev       map[string][]*wire.Share
	disconnectedShares    map[string]*orphanInfo
	requestedParents      map[string]time.Time
	rpcClient             *rpc.Client
	disconnectedShareLock sync.Mutex
	allSharesLock         sync.Mutex
}

type ChainShare struct {
	Share    *wire.Share
	Previous *ChainShare
	Next     *ChainShare
}

func NewShareChain(client *rpc.Client) *ShareChain {
	sc := &ShareChain{
		disconnectedShares:    make(map[string]*orphanInfo),
		requestedParents:      make(map[string]time.Time),
		allSharesLock:         sync.Mutex{},
		AllSharesByPrev:       make(map[string][]*wire.Share),
		AllShares:             make(map[string]*ChainShare),
		disconnectedShareLock: sync.Mutex{},
		SharesChannel:         make(chan []wire.Share, 10),
		NeedShareChannel:      make(chan *chainhash.Hash, 10),
		rpcClient:             client,
	}
	go sc.ReadShareChan()
	return sc
}

func (sc *ShareChain) ReadShareChan() {
	for s := range sc.SharesChannel {
		sc.AddShares(s, false)
	}
}

func (sc *ShareChain) AddShares(s []wire.Share, trusted bool) {
	sc.disconnectedShareLock.Lock()
	defer sc.disconnectedShareLock.Unlock()

	for i := range s {
		share := s[i]

		if share.Hash == nil {
			logging.Warnf("ShareChain: Ignoring share with nil hash.")
			continue
		}

		if share.ShareInfo.Bits == 0 {
			if share.MinHeader.Bits != 0 {
				logging.Debugf("Share %s is missing Bits, deriving from MinHeader.Bits", share.Hash.String()[:12])
				share.ShareInfo.Bits = share.MinHeader.Bits
			} else if share.MinHeader.PreviousBlock != nil {
				logging.Debugf("Share %s is missing all Bits, querying daemon for block header %s", share.Hash.String()[:12], share.MinHeader.PreviousBlock.String())
				headerInfo, err := sc.rpcClient.GetBlockHeader(share.MinHeader.PreviousBlock)
				if err != nil {
					logging.Warnf("Failed to get block header for share: %v", err)
				} else {
					bits, err := strconv.ParseUint(headerInfo.Bits, 16, 32)
					if err == nil {
						logging.Debugf("Derived bits %08x from RPC for share %s", uint32(bits), share.Hash.String()[:12])
						share.ShareInfo.Bits = uint32(bits)
					}
				}
			}
		}

		if share.ShareInfo.Bits == 0 {
			logging.Warnf("Ignoring share %s with Bits == 0 after all fallbacks.", share.Hash.String()[:12])
			continue
		}

		if !trusted && !share.IsValid() {
			if share.POWHash != nil {
				target := blockchain.CompactToBig(share.ShareInfo.Bits)
				if target.Sign() < 0 {
					target.Abs(target)
				}
				logging.Debugf("share %s PoW is invalid – target %064x hash %064x",
					share.Hash.String()[:12], target, blockchain.HashToBig(share.POWHash))
			} else {
				logging.Warnf("ShareChain: Ignoring invalid share (nil PoWHash).")
			}
			continue
		}

		hashStr := share.Hash.String()
		sc.allSharesLock.Lock()
		_, inAllShares := sc.AllShares[hashStr]
		sc.allSharesLock.Unlock()

		_, inDisconnected := sc.disconnectedShares[hashStr]

		if !inAllShares && !inDisconnected {
			logging.Debugf("ShareChain: Adding share candidate %s to disconnected list", hashStr[:12])
			sc.disconnectedShares[hashStr] = &orphanInfo{share: &share, age: 0}

			if prev := share.ShareInfo.ShareData.PreviousShareHash; prev != nil {
				prevStr := prev.String()
				sc.AllSharesByPrev[prevStr] = append(sc.AllSharesByPrev[prevStr], &share)
			}
		}
	}
	sc.Resolve(false)
}

// GetSharesForPayout retrieves a slice of shares from the chain for PPLNS calculation.
// It traverses backwards from the block-finding share up to the window size.
func (sc *ShareChain) GetSharesForPayout(blockFindShareHash *chainhash.Hash, windowSize int) ([]*wire.Share, error) {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	startShare, ok := sc.AllShares[blockFindShareHash.String()]
	if !ok {
		return nil, fmt.Errorf("block-finding share %s not found in chain", blockFindShareHash.String())
	}

	payoutShares := make([]*wire.Share, 0, windowSize)
	current := startShare
	for i := 0; i < windowSize && current != nil; i++ {
		payoutShares = append(payoutShares, current.Share)
		current = current.Previous
	}

	return payoutShares, nil
}

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

func (sc *ShareChain) GetStats() ChainStats {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	stats := ChainStats{}
	stats.SharesTotal = len(sc.AllShares)
	stats.SharesOrphan = len(sc.disconnectedShares)

	// Get network stats from the daemon
	netInfo, err := sc.rpcClient.GetMiningInfo()
	if err != nil {
		logging.Warnf("Could not get network info from daemon: %v", err)
	} else {
		stats.NetworkHashrate = netInfo.NetworkHashPS
		stats.NetworkDifficulty = netInfo.Difficulty
	}

	lookbackDuration := 30 * time.Minute
	startTime := time.Now().Add(-lookbackDuration)

	var totalWork = new(big.Float).SetFloat64(0.0)
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	
	var deadShares = 0
	var sharesInWindow = 0

	current := sc.Tip
	for current != nil && time.Unix(int64(current.Share.ShareInfo.Timestamp), 0).After(startTime) {
		sharesInWindow++
		if current.Share.ShareInfo.ShareData.StaleInfo != wire.StaleInfoNone {
			deadShares++
		}

		shareTargetInt := blockchain.CompactToBig(current.Share.ShareInfo.Bits)
		if shareTargetInt.Sign() <= 0 {
			current = current.Previous
			continue
		}
		shareTargetFloat := new(big.Float).SetInt(shareTargetInt)

		// work = maxTarget / shareTarget. Use floating point math.
		workOfShare := new(big.Float).Quo(maxTargetFloat, shareTargetFloat)
		totalWork.Add(totalWork, workOfShare)
		
		current = current.Previous
	}

	stats.SharesDead = deadShares
	if sharesInWindow > 0 {
		stats.Efficiency = 100 * (1 - (float64(stats.SharesDead) / float64(sharesInWindow)))
	} else if stats.SharesOrphan == 0 && stats.SharesTotal > 0 {
		stats.Efficiency = 100.0
	}

	totalWorkFloat, _ := totalWork.Float64()
	if lookbackDuration.Seconds() > 0 && totalWorkFloat > 0 {
		// Hashrate = (TotalWork * 2^32) / TimeInSeconds
		pow32 := math.Pow(2, 32)
		stats.PoolHashrate = (totalWorkFloat * pow32) / lookbackDuration.Seconds()
	}


	// Calculate Time to Block based on pool's hashrate and network difficulty
	if stats.PoolHashrate > 0 && stats.NetworkDifficulty > 0 {
		// Time To Block (in seconds) = (Network Difficulty * 2^32) / Pool Hashrate
		pow32 := math.Pow(2, 32)
		netWorkTerm := stats.NetworkDifficulty * pow32
		stats.TimeToBlock = netWorkTerm / stats.PoolHashrate
	}

	stats.CurrentPayout = 0.0
	return stats
}

func (sc *ShareChain) bestOrphan() (hash string, info *orphanInfo) {
	for h, inf := range sc.disconnectedShares {
		if info == nil || inf.share.ShareInfo.AbsHeight > info.share.ShareInfo.AbsHeight {
			hash, info = h, inf
		}
	}
	return
}

func (sc *ShareChain) attachChildren(parentHash string, parentCS *ChainShare) {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	if kids, ok := sc.AllSharesByPrev[parentHash]; ok {
		for _, kidShare := range kids {
			kHash := kidShare.Hash.String()
			if _, ok := sc.disconnectedShares[kHash]; ok {
				logging.Infof(" ↳ Attaching waiting child %s to parent %s", kHash[:12], parentHash[:12])
				cs := &ChainShare{Share: kidShare, Previous: parentCS}
				parentCS.Next = cs
				sc.AllShares[kHash] = cs
				delete(sc.disconnectedShares, kHash)
				sc.attachChildren(kHash, cs)
			}
		}
		delete(sc.AllSharesByPrev, parentHash)
	}
}

func (sc *ShareChain) Resolve(skipCommit bool) {
	logging.Debugf("Resolving sharechain with %d disconnected shares", len(sc.disconnectedShares))
	sc.allSharesLock.Lock()
	if sc.Tip == nil && len(sc.disconnectedShares) > 0 {
		h, o := sc.bestOrphan()
		if o != nil {
			cs := &ChainShare{Share: o.share}
			sc.AllShares[h] = cs
			sc.Tip, sc.Tail = cs, cs
			delete(sc.disconnectedShares, h)
			logging.Warnf("Forcing genesis from orphan %s at height %d", h[:12], o.share.ShareInfo.AbsHeight)
			sc.allSharesLock.Unlock()
			sc.attachChildren(h, cs)
			sc.allSharesLock.Lock()
		}
	}
	sc.allSharesLock.Unlock()

	passCount := 0
	for changedInLoop := true; changedInLoop; {
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
				sc.allSharesLock.Lock()
				if parent.Next != nil {
					logging.Warnf("Parent share %s already has a next share. Ignoring link for %s.", parent.Share.Hash.String()[:12], newChainShare.Share.Hash.String()[:12])
				} else {
					parent.Next = newChainShare
				}
				sc.AllShares[hashStr] = newChainShare
				if sc.Tip == parent {
					sc.Tip = newChainShare
					logging.Infof("Accepted share %s becomes new tip. Height: %d", hashStr[:12], orphan.share.ShareInfo.AbsHeight)
				}
				sc.allSharesLock.Unlock()
				sc.attachChildren(hashStr, newChainShare)
				delete(sc.disconnectedShares, hashStr)
				changedInLoop = true
			}
		}
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
								logging.Debugf("Requesting missing parent %s for share %s", hashStr[:12], orphan.share.ShareInfo.ShareData.PreviousShareHash.String()[:12])
								sc.requestedParents[hashStr] = time.Now()
							default:
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
	sc.allSharesLock.Lock()
	current := sc.Tail
	for current != nil {
		shares = append(shares, *current.Share)
		current = current.Next
	}
	sc.allSharesLock.Unlock()
	return wire.WriteShares(f, shares)
}

func (sc *ShareChain) Load() error {
	logging.Debugf("SHARECHAIN/LOAD: Opening shares.dat...")
	f, err := os.Open("shares.dat")
	if err != nil {
		if os.IsNotExist(err) {
			logging.Debugf("SHARECHAIN/LOAD: shares.dat not found, starting with empty chain.")
			return nil
		}
		return err
	}
	defer f.Close()
	logging.Debugf("SHARECHAIN/LOAD: Reading shares from file...")
	shares, err := wire.ReadShares(f)
	if err != nil && err.Error() != "EOF" {
		return fmt.Errorf("error reading shares: %v", err)
	}
	logging.Debugf("SHARECHAIN/LOAD: Read %d shares from shares.dat.", len(shares))
	logging.Debugf("SHARECHAIN/LOAD: Adding loaded shares to the chain as trusted...")
	sc.AddShares(shares, true)
	logging.Debugf("SHARECHAIN/LOAD: Resolving loaded share chain...")
	sc.Resolve(true)
	logging.Debugf("SHARECHAIN/LOAD: Finished resolving.")
	return nil
}
