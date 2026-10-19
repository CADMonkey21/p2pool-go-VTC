package work

import (
	"encoding/json"
	"math/big"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

const maxShareAge = 72 * time.Hour

type PoolStats struct {
	PoolHashrate      float64 `json:"pool_hashrate"`
	NetworkHashrate   float64 `json:"network_hashrate"`
	NetworkDifficulty float64 `json:"network_difficulty"`
	SharesTotal       int     `json:"shares_total"`
	SharesOrphan      int     `json:"shares_orphan"`
	SharesDead        int     `json:"shares_dead"`
	Efficiency        float64 `json:"efficiency"`
	TimeToBlock       float64 `json:"time_to_block_seconds"`
}

type NetworkMinerStats struct {
	Address     string
	Hashrate    float64
	ShareDiff   float64
	ShareCount  int
	LastSeen    int64
}

type ShareChain struct {
	shares            map[string]*Share
	mutex             sync.RWMutex
	rpcClient         *rpc.Client
	genesisHash       *chainhash.Hash
	TipHash           *chainhash.Hash
	FoundBlockChan    chan *Share
	poolHashrate      float64
	networkHashrate   float64
	networkDifficulty float64
	poolStatsMutex    sync.RWMutex
}

func NewShareChain(rpcClient *rpc.Client) *ShareChain {
	sc := &ShareChain{
		shares:         make(map[string]*Share),
		rpcClient:      rpcClient,
		FoundBlockChan: make(chan *Share, 100),
	}
	nullHash, _ := chainhash.NewHash(make([]byte, 32))
	sc.genesisHash = nullHash
	sc.TipHash = nullHash
	go sc.maintenanceLoop()
	return sc
}

func (sc *ShareChain) maintenanceLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		sc.PruneOldShares()
		sc.updateStats()
	}
}

func (sc *ShareChain) GetShare(hashStr string) *Share {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.shares[hashStr]
}

func (sc *ShareChain) HasShare(hashStr string) bool {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	_, exists := sc.shares[hashStr]
	return exists
}

func (sc *ShareChain) AddShares(newShares []Share) int {
	sc.mutex.Lock()
	added := 0
	for i := range newShares {
		s := newShares[i]
		if s.Hash == nil {
			_ = s.CalculateHashes()
		}
		if s.Hash == nil {
			continue
		}
		hStr := s.Hash.String()
		if _, exists := sc.shares[hStr]; !exists {
			shareCopy := s
			sc.shares[hStr] = &shareCopy
			added++
			sc.updateTip(&shareCopy)
		}
	}
	sc.mutex.Unlock()

	if added > 0 {
		sc.updateStats()
	}
	return added
}

func (sc *ShareChain) updateTip(s *Share) {
	if s.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
		return
	}
	if sc.TipHash == nil || sc.TipHash.IsEqual(sc.genesisHash) {
		sc.TipHash = s.Hash
		return
	}

	currentTip := sc.shares[sc.TipHash.String()]
	if currentTip == nil {
		sc.TipHash = s.Hash
		return
	}

	// Advance TipHash if the new share builds directly on currentTip, has a higher VTC block height,
	// or is newer at the same VTC block height.
	if s.ShareInfo.ShareData.PreviousShareHash != nil && s.ShareInfo.ShareData.PreviousShareHash.IsEqual(sc.TipHash) {
		sc.TipHash = s.Hash
		return
	}
	if s.ShareInfo.AbsHeight > currentTip.ShareInfo.AbsHeight {
		sc.TipHash = s.Hash
		return
	}
	if s.ShareInfo.AbsHeight == currentTip.ShareInfo.AbsHeight && s.ShareInfo.Timestamp >= currentTip.ShareInfo.Timestamp {
		sc.TipHash = s.Hash
	}
}

func (sc *ShareChain) PruneOldShares() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()
	cutoff := time.Now().Add(-maxShareAge).Unix()
	pruned := 0
	for hStr, s := range sc.shares {
		if int64(s.ShareInfo.Timestamp) < cutoff {
			delete(sc.shares, hStr)
			pruned++
		}
	}
	if pruned > 0 {
		logging.Debugf("ShareChain: Pruned %d shares older than 72 hours", pruned)
	}
}

func (sc *ShareChain) Load() error {
	path := "sharechain.json"
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var fileShares []Share
	if err := json.Unmarshal(b, &fileShares); err != nil {
		return err
	}
	// Sort ascending by timestamp before adding so TipHash lands on the true latest share
	sort.Slice(fileShares, func(i, j int) bool {
		return fileShares[i].ShareInfo.Timestamp < fileShares[j].ShareInfo.Timestamp
	})
	sc.AddShares(fileShares)
	logging.Infof("Loaded %d shares from disk", len(fileShares))
	return nil
}

func (sc *ShareChain) Commit() error {
	sc.mutex.RLock()
	sharesToSave := make([]Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		sharesToSave = append(sharesToSave, *s)
	}
	sc.mutex.RUnlock()

	path := "sharechain.json"
	b, err := json.MarshalIndent(sharesToSave, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func (sc *ShareChain) GetAllShares() []Share {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	list := make([]Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ShareInfo.Timestamp < list[j].ShareInfo.Timestamp
	})
	return list
}

func (sc *ShareChain) GetSharesForPayout(blockFindShareHash *chainhash.Hash, windowSize int) ([]*Share, error) {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	var payoutShares []*Share
	visited := make(map[string]bool)

	if blockFindShareHash != nil {
		currentHash := blockFindShareHash.String()
		for len(payoutShares) < windowSize {
			if visited[currentHash] {
				break
			}
			visited[currentHash] = true

			share, exists := sc.shares[currentHash]
			if !exists {
				break
			}
			if share.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
				payoutShares = append(payoutShares, share)
			}
			if share.ShareInfo.ShareData.PreviousShareHash == nil || share.ShareInfo.ShareData.PreviousShareHash.IsEqual(sc.genesisHash) {
				break
			}
			currentHash = share.ShareInfo.ShareData.PreviousShareHash.String()
		}
	}

	// If parent links had a gap (e.g. concurrent shares at same block height), fill remaining window
	// deterministically from the most recent valid shares in the ShareChain so payouts never break.
	if len(payoutShares) < windowSize && len(sc.shares) > len(payoutShares) {
		allValid := make([]*Share, 0, len(sc.shares))
		for hStr, s := range sc.shares {
			if !visited[hStr] && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
				allValid = append(allValid, s)
			}
		}
		sort.Slice(allValid, func(i, j int) bool {
			if allValid[i].ShareInfo.Timestamp == allValid[j].ShareInfo.Timestamp {
				return allValid[i].Hash.String() > allValid[j].Hash.String()
			}
			return allValid[i].ShareInfo.Timestamp > allValid[j].ShareInfo.Timestamp
		})
		for _, s := range allValid {
			if len(payoutShares) >= windowSize {
				break
			}
			payoutShares = append(payoutShares, s)
		}
	}

	return payoutShares, nil
}

func DecodeShareAddress(s *Share) string {
	if s == nil || len(s.ShareInfo.ShareData.PubKeyHash) == 0 {
		return ""
	}
	if s.ShareInfo.ShareData.PubKeyHashVersion == 0x00 {
		converted, err := bech32.ConvertBits(s.ShareInfo.ShareData.PubKeyHash, 8, 5, true)
		if err != nil {
			return ""
		}
		hrp := "vtc"
		if config.Active.Testnet {
			hrp = "tvtc"
		}
		addr, _ := bech32.Encode(hrp, append([]byte{s.ShareInfo.ShareData.PubKeyHashVersion}, converted...))
		return addr
	}
	return base58.CheckEncode(s.ShareInfo.ShareData.PubKeyHash, s.ShareInfo.ShareData.PubKeyHashVersion)
}

// GetNetworkMiners computes active miner statistics across the entire P2P ShareChain over the last 10 minutes.
func (sc *ShareChain) GetNetworkMiners(window time.Duration) map[string]*NetworkMinerStats {
	sc.mutex.RLock()
	shares := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		shares = append(shares, s)
	}
	sc.mutex.RUnlock()

	now := time.Now().Unix()
	windowSec := window.Seconds()
	if windowSec <= 0 {
		windowSec = 600
	}
	cutoff := now - int64(windowSec)

	maxTarget, _ := new(big.Int).SetString("000000FFFF000000000000000000000000000000000000000000000000000000", 16)
	maxTargetFloat := new(big.Float).SetPrec(256).SetInt(maxTarget)

	type minerWork struct {
		workSum   float64
		lastDiff  float64
		lastSeen  int64
		firstSeen int64
		count     int
	}
	workByAddr := make(map[string]*minerWork)

	for _, s := range shares {
		ts := int64(s.ShareInfo.Timestamp)
		if ts < cutoff || s.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
			continue
		}
		addr := DecodeShareAddress(s)
		if addr == "" {
			continue
		}
		if s.Target == nil || s.Target.Sign() <= 0 {
			continue
		}
		targetFloat := new(big.Float).SetPrec(256).SetInt(s.Target)
		diffFloatBig := new(big.Float).SetPrec(256).Quo(maxTargetFloat, targetFloat)
		diffFloat, _ := diffFloatBig.Float64()

		mw, exists := workByAddr[addr]
		if !exists {
			mw = &minerWork{
				lastDiff:  diffFloat,
				lastSeen:  ts,
				firstSeen: ts,
			}
			workByAddr[addr] = mw
		}
		mw.workSum += diffFloat
		mw.count++
		if ts >= mw.lastSeen {
			mw.lastSeen = ts
			mw.lastDiff = diffFloat
		}
		if ts < mw.firstSeen {
			mw.firstSeen = ts
		}
	}

	result := make(map[string]*NetworkMinerStats)
	for addr, mw := range workByAddr {
		span := float64(now - mw.firstSeen)
		if span < 30.0 {
			span = 30.0
		}
		if span > windowSec {
			span = windowSec
		}
		result[addr] = &NetworkMinerStats{
			Address:    addr,
			Hashrate:   (mw.workSum * 16777216.0) / span,
			ShareDiff:  mw.lastDiff,
			ShareCount: mw.count,
			LastSeen:   mw.lastSeen,
		}
	}
	return result
}

func (sc *ShareChain) updateStats() {
	sc.mutex.RLock()
	shares := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		shares = append(shares, s)
	}
	sc.mutex.RUnlock()

	now := time.Now().Unix()
	windowSeconds := float64(600) // 10-minute responsive window matching Stratum local rate monitor
	cutoffWindow := now - int64(windowSeconds)
	var workWindow float64
	var earliestShareTime int64 = now

	maxTarget, _ := new(big.Int).SetString("000000FFFF000000000000000000000000000000000000000000000000000000", 16)
	maxTargetFloat := new(big.Float).SetPrec(256).SetInt(maxTarget)

	for _, s := range shares {
		ts := int64(s.ShareInfo.Timestamp)
		if ts > cutoffWindow && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			if s.Target != nil && s.Target.Sign() > 0 {
				targetFloat := new(big.Float).SetPrec(256).SetInt(s.Target)
				diffFloatBig := new(big.Float).SetPrec(256).Quo(maxTargetFloat, targetFloat)
				diffFloat, _ := diffFloatBig.Float64()
				workWindow += diffFloat
				if ts < earliestShareTime {
					earliestShareTime = ts
				}
			}
		}
	}

	effectiveWindow := float64(now - earliestShareTime)
	if effectiveWindow < 10.0 {
		effectiveWindow = 10.0
	}
	if effectiveWindow > windowSeconds {
		effectiveWindow = windowSeconds
	}

	info, err := sc.rpcClient.GetMiningInfo()
	netHash := 0.0
	netDiff := 0.0
	if err == nil {
		netHash = info.NetworkHashPS
		netDiff = info.Difficulty
	}

	sc.poolStatsMutex.Lock()
	calculatedHashrate := (workWindow * 16777216) / effectiveWindow
	sc.poolHashrate = calculatedHashrate

	if err == nil {
		sc.networkHashrate = netHash
		sc.networkDifficulty = netDiff
	}
	sc.poolStatsMutex.Unlock()
}

func (sc *ShareChain) GetStats() PoolStats {
	sc.mutex.RLock()
	total := len(sc.shares)
	orphan := 0
	doa := 0
	for _, s := range sc.shares {
		switch s.ShareInfo.ShareData.StaleInfo {
		case StaleInfoOrphan:
			orphan++
		case StaleInfoDOA:
			doa++
		}
	}
	sc.mutex.RUnlock()

	eff := 100.0
	if total > 0 {
		eff = float64(total-orphan-doa) / float64(total) * 100.0
	}

	sc.poolStatsMutex.RLock()
	pH := sc.poolHashrate
	nH := sc.networkHashrate
	nD := sc.networkDifficulty
	sc.poolStatsMutex.RUnlock()

	ttb := 0.0
	if pH > 0 && nH > 0 {
		ttb = (nH / pH) * 150.0
	}

	return PoolStats{
		PoolHashrate:      pH,
		NetworkHashrate:   nH,
		NetworkDifficulty: nD,
		SharesTotal:       total,
		SharesOrphan:      orphan,
		SharesDead:        doa,
		Efficiency:        eff,
		TimeToBlock:       ttb,
	}
}

func (sc *ShareChain) GetRecentShares(count int) ([]*Share, error) {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	shareList := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		shareList = append(shareList, s)
	}

	sort.Slice(shareList, func(i, j int) bool {
		strA := shareList[i]
		strB := shareList[j]
		if strA == nil || strB == nil {
			return false
		}
		return strA.ShareInfo.Timestamp > strB.ShareInfo.Timestamp
	})

	if count > len(shareList) {
		count = len(shareList)
	}
	return shareList[:count], nil
}

func (sc *ShareChain) GetTipHash() *chainhash.Hash {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.TipHash
}
