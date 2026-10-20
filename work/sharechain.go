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
	Address    string
	Hashrate   float64
	ShareDiff  float64
	ShareCount int
	LastSeen   int64
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
			_ = s.CalculateHash()
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

	if s.ShareInfo.AbsHeight > currentTip.ShareInfo.AbsHeight {
		sc.TipHash = s.Hash
		return
	}
	if s.ShareInfo.AbsHeight == currentTip.ShareInfo.AbsHeight {
		if s.ShareInfo.Timestamp > currentTip.ShareInfo.Timestamp {
			sc.TipHash = s.Hash
		} else if s.ShareInfo.Timestamp == currentTip.ShareInfo.Timestamp && s.Hash.String() > currentTip.Hash.String() {
			sc.TipHash = s.Hash
		}
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
	tmpPath := "sharechain.json.tmp"
	b, err := json.MarshalIndent(sharesToSave, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpPath, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// GetSharesForSync returns up to maxCount most recent shares for fast P2P stream synchronization.
func (sc *ShareChain) GetSharesForSync(maxCount int) []Share {
	sc.mutex.RLock()
	list := make([]Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		list = append(list, *s)
	}
	sc.mutex.RUnlock()

	sort.Slice(list, func(i, j int) bool {
		if list[i].ShareInfo.Timestamp == list[j].ShareInfo.Timestamp {
			return list[i].Hash.String() > list[j].Hash.String()
		}
		return list[i].ShareInfo.Timestamp > list[j].ShareInfo.Timestamp
	})

	if maxCount > 0 && len(list) > maxCount {
		list = list[:maxCount]
	}
	return list
}

const (
	MinPoolShareDiff      = 0.50
	TargetPoolShareTime   = 5.0
	MaxPPLNSWindowAgeSecs = 24 * 3600
)

// GetPoolShareDifficulty computes the pool-wide P2Pool share difficulty targeting 1 share every 15s across the pool.
func (sc *ShareChain) GetPoolShareDifficulty() float64 {
	sc.poolStatsMutex.RLock()
	pH := sc.poolHashrate
	sc.poolStatsMutex.RUnlock()

	diff := (pH * TargetPoolShareTime) / 16777216.0
	if diff < MinPoolShareDiff {
		diff = MinPoolShareDiff
	}
	return diff
}

// GetEffectiveShareDiff returns the verified difficulty work of a share, clamping self-reported Target
// against actual cryptographic POWHash and the maximum reasonable pool difficulty to prevent spoofing.
func (sc *ShareChain) GetEffectiveShareDiff(s *Share) float64 {
	if s == nil || s.Target == nil || s.Target.Sign() <= 0 {
		return 0
	}
	targetDiff := TargetToDiff(s.Target)
	if targetDiff <= 0 {
		return 0
	}
	if s.POWHash != nil {
		powInt := new(big.Int).SetBytes(s.POWHash.CloneBytes())
		powDiff := TargetToDiff(powInt)
		if powDiff < targetDiff {
			return 0 // Invalid PoW for claimed target
		}
	}
	// Cap maximum single-share credited difficulty to prevent self-asserted target spoofing
	maxReasonableDiff := sc.GetPoolShareDifficulty() * 4.0
	if maxReasonableDiff < 16.0 {
		maxReasonableDiff = 16.0
	}
	if targetDiff > maxReasonableDiff {
		targetDiff = maxReasonableDiff
	}
	return targetDiff
}

// GetSharesForPayout deterministically selects valid shares from the synchronized ShareChain
// capped by cumulative work (and a 24-hour max lookback) rather than raw share count, preventing
// low-diff share flooding from shrinking the PPLNS payout window.
func (sc *ShareChain) GetSharesForPayout(blockFindShareHash *chainhash.Hash, windowSize int) ([]*Share, error) {
	sc.mutex.RLock()
	allValid := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		if s != nil && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone && s.Hash != nil {
			allValid = append(allValid, s)
		}
	}
	sc.mutex.RUnlock()

	if len(allValid) == 0 {
		return allValid, nil
	}

	// Sort deterministically: newest Timestamp first (tie-broken by Hash)
	sort.Slice(allValid, func(i, j int) bool {
		if allValid[i].ShareInfo.Timestamp == allValid[j].ShareInfo.Timestamp {
			return allValid[i].Hash.String() > allValid[j].Hash.String()
		}
		return allValid[i].ShareInfo.Timestamp > allValid[j].ShareInfo.Timestamp
	})

	if windowSize <= 0 {
		windowSize = 2160
	}
	poolShareDiff := sc.GetPoolShareDifficulty()
	targetCumulativeWork := float64(windowSize) * poolShareDiff

	sc.poolStatsMutex.RLock()
	// vertcoind difficulty is in 2^32-hash units; share difficulty is in 2^24-hash units.
	netDiff := sc.networkDifficulty * 256.0
	sc.poolStatsMutex.RUnlock()
	if netDiff > 0 && targetCumulativeWork > netDiff*3.0 {
		targetCumulativeWork = netDiff * 3.0
	}

	newestTs := int64(allValid[0].ShareInfo.Timestamp)
	cutoffTs := newestTs - MaxPPLNSWindowAgeSecs

	selected := make([]*Share, 0, windowSize)
	var accumulatedWork float64
	for _, s := range allValid {
		if int64(s.ShareInfo.Timestamp) < cutoffTs {
			break
		}
		diff := sc.GetEffectiveShareDiff(s)
		if diff <= 0 {
			continue
		}
		selected = append(selected, s)
		accumulatedWork += diff
		if accumulatedWork >= targetCumulativeWork {
			break
		}
	}
	return selected, nil
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

func (sc *ShareChain) GetNetworkMiners(window time.Duration) map[string]*NetworkMinerStats {
	sc.mutex.RLock()
	shares := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		shares = append(shares, s)
	}
	sc.mutex.RUnlock()

	var maxTs int64
	for _, s := range shares {
		if int64(s.ShareInfo.Timestamp) > maxTs {
			maxTs = int64(s.ShareInfo.Timestamp)
		}
	}
	if maxTs == 0 {
		maxTs = time.Now().Unix()
	}

	windowSec := window.Seconds()
	if windowSec <= 0 {
		windowSec = 300
	}
	cutoff := maxTs - int64(windowSec)

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
		if addr == "" || s.Target == nil || s.Target.Sign() <= 0 {
			continue
		}
		diffFloat := sc.GetEffectiveShareDiff(s)
		if diffFloat <= 0 {
			continue
		}
		_ = maxTargetFloat

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
		span := float64(maxTs - mw.firstSeen)
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
	netMiners := sc.GetNetworkMiners(5 * time.Minute)
	var totalPoolHashrate float64
	for _, nm := range netMiners {
		totalPoolHashrate += nm.Hashrate
	}

	info, err := sc.rpcClient.GetMiningInfo()
	netHash := 0.0
	netDiff := 0.0
	if err == nil {
		netHash = info.NetworkHashPS
		netDiff = info.Difficulty
	}

	sc.poolStatsMutex.Lock()
	sc.poolHashrate = totalPoolHashrate
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
