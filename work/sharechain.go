package work

import (
	"fmt"
	"math/big"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/CADMonkey21/p2pool-go-VTC/wire"
)

// Each unit of VertHash difficulty represents 2^32 hashes.
const hashrateConstant = 4294967296 // 2^32

const (
	maxResolvePasses = 100
)

// PeerManager defines the interface the ShareChain needs to communicate back to the P2P manager.
type PeerManager interface {
	Broadcast(msg wire.P2PoolMessage)
}

type orphanInfo struct {
	share   *wire.Share
	age     int
	isLocal bool
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
	SharesChannel       chan []wire.Share
	NeedShareChannel    chan *chainhash.Hash
	FoundBlockChan      chan *wire.Share
	Tip                 *ChainShare
	Tail                *ChainShare
	AllShares           map[string]*ChainShare
	AllSharesByPrev     map[string][]*wire.Share
	disconnectedShares  map[string]*orphanInfo
	requestedParents    map[string]time.Time
	rpcClient           *rpc.Client
	pm                  PeerManager // Reference to the peer manager
	disconnectedShareLock sync.Mutex
	allSharesLock       sync.Mutex
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
		FoundBlockChan:        make(chan *wire.Share, 10),
		rpcClient:             client,
	}
	go sc.ReadShareChan()
	return sc
}

func (sc *ShareChain) SetPeerManager(pm PeerManager) {
	sc.pm = pm
}

func (sc *ShareChain) ReadShareChan() {
	for s := range sc.SharesChannel {
		sc.AddShares(s, false)
	}
}

func (sc *ShareChain) AddShares(s []wire.Share, trusted bool) {
	sc.disconnectedShareLock.Lock()
	defer sc.disconnectedShareLock.Unlock()
	logging.Debugf("SHARECHAIN/AddShares: Attempting to add %d new shares.", len(s))

	sort.Slice(s, func(i, j int) bool {
		return s[i].ShareInfo.AbsHeight < s[j].ShareInfo.AbsHeight
	})

	var newSharesAdded bool
	for i := range s {
		share := &s[i]

		if share.Hash == nil {
			share.CalculateHashes()
		}
		shareHashStr := share.Hash.String()

		origin := "PEER"
		if trusted {
			origin = "LOCAL"
		}
		logging.Debugf("SHARECHAIN/AddShares: Processing share %s from %s.", shareHashStr[:12], origin)

		sc.allSharesLock.Lock()
		_, existsInChain := sc.AllShares[shareHashStr]
		sc.allSharesLock.Unlock()

		if existsInChain {
			logging.Debugf("SHARECHAIN/AddShares: Share %s already in the main chain. Ignoring.", shareHashStr[:12])
			continue
		}

		if _, existsAsOrphan := sc.disconnectedShares[shareHashStr]; existsAsOrphan {
			logging.Debugf("SHARECHAIN/AddShares: Share %s already in the orphan list. Ignoring.", shareHashStr[:12])
			continue
		}

		if !trusted {
			valid, reason := share.IsValid()
			if !valid {
				logging.Warnf("SHARECHAIN/AddShares: Discarding INVALID share %s from %s. Reason: %s", shareHashStr[:12], origin, reason)
				continue
			}
		}

		logging.Debugf("SHARECHAIN/AddShares: Adding share %s to disconnected list (new orphan).", shareHashStr[:12])
		sc.disconnectedShares[shareHashStr] = &orphanInfo{share: share, age: 0, isLocal: trusted}
		newSharesAdded = true

		if prev := share.ShareInfo.ShareData.PreviousShareHash; prev != nil {
			prevStr := prev.String()
			logging.Debugf("SHARECHAIN/AddShares: Share %s lists previous share %s.", shareHashStr[:12], prevStr[:12])
			sc.AllSharesByPrev[prevStr] = append(sc.AllSharesByPrev[prevStr], share)
		} else {
			logging.Warnf("SHARECHAIN/AddShares: Share %s has no previous share hash!", shareHashStr[:12])
		}
	}

	if newSharesAdded {
		go sc.Resolve(false)
	} else {
		logging.Debugf("SHARECHAIN/AddShares: No new shares were added to the orphan list in this batch.")
	}
}

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
	stats := ChainStats{}
	sc.disconnectedShareLock.Lock()
	stats.SharesOrphan = len(sc.disconnectedShares)
	sc.disconnectedShareLock.Unlock()

	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	stats.SharesTotal = len(sc.AllShares)

	netInfo, err := sc.rpcClient.GetMiningInfo()
	if err != nil {
		logging.Warnf("Could not get network info from daemon: %v", err)
	} else {
		stats.NetworkHashrate = netInfo.NetworkHashPS
		stats.NetworkDifficulty = netInfo.Difficulty
	}

	lookbackDuration := 30 * time.Minute
	startTime := time.Now().Add(-lookbackDuration)

	totalDifficulty := new(big.Int)
	var deadShares, sharesInWindow int

	current := sc.Tip
	for current != nil && time.Unix(int64(current.Share.ShareInfo.Timestamp), 0).After(startTime) {
		sharesInWindow++
		if current.Share.ShareInfo.ShareData.StaleInfo != wire.StaleInfoNone {
			deadShares++
		}

		shareTarget := blockchain.CompactToBig(current.Share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			current = current.Previous
			continue
		}
		maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
		difficulty := new(big.Int).Div(maxTarget, shareTarget)
		totalDifficulty.Add(totalDifficulty, difficulty)

		current = current.Previous
	}

	stats.SharesDead = deadShares
	if sharesInWindow > 0 {
		stats.Efficiency = 100 * (1 - (float64(stats.SharesDead) / float64(sharesInWindow)))
	} else if stats.SharesOrphan == 0 && stats.SharesTotal > 0 {
		stats.Efficiency = 100.0
	}

	if totalDifficulty.Sign() > 0 {
		totalDifficultyFloat := new(big.Float).SetInt(totalDifficulty)
		hashrateFloat := new(big.Float).Quo(new(big.Float).Mul(totalDifficultyFloat, big.NewFloat(hashrateConstant)), big.NewFloat(lookbackDuration.Seconds()))
		stats.PoolHashrate, _ = hashrateFloat.Float64()
	}

	if stats.PoolHashrate > 0 && stats.NetworkDifficulty > 0 {
		stats.TimeToBlock = (stats.NetworkDifficulty * hashrateConstant) / stats.PoolHashrate
	}

	stats.CurrentPayout = 0.0
	return stats
}

func (sc *ShareChain) findGenesisOrphan() (hash string, info *orphanInfo) {
	var bestOrphan *orphanInfo
	var bestOrphanHash string

	for h, i := range sc.disconnectedShares {
		prev := i.share.ShareInfo.ShareData.PreviousShareHash
		if prev == nil {
			if bestOrphan == nil || i.share.ShareInfo.AbsHeight < bestOrphan.share.ShareInfo.AbsHeight {
				bestOrphan = i
				bestOrphanHash = h
			}
			continue
		}
		if _, parentIsOrphan := sc.disconnectedShares[prev.String()]; !parentIsOrphan {
			if bestOrphan == nil || i.share.ShareInfo.AbsHeight < bestOrphan.share.ShareInfo.AbsHeight {
				bestOrphan = i
				bestOrphanHash = h
			}
		}
	}
	return bestOrphanHash, bestOrphan
}

func (sc *ShareChain) getChainWeight(start *wire.Share) (*big.Int, *wire.Share) {
	weight := big.NewInt(0)
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
	current := start
	var tip *wire.Share

	for {
		if current == nil {
			break
		}

		shareTarget := blockchain.CompactToBig(current.ShareInfo.Bits)
		if shareTarget.Sign() > 0 {
			difficulty := new(big.Int).Div(maxTarget, shareTarget)
			weight.Add(weight, difficulty)
		}

		tip = current
		nextHash := current.Hash.String()
		children, ok := sc.AllSharesByPrev[nextHash]
		if !ok || len(children) == 0 {
			break
		}

		if len(children) > 1 {
			bestWeight := big.NewInt(0)
			var bestTip *wire.Share
			for _, child := range children {
				childWeight, childTip := sc.getChainWeight(child)
				if childWeight.Cmp(bestWeight) > 0 {
					bestWeight = childWeight
					bestTip = childTip
				}
			}
			weight.Add(weight, bestWeight)
			tip = bestTip
			current = nil // End the loop
		} else {
			current = children[0]
		}
	}
	return weight, tip
}

func (sc *ShareChain) attachChildren(parentHash string, parentCS *ChainShare) {
	sc.allSharesLock.Lock()

	newlyLinkedChildren := make([]*ChainShare, 0)
	if kids, ok := sc.AllSharesByPrev[parentHash]; ok {
		for _, kidShare := range kids {
			kHash := kidShare.Hash.String()
			if _, isOrphan := sc.disconnectedShares[kHash]; isOrphan {
				logging.Infof(" ↳ Attaching waiting child %s to parent %s", kHash[:12], parentHash[:12])
				cs := &ChainShare{Share: kidShare, Previous: parentCS}
				parentCS.Next = cs
				sc.AllShares[kHash] = cs
				delete(sc.disconnectedShares, kHash)
				newlyLinkedChildren = append(newlyLinkedChildren, cs)
			}
		}
		delete(sc.AllSharesByPrev, parentHash)
	}

	sc.allSharesLock.Unlock()

	for _, childCS := range newlyLinkedChildren {
		sc.attachChildren(childCS.Share.Hash.String(), childCS)
	}
}

func (sc *ShareChain) Resolve(loading bool) {
	sc.disconnectedShareLock.Lock()
	defer sc.disconnectedShareLock.Unlock()

	logging.Debugf("SHARECHAIN/Resolve: Starting resolution with %d disconnected shares.", len(sc.disconnectedShares))
	sc.allSharesLock.Lock()
	if sc.Tip == nil && len(sc.disconnectedShares) > 0 {
		h, o := sc.findGenesisOrphan()
		if o != nil {
			cs := &ChainShare{Share: o.share}
			sc.AllShares[h] = cs
			sc.Tip, sc.Tail = cs, cs
			delete(sc.disconnectedShares, h)
			logging.Warnf("SHARECHAIN/Resolve: No tip found. Forcing genesis from oldest orphan: %s at height %d.", h[:12], o.share.ShareInfo.AbsHeight)
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
			logging.Warnf("SHARECHAIN/Resolve: Exceeded max resolution passes (%d). Breaking loop to prevent getting stuck.", maxResolvePasses)
			break
		}
		changedInLoop = false
		for hashStr, orphan := range sc.disconnectedShares {
			if orphan.share.ShareInfo.ShareData.PreviousShareHash == nil {
				logging.Warnf("SHARECHAIN/Resolve: Orphan %s has no previous hash. Cannot link.", hashStr[:12])
				continue
			}
			prevHashStr := orphan.share.ShareInfo.ShareData.PreviousShareHash.String()
			sc.allSharesLock.Lock()
			parent, exists := sc.AllShares[prevHashStr]
			sc.allSharesLock.Unlock()

			if exists {
				origin := "PEER"
				if orphan.isLocal {
					origin = "LOCAL"
				}
				newChainShare := &ChainShare{Share: orphan.share, Previous: parent}

				sc.allSharesLock.Lock()
				if parent.Next != nil {
					existingChild := parent.Next.Share
					newChild := orphan.share
					existingWeight, _ := sc.getChainWeight(existingChild)
					newWeight, newTipShare := sc.getChainWeight(newChild)

					logging.Warnf("SHARECHAIN/Resolve: Fork detected at parent %s. Existing child: %s (weight %s), New child: %s (weight %s)",
						parent.Share.Hash.String()[:12], existingChild.Hash.String()[:12], existingWeight.String(), newChild.Hash.String()[:12], newWeight.String())

					if newWeight.Cmp(existingWeight) > 0 {
						logging.Warnf("SHARECHAIN/Resolve: New chain is stronger. Reorganizing.")
						current := parent.Next
						parent.Next = nil
						for current != nil {
							delete(sc.AllShares, current.Share.Hash.String())
							sc.disconnectedShares[current.Share.Hash.String()] = &orphanInfo{share: current.Share, age: 0, isLocal: false}
							current = current.Next
						}
						parent.Next = newChainShare
						sc.AllShares[hashStr] = newChainShare
						delete(sc.disconnectedShares, hashStr)
						if newTipShare != nil {
							if tipCS, ok := sc.AllShares[newTipShare.Hash.String()]; ok {
								sc.Tip = tipCS
							}
						}
						changedInLoop = true
					} else {
						logging.Warnf("SHARECHAIN/Resolve: Existing chain is stronger. Ignoring new share.")
					}
				} else {
					logging.Infof("SHARECHAIN/Resolve: Found parent %s for [%s] orphan %s. Linking now.", prevHashStr[:12], origin, hashStr[:12])
					parent.Next = newChainShare
					sc.AllShares[hashStr] = newChainShare
					if sc.Tip == parent {
						sc.Tip = newChainShare
						logging.Infof("SHARECHAIN/Resolve: Share %s is the new tip. New Tip Height: %d.", hashStr[:12], newChainShare.Share.ShareInfo.AbsHeight)
					}
					delete(sc.disconnectedShares, hashStr)
					changedInLoop = true

					if sc.pm != nil {
						sc.pm.Broadcast(&wire.MsgShares{Shares: []wire.Share{*newChainShare.Share}})
					}
				}
				sc.allSharesLock.Unlock()

				if changedInLoop {
					sc.attachChildren(hashStr, newChainShare)
				}

			} else {
				logging.Debugf("SHARECHAIN/Resolve: Parent %s for orphan %s not found in main chain.", prevHashStr[:12], hashStr[:12])
			}
		}

		if !changedInLoop {
			logging.Debugf("SHARECHAIN/Resolve: No shares were linked in pass #%d. Checking for missing parents to request.", passCount)
			for hashStr, orphan := range sc.disconnectedShares {
				if !loading {
					orphan.age++
					if orphan.age > (config.Active.PPLNSWindow * 3) {
						logging.Warnf("SHARECHAIN/Resolve: Purging stale orphan %s after %d checks.", hashStr[:12], orphan.age)
						prev := orphan.share.ShareInfo.ShareData.PreviousShareHash
						if prev != nil {
							key := prev.String()
							if kids, ok := sc.AllSharesByPrev[key]; ok {
								k := orphan.share.Hash.String()
								out := kids[:0]
								for _, s := range kids {
									if s.Hash.String() != k {
										out = append(out, s)
									}
								}
								if len(out) == 0 {
									delete(sc.AllSharesByPrev, key)
								} else {
									sc.AllSharesByPrev[key] = out
								}
							}
						}
						delete(sc.disconnectedShares, hashStr)
						continue
					}
				}

				prevHash := orphan.share.ShareInfo.ShareData.PreviousShareHash
				if prevHash != nil {
					if lastReq, ok := sc.requestedParents[prevHash.String()]; !ok || time.Since(lastReq) > 30*time.Second {
						logging.Debugf("SHARECHAIN/Resolve: Requesting missing parent %s for orphan %s.", prevHash.String()[:12], hashStr[:12])
						sc.NeedShareChannel <- prevHash
						sc.requestedParents[prevHash.String()] = time.Now()
					}
				}
			}
		}
	}
	logging.Debugf("SHARECHAIN/Resolve: Finished resolution after %d passes. %d orphans remaining.", passCount, len(sc.disconnectedShares))
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
	logging.Debugf("SHARECHAIN/LOAD: Read %d shares from shares.dat. Reconstructing chain...", len(shares))
	if len(shares) == 0 {
		return nil
	}

	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	// Step 1: Create ChainShare wrappers for all shares and index them by hash.
	for i := range shares {
		share := &shares[i]
		if share.Hash == nil {
			share.CalculateHashes()
		}
		sc.AllShares[share.Hash.String()] = &ChainShare{Share: share}
	}

	// Step 2: Link the ChainShare objects together.
	var bestTip *ChainShare
	for _, cs := range sc.AllShares {
		if cs.Share.ShareInfo.ShareData.PreviousShareHash != nil {
			prevHashStr := cs.Share.ShareInfo.ShareData.PreviousShareHash.String()
			if parentCS, ok := sc.AllShares[prevHashStr]; ok {
				cs.Previous = parentCS
				parentCS.Next = cs
			}
		}
	}

	// Step 3: Find the true tip (the one with no 'Next' pointer).
	for _, cs := range sc.AllShares {
		if cs.Next == nil {
			if bestTip == nil {
				bestTip = cs
			} else {
				// Compare weights to find the true tip in case of fragmentation
				currentTipWeight, _ := sc.getChainWeight(bestTip.Share)
				newTipWeight, _ := sc.getChainWeight(cs.Share)
				if newTipWeight.Cmp(currentTipWeight) > 0 {
					bestTip = cs
				}
			}
		}
	}
	sc.Tip = bestTip

	// Step 4: Find the tail by traversing back from the tip.
	if sc.Tip != nil {
		current := sc.Tip
		for current.Previous != nil {
			current = current.Previous
		}
		sc.Tail = current
		// FIX: Corrected the field access from .Info to .ShareInfo
		logging.Infof("SHARECHAIN/LOAD: Reconstructed chain from file. Tip: %s at height %d. Total shares: %d", sc.Tip.Share.Hash.String()[:12], sc.Tip.Share.ShareInfo.AbsHeight, len(sc.AllShares))
	} else if len(sc.AllShares) > 0 {
		logging.Warnf("SHARECHAIN/LOAD: Loaded %d shares but could not determine a chain tip. The chain may be fragmented.", len(sc.AllShares))
	} else {
		logging.Infof("SHARECHAIN/LOAD: shares.dat was empty or contained no valid shares.")
	}

	return nil
}

func (sc *ShareChain) GetProjectedPayouts(limit int) (map[string]float64, error) {
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	tip := sc.Tip
	if tip == nil {
		return nil, fmt.Errorf("sharechain has no tip, cannot calculate payouts")
	}

	totalPayout := uint64(12.5 * 1e8) // 12.5 VTC in satoshis
	if tip.Share != nil && tip.Share.ShareInfo.ShareData.Subsidy > 0 {
		totalPayout = tip.Share.ShareInfo.ShareData.Subsidy
	}

	feePercentage := config.Active.Fee / 100.0
	amountToDistribute := totalPayout - uint64(float64(totalPayout)*feePercentage)

	windowSize := config.Active.PPLNSWindow
	if windowSize <= 0 {
		return nil, fmt.Errorf("PPLNS window size is not configured or is invalid")
	}

	payoutShares := make([]*wire.Share, 0, windowSize)
	current := tip
	for i := 0; i < windowSize && current != nil; i++ {
		payoutShares = append(payoutShares, current.Share)
		current = current.Previous
	}

	payouts := make(map[string]uint64)
	totalWorkInWindow := new(big.Int)
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	for _, share := range payoutShares {
		shareTarget := blockchain.CompactToBig(share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			continue
		}
		difficulty := new(big.Int).Div(new(big.Int).Set(maxTarget), shareTarget)
		totalWorkInWindow.Add(totalWorkInWindow, difficulty)
	}

	if totalWorkInWindow.Sign() <= 0 {
		return make(map[string]float64), nil
	}

	for _, share := range payoutShares {
		if share.ShareInfo.ShareData.PubKeyHash == nil || len(share.ShareInfo.ShareData.PubKeyHash) == 0 {
			continue
		}

		address, err := bech32.Encode("vtc", append([]byte{share.ShareInfo.ShareData.PubKeyHashVersion}, share.ShareInfo.ShareData.PubKeyHash...))
		if err != nil {
			continue
		}

		shareTarget := blockchain.CompactToBig(share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			continue
		}
		difficulty := new(big.Int).Div(new(big.Int).Set(maxTarget), shareTarget)

		payoutAmount := new(big.Int).Mul(big.NewInt(int64(amountToDistribute)), difficulty)
		payoutAmount.Div(payoutAmount, totalWorkInWindow)

		payouts[address] += payoutAmount.Uint64()
	}

	finalPayouts := make(map[string]float64)
	for addr, amountSatoshis := range payouts {
		finalPayouts[addr] = float64(amountSatoshis) / 100000000.0
	}

	return finalPayouts, nil
}

func (sc *ShareChain) GetProjectedPayoutForAddress(address string) (float64, error) {
	allPayouts, err := sc.GetProjectedPayouts(0)
	if err != nil {
		return 0, err
	}
	return allPayouts[address], nil
}
