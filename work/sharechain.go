package work

import (
	"encoding/json"
	"fmt"
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
	verified          map[string]bool
	cumWork           map[string]float64
	chainLen          map[string]int
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
		shares: make(map[string]*Share), verified: make(map[string]bool), cumWork: make(map[string]float64), chainLen: make(map[string]int),
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
	for i := range newShares {
		sh := newShares[i]
		if sh.Hash == nil {
			_ = sh.CalculateHash()
		}
		if sh.Hash == nil {
			continue
		}
		h := sh.Hash.String()
		if _, ok := sc.shares[h]; ok {
			continue
		}
		if sh.POWHash == nil {
			_ = sh.CalculatePOWHash()
		}
		if ok, _ := sh.IsValid(); !ok {
			continue
		}
		cp := sh
		sc.shares[h] = &cp
	}
	sc.mutex.Unlock()
	v := sc.verifyPending()
	if v > 0 {
		sc.updateStats()
	}
	return v
}
func (sc *ShareChain) PruneOldShares() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()
	cutoff := time.Now().Add(-maxShareAge).Unix()
	pruned := 0
	for hStr, s := range sc.shares {
		if int64(s.ShareInfo.Timestamp) < cutoff {
			delete(sc.shares, hStr)
			delete(sc.verified, hStr)
			delete(sc.cumWork, hStr)
			delete(sc.chainLen, hStr)
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
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &fileShares); err != nil {
		return err
	}
	sort.Slice(fileShares, func(i, j int) bool {
		return fileShares[i].ShareInfo.Timestamp < fileShares[j].ShareInfo.Timestamp
	})
	sc.loadTrusted(fileShares)
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
	w := sc.getOrderedChainWalkLocked(sc.TipHash, maxCount)
	res := make([]Share, 0, len(w))
	for _, x := range w {
		res = append(res, *x)
	}
	sc.mutex.RUnlock()
	return res
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
		if vis[h] || !sc.verified[h] {
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

func (sc *ShareChain) loadTrusted(fs []Share) {
	sc.mutex.Lock()
	for i := range fs {
		sh := fs[i]
		if sh.Hash == nil {
			_ = sh.CalculateHash()
		}
		if sh.Hash != nil {
			cp := sh
			sc.shares[sh.Hash.String()] = &cp
		}
	}
	vis := make(map[string]bool, len(sc.shares))
	var path []*Share
	for h, sh := range sc.shares {
		if sc.verified[h] {
			continue
		}
		path = path[:0]
		for cur := sh; cur != nil && cur.Hash != nil; {
			ch := cur.Hash.String()
			if sc.verified[ch] || vis[ch] {
				break
			}
			vis[ch] = true
			path = append(path, cur)
			p := cur.ShareInfo.ShareData.PreviousShareHash
			if p == nil || p.IsEqual(sc.genesisHash) {
				break
			}
			cur = sc.shares[p.String()]
		}
		for k := len(path) - 1; k >= 0; k-- {
			sc.markVerifiedLocked(path[k])
		}
	}
	sc.mutex.Unlock()
	sc.updateStats()
}

func (sc *ShareChain) updateTipLocked(s *Share) {
	if s == nil || s.Hash == nil || s.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
		return
	}
	h := s.Hash.String()
	if !sc.verified[h] {
		return
	}
	if sc.TipHash == nil || sc.TipHash.IsEqual(sc.genesisHash) || !sc.verified[sc.TipHash.String()] {
		sc.TipHash = s.Hash
		return
	}
	ch := sc.TipHash.String()
	wn, wc := sc.cumWork[h], sc.cumWork[ch]
	if wn > wc+1e-9 || (wn >= wc-1e-9 && h > ch) {
		sc.TipHash = s.Hash
	}
}

func (sc *ShareChain) markVerifiedLocked(s *Share) {
	h := s.Hash.String()
	sc.verified[h] = true
	var pw float64
	var pl int
	if p := s.ShareInfo.ShareData.PreviousShareHash; p != nil && !p.IsEqual(sc.genesisHash) {
		ps := p.String()
		pw, pl = sc.cumWork[ps], sc.chainLen[ps]
	}
	d := TargetToDiff(s.Target)
	if s.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
		d = 0
	} else if d < MinPoolShareDiff {
		d = MinPoolShareDiff
	}
	sc.cumWork[h], sc.chainLen[h] = pw+d, pl+1
	sc.updateTipLocked(s)
}

func (sc *ShareChain) verifyPending() int {
	vn := 0
	for {
		sc.mutex.RLock()
		var pend []*Share
		for h, s := range sc.shares {
			if !sc.verified[h] && s != nil && s.Hash != nil {
				pend = append(pend, s)
			}
		}
		sc.mutex.RUnlock()
		if len(pend) == 0 {
			break
		}
		sort.Slice(pend, func(i, j int) bool { return pend[i].ShareInfo.Timestamp < pend[j].ShareInfo.Timestamp })
		prog := false
		for _, s := range pend {
			h, p := s.Hash.String(), s.ShareInfo.ShareData.PreviousShareHash
			sc.mutex.RLock()
			hasP := p != nil && !p.IsEqual(sc.genesisHash) && sc.shares[p.String()] != nil
			pVer := p != nil && sc.verified[p.String()]
			pLen := 0
			if pVer {
				pLen = sc.chainLen[p.String()]
			}
			sc.mutex.RUnlock()
			if hasP && !pVer {
				continue
			}
			post := int64(s.ShareInfo.Timestamp) >= ConsensusV2ActivationTime || s.Type >= ShareVersionV2
			if post && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
				if !pVer || pLen < TargetLookbehind {
					if int64(s.ShareInfo.Timestamp) > time.Now().Unix()-1800 {
						continue
					}
				} else if !sc.checkConsensus(s, p, pLen) {
					sc.mutex.Lock()
					delete(sc.shares, h)
					sc.mutex.Unlock()
					continue
				}
			}
			sc.mutex.Lock()
			sc.markVerifiedLocked(s)
			sc.mutex.Unlock()
			vn++
			prog = true
		}
		if !prog {
			break
		}
	}
	return vn
}

func (sc *ShareChain) checkConsensus(s *Share, p *chainhash.Hash, pLen int) bool {
	h := s.Hash.String()
	expD, clmD := sc.GetExpectedShareDifficulty(p), TargetToDiff(s.Target)
	pi := new(big.Int).SetBytes(s.POWHash.CloneBytes())
	actD, netT := TargetToDiff(pi), blockchain.CompactToBig(s.MinHeader.Bits)
	if (actD < expD*0.999 || clmD < expD*0.999 || clmD > expD*1.001) && pi.Cmp(netT) > 0 {
		logging.Warnf("ShareChain: Rejected share %s: diff %.4f != consensus %.4f", h[:12], clmD, expD)
		return false
	}
	if pLen >= DefaultPPLNSWindow {
		if ok, r := VerifyShareCoinbase(s, sc); !ok {
			logging.Warnf("ShareChain: Rejected share %s: %s", h[:12], r)
			return false
		}
	}
	return true
}

func (sc *ShareChain) GetMissingParentHashes(max int) []string {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for h, s := range sc.shares {
		if !sc.verified[h] && s != nil {
			p := s.ShareInfo.ShareData.PreviousShareHash
			if p != nil && !p.IsEqual(sc.genesisHash) {
				ps := p.String()
				if sc.shares[ps] == nil && !seen[ps] {
					seen[ps] = true
					out = append(out, ps)
					if max > 0 && len(out) >= max {
						break
					}
				}
			}
		}
	}
	return out
}

func (sc *ShareChain) GetSharesByHashWithParents(hashes []string, parents int) []Share {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	if parents <= 0 {
		parents = 250
	}
	seen := make(map[string]bool)
	var out []Share
	for _, cur := range hashes {
		for i := 0; i < parents && cur != ""; i++ {
			if seen[cur] || !sc.verified[cur] || sc.shares[cur] == nil {
				break
			}
			s := sc.shares[cur]
			seen[cur] = true
			out = append(out, *s)
			p := s.ShareInfo.ShareData.PreviousShareHash
			if p == nil || p.IsEqual(sc.genesisHash) {
				break
			}
			cur = p.String()
		}
	}
	return out
}

func (sc *ShareChain) SetNetworkBitsFromTemplate(bitsHex string) {
	var b uint32
	if _, err := fmt.Sscanf(bitsHex, "%x", &b); err == nil && b != 0 {
		nd := TargetToDiff(blockchain.CompactToBig(b))
		if nd > 0 {
			sc.poolStatsMutex.Lock()
			sc.networkDifficulty = nd
			sc.networkHashrate = (nd * 4294967296.0) / 150.0
			sc.poolStatsMutex.Unlock()
		}
	}
}
