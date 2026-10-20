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
	"github.com/btcsuite/btcd/blockchain"
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
	TargetPoolShareTime   = 15.0
	TargetLookbehind      = 200
	DefaultPPLNSWindow    = 8640
	MaxPPLNSWindowAgeSecs = 36 * 3600
)

func (sc *ShareChain) getOrderedChainWalkLocked(start *chainhash.Hash, max int) []*Share {
	vis := make(map[string]bool)
	res := make([]*Share, 0, 256)
	cur := start
	if cur == nil || cur.IsEqual(sc.genesisHash) {
		cur = sc.TipHash
	}
	for cur != nil && !cur.IsEqual(sc.genesisHash) {
		h := cur.String()
		if vis[h] {
			break
		}
		s := sc.shares[h]
		if s == nil {
			break
		}
		vis[h] = true
		if s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			res = append(res, s)
			if max > 0 && len(res) >= max {
				return res
			}
		}
		cur = s.ShareInfo.ShareData.PreviousShareHash
	}
	var maxTs int32 = 0x7fffffff
	if len(res) > 0 {
		maxTs = res[len(res)-1].ShareInfo.Timestamp
	}
	rem := make([]*Share, 0, len(sc.shares))
	for h, s := range sc.shares {
		if !vis[h] && s != nil && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone && s.Hash != nil && s.ShareInfo.Timestamp <= maxTs {
			rem = append(rem, s)
		}
	}
	sort.Slice(rem, func(i, j int) bool {
		if rem[i].ShareInfo.Timestamp == rem[j].ShareInfo.Timestamp {
			return rem[i].Hash.String() > rem[j].Hash.String()
		}
		return rem[i].ShareInfo.Timestamp > rem[j].ShareInfo.Timestamp
	})
	for _, s := range rem {
		res = append(res, s)
		if max > 0 && len(res) >= max {
			break
		}
	}
	return res
}
func (sc *ShareChain) GetExpectedShareDifficulty(prev *chainhash.Hash) float64 {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.getExpectedShareDifficultyLocked(prev)
}
func (sc *ShareChain) getExpectedShareDifficultyLocked(prev *chainhash.Hash) float64 {
	w := sc.getOrderedChainWalkLocked(prev, TargetLookbehind)
	if len(w) < TargetLookbehind {
		return MinPoolShareDiff
	}
	var tw float64
	for i := 0; i < TargetLookbehind-1; i++ {
		d := TargetToDiff(w[i].Target)
		if d < MinPoolShareDiff {
			d = MinPoolShareDiff
		}
		tw += d
	}
	dt := float64(w[0].ShareInfo.Timestamp - w[TargetLookbehind-1].ShareInfo.Timestamp)
	if dt <= 1.0 {
		dt = 1.0
	}
	pDiff := TargetToDiff(w[0].Target)
	if pDiff < MinPoolShareDiff {
		pDiff = MinPoolShareDiff
	}
	raw := (tw * TargetPoolShareTime) / dt
	if raw < pDiff*0.90 {
		raw = pDiff * 0.90
	} else if raw > pDiff*1.10 {
		raw = pDiff * 1.10
	}
	if raw < MinPoolShareDiff {
		raw = MinPoolShareDiff
	}
	return raw
}
func (sc *ShareChain) GetPoolShareDifficulty() float64 {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.getExpectedShareDifficultyLocked(sc.TipHash)
}
func (sc *ShareChain) GetEffectiveShareDiff(s *Share) float64 {
	if s == nil || s.Target == nil || s.Target.Sign() <= 0 {
		return 0
	}
	td := TargetToDiff(s.Target)
	if td <= 0 {
		return 0
	}
	if s.POWHash != nil {
		p := new(big.Int).SetBytes(s.POWHash.CloneBytes())
		if TargetToDiff(p) < td*0.999 {
			return 0
		}
	}
	if td > 16.0 {
		td = 16.0
	}
	return td
}
func (sc *ShareChain) GetSharesForPayout(tip *chainhash.Hash, win int) ([]*Share, error) {
	sc.mutex.RLock()
	if win <= 0 {
		win = DefaultPPLNSWindow
	}
	all := sc.getOrderedChainWalkLocked(tip, win)
	pDiff := sc.getExpectedShareDifficultyLocked(tip)
	sc.mutex.RUnlock()
	if len(all) == 0 {
		return all, nil
	}
	capW := float64(win) * pDiff
	if all[0].MinHeader.Bits != 0 {
		bd := TargetToDiff(blockchain.CompactToBig(all[0].MinHeader.Bits))
		if bd > 0 && capW > bd*12.0 {
			capW = bd * 12.0
		}
	}
	cut := int64(all[0].ShareInfo.Timestamp) - MaxPPLNSWindowAgeSecs
	maxD := pDiff * 4.0
	if maxD < 16.0 {
		maxD = 16.0
	}
	sel := make([]*Share, 0, len(all))
	var acc float64
	for _, s := range all {
		if int64(s.ShareInfo.Timestamp) < cut {
			break
		}
		d := sc.GetEffectiveShareDiff(s)
		if d <= 0 {
			continue
		}
		if d > maxD {
			d = maxD
		}
		sel = append(sel, s)
		acc += d
		if acc >= capW {
			break
		}
	}
	return sel, nil
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
