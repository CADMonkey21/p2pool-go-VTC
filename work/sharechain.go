package work

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-vtc/config"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	"github.com/CADMonkey21/p2pool-go-vtc/rpc"
	"github.com/CADMonkey21/p2pool-go-vtc/wire"
)

// Each unit of VertHash difficulty represents 2^24 hashes.
const hashrateConstant = 16777216 // 2^24

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
	FoundBlockChan        chan *wire.Share
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
		FoundBlockChan:        make(chan *wire.Share, 10),
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
	logging.Debugf("SHARECHAIN/AddShares: Attempting to add %d new shares.", len(s))

	var newSharesAdded bool
	for i := range s {
		share := &s[i]
		shareHashStr := share.Hash.String()

		logging.Debugf("SHARECHAIN/AddShares: Processing share %s.", shareHashStr[:12])

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
				logging.Warnf("SHARECHAIN/AddShares: Discarding INVALID share %s. Reason: %s", shareHashStr[:12], reason)
				continue
			}
		}

		// Block checking logic
		if share.MinHeader.PreviousBlock != nil {
			headerInfo, err := sc.rpcClient.GetBlockHeader(share.MinHeader.PreviousBlock)
			if err == nil {
				bitsBytes, err := hex.DecodeString(headerInfo.Bits)
				if err != nil || len(bitsBytes) != 4 {
					logging.Warnf("Bad bits from daemon: %v", headerInfo.Bits)
				} else {
					networkBits := binary.LittleEndian.Uint32(bitsBytes)
					networkTarget := blockchain.CompactToBig(networkBits)

					if share.POWHash != nil {
						sharePOWInt := new(big.Int).SetBytes(share.POWHash.CloneBytes())
						if sharePOWInt.Cmp(networkTarget) <= 0 {
							logging.Infof("!!!! PEER BLOCK DETECTED !!!! Share %s is a valid block!", share.Hash.String()[:12])
							select {
							case sc.FoundBlockChan <- share:
							default:
								logging.Warnf("FoundBlockChan is full, dropping block notification.")
							}
						}
					}
				}
			}
		}

		logging.Debugf("SHARECHAIN/AddShares: Adding share %s to disconnected list (new orphan).", shareHashStr[:12])
		sc.disconnectedShares[shareHashStr] = &orphanInfo{share: share, age: 0}
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
		sc.Resolve(false)
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
	sc.allSharesLock.Lock()
	defer sc.allSharesLock.Unlock()

	stats := ChainStats{}
	stats.SharesTotal = len(sc.AllShares)
	stats.SharesOrphan = len(sc.disconnectedShares)

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
		stats.PoolHashrate = (totalWorkFloat * hashrateConstant) / lookbackDuration.Seconds()
	}

	if stats.PoolHashrate > 0 && stats.NetworkDifficulty > 0 {
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
	logging.Debugf("SHARECHAIN/attachChildren: Attaching children to %s", parentHash[:12])

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
	logging.Debugf("SHARECHAIN/Resolve: Starting resolution with %d disconnected shares.", len(sc.disconnectedShares))
	sc.allSharesLock.Lock()
	if sc.Tip == nil && len(sc.disconnectedShares) > 0 {
		h, o := sc.bestOrphan()
		if o != nil {
			cs := &ChainShare{Share: o.share}
			sc.AllShares[h] = cs
			sc.Tip, sc.Tail = cs, cs
			delete(sc.disconnectedShares, h)
			logging.Warnf("SHARECHAIN/Resolve: No tip found. Forcing genesis from best orphan: %s at height %d.", h[:12], o.share.ShareInfo.AbsHeight)
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
				logging.Infof("SHARECHAIN/Resolve: Found parent %s for orphan %s. Linking now.", prevHashStr[:12], hashStr[:12])
				newChainShare := &ChainShare{Share: orphan.share, Previous: parent}

				sc.allSharesLock.Lock()
				if parent.Next != nil {
					logging.Warnf("SHARECHAIN/Resolve: Parent %s already has a 'next' share. This indicates a potential fork or duplicate share. Current orphan %s will not be linked to this parent.", parent.Share.Hash.String()[:12], hashStr[:12])
				} else {
					parent.Next = newChainShare
					sc.AllShares[hashStr] = newChainShare
					if sc.Tip == parent {
						sc.Tip = newChainShare
						logging.Infof("SHARECHAIN/Resolve: Share %s is the new tip. New Tip Height: %d.", hashStr[:12], newChainShare.Share.ShareInfo.AbsHeight)
					}
					delete(sc.disconnectedShares, hashStr)
					changedInLoop = true
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
				orphan.age++
				if orphan.age > maxOrphanAge {
					logging.Warnf("SHARECHAIN/Resolve: Purging stale orphan %s after %d checks.", hashStr[:12], orphan.age)
					delete(sc.disconnectedShares, hashStr)
				} else {
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
	logging.Debugf("SHARECHAIN/LOAD: Read %d shares from shares.dat.", len(shares))
	logging.Debugf("SHARECHAIN/LOAD: Adding loaded shares to the chain as trusted...")
	sc.AddShares(shares, true)
	logging.Debugf("SHARECHAIN/LOAD: Resolving loaded share chain...")
	sc.Resolve(true)
	logging.Debugf("SHARECHAIN/LOAD: Finished resolving.")
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
		return nil, fmt.Errorf("total work in PPLNS window is zero, cannot project payment")
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
