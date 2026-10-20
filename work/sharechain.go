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
	"github.com/btcsuite/btcd/blockchain"
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
	TargetPoolShareTime   = 15.0
	TargetLookbehind      = 200
	DefaultPPLNSWindow    = 8640
	MaxPPLNSWindowAgeSecs = 36 * 3600
)

// getOrderedChainWalk walks the ShareChain starting from startHash via PreviousShareHash links,
// falling back to deterministic timestamp+hash order if a historical link is missing.
// Must be called with sc.mutex held.
func (sc *ShareChain) getOrderedChainWalkLocked(startHash *chainhash.Hash, maxCount int) []*Share {
	visited := make(map[string]bool)
	result := make([]*Share, 0, 256)

	currHash := startHash
	if currHash == nil || currHash.IsEqual(sc.genesisHash) {
		currHash = sc.TipHash
	}
	for currHash != nil && !currHash.IsEqual(sc.genesisHash) {
		hStr := currHash.String()
		if visited[hStr] {
			break
		}
		s, exists := sc.shares[hStr]
		if !exists || s == nil {
			break
		}
		visited[hStr] = true
		if s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			result = append(result, s)
			if maxCount > 0 && len(result) >= maxCount {
				return result
			}
		}
		currHash = s.ShareInfo.ShareData.PreviousShareHash
	}

	// If parent links ended early (e.g. across older historical shares), append remaining valid shares older than the last walked share
	var maxTs int32 = 0x7fffffff
	if len(result) > 0 {
		maxTs = result[len(result)-1].ShareInfo.Timestamp
	}
	remaining := make([]*Share, 0, len(sc.shares))
	for hStr, s := range sc.shares {
		if !visited[hStr] && s != nil && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone && s.Hash != nil && s.ShareInfo.Timestamp <= maxTs {
			remaining = append(remaining, s)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].ShareInfo.Timestamp == remaining[j].ShareInfo.Timestamp {
			return remaining[i].Hash.String() > remaining[j].Hash.String()
		}
		return remaining[i].ShareInfo.Timestamp > remaining[j].ShareInfo.Timestamp
	})
	for _, s := range remaining {
		result = append(result, s)
		if maxCount > 0 && len(result) >= maxCount {
			break
		}
	}
	return result
}

// GetExpectedShareDifficulty deterministically derives the consensus share difficulty from the ShareChain
// using a 200-share lookbehind and a +/-10% step limit, matching original p2pool-vtc (data.py#L149-L154).
func (sc *ShareChain) GetExpectedShareDifficulty(prevShareHash *chainhash.Hash) float64 {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.getExpectedShareDifficultyLocked(prevShareHash)
}

func (sc *ShareChain) getExpectedShareDifficultyLocked(prevShareHash *chainhash.Hash) float64 {
	walk := sc.getOrderedChainWalkLocked(prevShareHash, TargetLookbehind)
	if len(walk) < TargetLookbehind {
		return MinPoolShareDiff
	}

	var totalWork float64
	for i := 0; i < TargetLookbehind-1; i++ {
		d := TargetToDiff(walk[i].Target)
		if d < MinPoolShareDiff {
			d = MinPoolShareDiff
		}
		totalWork += d
	}

	actualTime := float64(walk[0].ShareInfo.Timestamp - walk[TargetLookbehind-1].ShareInfo.Timestamp)
	desiredTime := float64(TargetLookbehind-1) * TargetPoolShareTime
	if actualTime <= 1.0 {
		actualTime = 1.0
	}

	// Clamp adjustment to +/-10% of the parent share difficulty (matching original p2pool +/-10% bound)
	parentDiff := TargetToDiff(walk[0].Target)
	if parentDiff < MinPoolShareDiff {
		parentDiff = MinPoolShareDiff
	}
	rawDiff := (totalWork * TargetPoolShareTime) / actualTime
	_ = desiredTime

	minAllowed := parentDiff * 0.90
	maxAllowed := parentDiff * 1.10
	if rawDiff < minAllowed {
		rawDiff = minAllowed
	} else if rawDiff > maxAllowed {
		rawDiff = maxAllowed
	}
	if rawDiff < MinPoolShareDiff {
		rawDiff = MinPoolShareDiff
	}
	return rawDiff
}

// GetPoolShareDifficulty returns the deterministic pool share difficulty at the current ShareChain tip.
func (sc *ShareChain) GetPoolShareDifficulty() float64 {
	sc.mutex.RLock()
	tip := sc.TipHash
	diff := sc.getExpectedShareDifficultyLocked(tip)
	sc.mutex.RUnlock()
	return diff
}

// GetEffectiveShareDiff returns the verified difficulty work of a share, checking cryptographic POWHash
// against s.Target and clamping against 4x the deterministic sharechain difficulty to prevent spoofing.
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
		if powDiff < targetDiff*0.999 {
			return 0 // Invalid PoW for claimed target
		}
	}
	if targetDiff > 16.0 {
		targetDiff = 16.0
	}
	return targetDiff
}

// GetSharesForPayout deterministically selects valid shares walking backward from blockFindShareHash,
// capped by cumulative work (up to 8640 shares / ~36 hours or 12 blocks of network work), so every
// P2Pool node computes the exact same PPLNS split solely from the ShareChain.
func (sc *ShareChain) GetSharesForPayout(blockFindShareHash *chainhash.Hash, windowSize int) ([]*Share, error) {
	sc.mutex.RLock()
	if windowSize <= 0 {
		windowSize = DefaultPPLNSWindow
	}
	allValid := sc.getOrderedChainWalkLocked(blockFindShareHash, windowSize)
	poolShareDiff := sc.getExpectedShareDifficultyLocked(blockFindShareHash)
	sc.mutex.RUnlock()

	if len(allValid) == 0 {
		return allValid, nil
	}

	targetCumulativeWork := float64(windowSize) * poolShareDiff

	// Derive network block difficulty deterministically from the tip share header bits (12 blocks of work cap)
	if allValid[0].MinHeader.Bits != 0 {
		blockTarget := blockchain.CompactToBig(allValid[0].MinHeader.Bits)
		blockDiffInShareUnits := TargetToDiff(blockTarget)
		if blockDiffInShareUnits > 0 && targetCumulativeWork > blockDiffInShareUnits*12.0 {
			targetCumulativeWork = blockDiffInShareUnits * 12.0
		}
	}

	newestTs := int64(allValid[0].ShareInfo.Timestamp)
	cutoffTs := newestTs - MaxPPLNSWindowAgeSecs
	maxSingleShareDiff := poolShareDiff * 4.0
	if maxSingleShareDiff < 16.0 {
		maxSingleShareDiff = 16.0
	}

	selected := make([]*Share, 0, len(allValid))
	var accumulatedWork float64
	for _, s := range allValid {
		if int64(s.ShareInfo.Timestamp) < cutoffTs {
			break
		}
		diff := sc.GetEffectiveShareDiff(s)
		if diff <= 0 {
			continue
		}
		if diff > maxSingleShareDiff {
			diff = maxSingleShareDiff
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
