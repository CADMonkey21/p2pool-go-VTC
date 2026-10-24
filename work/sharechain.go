package work

import (
	"bufio"
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
	children          map[string][]string // previous share hash -> held shares built on it
	curPrevBlock      string
	curBits           uint32
	mutex             sync.RWMutex
	changes           uint64 // shares added or removed since Load; guarded by mutex
	commitMu          sync.Mutex
	savedChanges      uint64 // changes as of the last save; guarded by commitMu
	verifyMu          sync.Mutex
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
		shares: make(map[string]*Share), verified: make(map[string]bool), cumWork: make(map[string]float64), chainLen: make(map[string]int), children: make(map[string][]string),
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
	now := time.Now()
	sc.mutex.Lock()
	var added []string
	for i := range newShares {
		sh := newShares[i]
		if CheckShareTime(&sh, now) != "" || !CheckShareTargetFloor(&sh) {
			continue
		}
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
		cp.decodedAddr = DecodeShareAddress(&cp)
		sc.shares[h] = &cp
		sc.changes++
		added = append(added, h)
	}
	sc.addWorkLocked(added)
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
	if pruned := sc.pruneLocked(time.Now().Add(-maxShareAge).Unix()); pruned > 0 {
		logging.Debugf("ShareChain: Pruned %d shares older than 72 hours", pruned)
	}
}

func (sc *ShareChain) pruneLocked(cutoff int64) int {
	var old []string
	for h, s := range sc.shares {
		if int64(s.ShareInfo.Timestamp) < cutoff {
			old = append(old, h)
		}
	}
	if len(old) > 0 {
		sc.changes++
		sc.removeSharesLocked(old)
	}
	return len(old)
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

// Commit writes the sharechain to sharechain.json unless no shares have been added or
// removed since the last save.
func (sc *ShareChain) Commit() error {
	sc.commitMu.Lock()
	defer sc.commitMu.Unlock()
	sc.mutex.RLock()
	changes := sc.changes
	if changes == sc.savedChanges {
		sc.mutex.RUnlock()
		return nil
	}
	sharesToSave := make([]Share, 0, len(sc.shares))
	for _, s := range sc.shares {
		sharesToSave = append(sharesToSave, *s)
	}
	sc.mutex.RUnlock()

	path := "sharechain.json"
	tmpPath := "sharechain.json.tmp"
	if err := writeSharesJSON(tmpPath, sharesToSave); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	sc.savedChanges = changes
	return nil
}

// writeSharesJSON encodes shares one at a time, one per line, so the whole file is
// never held in memory.
func writeSharesJSON(path string, shares []Share) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w) // Encode appends a newline after each share
	w.WriteString("[\n")
	for i := range shares {
		if i > 0 {
			w.WriteString(",")
		}
		if err := enc.Encode(&shares[i]); err != nil {
			f.Close()
			return err
		}
	}
	w.WriteString("]\n")
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// GetSharesForSync returns up to maxCount most recent shares for fast P2P stream synchronization.
func (sc *ShareChain) GetSharesForSync(maxCount int) []Share {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	vis := make(map[string]bool)
	res := make([]Share, 0, 256)
	cur := sc.TipHash
	nonStale := 0
	for cur != nil && !cur.IsEqual(sc.genesisHash) {
		h := cur.String()
		if vis[h] {
			break
		}
		sh := sc.shares[h]
		if sh == nil {
			break
		}
		vis[h] = true
		res = append(res, *sh)
		if sh.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			nonStale++
			if maxCount > 0 && nonStale >= maxCount {
				break
			}
		}
		cur = sh.ShareInfo.ShareData.PreviousShareHash
	}
	return res
}

const (
	MinPoolShareDiff       = 0.50
	TargetPoolShareTime    = 15.0
	TargetLookbehind       = 200
	DefaultPPLNSWindow     = 8640
	MaxPPLNSWindowAgeSecs  = 36 * 3600
	MaxShareTimeFutureSecs = 2 * 3600
	MaxShareTimePastSecs   = 10 * 60
)

func (s *Share) getTargetDiff() float64 {
	if s == nil || s.Target == nil || s.Target.Sign() <= 0 {
		return 0
	}
	if s.targetDiff > 0 {
		return s.targetDiff
	}
	d := TargetToDiff(s.Target)
	s.targetDiff = d
	return d
}

func (s *Share) getPrevKey() string {
	if s.prevKey != "" {
		return s.prevKey
	}
	if s.ShareInfo.ShareData.PreviousShareHash != nil {
		s.prevKey = s.ShareInfo.ShareData.PreviousShareHash.String()
	}
	return s.prevKey
}

func (sc *ShareChain) getOrderedChainWalkLocked(start *chainhash.Hash, max int) []*Share {
	capHint := max
	if capHint <= 0 || capHint > 512 {
		capHint = 512
	}
	res := make([]*Share, 0, capHint)
	cur := start
	if cur == nil || cur.IsEqual(sc.genesisHash) {
		cur = sc.TipHash
	}
	if cur == nil || cur.IsEqual(sc.genesisHash) {
		return res
	}
	h := cur.String()
	maxSteps := len(sc.shares) + 1
	for steps := 0; steps < maxSteps && h != ""; steps++ {
		s := sc.shares[h]
		if s == nil {
			break
		}
		if s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			res = append(res, s)
			if max > 0 && len(res) >= max {
				return res
			}
		}
		p := s.ShareInfo.ShareData.PreviousShareHash
		if p == nil || p.IsEqual(sc.genesisHash) {
			break
		}
		h = s.getPrevKey()
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
		d := w[i].getTargetDiff()
		if d < MinPoolShareDiff {
			d = MinPoolShareDiff
		}
		tw += d
	}
	dt := float64(int64(w[0].ShareInfo.Timestamp) - int64(w[TargetLookbehind-1].ShareInfo.Timestamp))
	if dt <= 1.0 {
		dt = 1.0
	}
	pDiff := w[0].getTargetDiff()
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
	if s.effDiffReady {
		return s.effDiff
	}
	td := s.getTargetDiff()
	if td <= 0 {
		s.effDiff = 0
		s.effDiffReady = true
		return 0
	}
	if s.POWHash != nil {
		p := new(big.Int).SetBytes(s.POWHash.CloneBytes())
		if TargetToDiff(p) < td*0.999 {
			s.effDiff = 0
			s.effDiffReady = true
			return 0
		}
	}
	if td > 16.0 {
		td = 16.0
	}
	s.effDiff = td
	s.effDiffReady = true
	return td
}
func (sc *ShareChain) GetSharesForPayout(tip *chainhash.Hash, win int) ([]*Share, error) {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	if win <= 0 {
		win = DefaultPPLNSWindow
	}
	cur := tip
	if cur == nil || cur.IsEqual(sc.genesisHash) {
		cur = sc.TipHash
	}
	if cur == nil || cur.IsEqual(sc.genesisHash) {
		return nil, nil
	}
	pDiff := sc.getExpectedShareDifficultyLocked(cur)
	h := cur.String()
	maxSteps := len(sc.shares) + 1
	var first *Share
	var capW float64
	var cut int64
	maxD := pDiff * 4.0
	if maxD < 16.0 {
		maxD = 16.0
	}
	sel := make([]*Share, 0, 512)
	var acc float64
	validCount := 0
	for steps := 0; steps < maxSteps && h != ""; steps++ {
		s := sc.shares[h]
		if s == nil {
			break
		}
		if s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			if first == nil {
				first = s
				capW = float64(win) * pDiff
				if first.MinHeader.Bits != 0 {
					bd := TargetToDiff(blockchain.CompactToBig(first.MinHeader.Bits))
					if bd > 0 && capW > bd*12.0 {
						capW = bd * 12.0
					}
				}
				cut = int64(first.ShareInfo.Timestamp) - MaxPPLNSWindowAgeSecs
			}
			validCount++
			if int64(s.ShareInfo.Timestamp) < cut {
				break
			}
			d := sc.GetEffectiveShareDiff(s)
			if d > 0 {
				if d > maxD {
					d = maxD
				}
				sel = append(sel, s)
				acc += d
				if acc >= capW {
					break
				}
			}
			if validCount >= win {
				break
			}
		}
		p := s.ShareInfo.ShareData.PreviousShareHash
		if p == nil || p.IsEqual(sc.genesisHash) {
			break
		}
		h = s.getPrevKey()
	}
	return sel, nil
}

func DecodeShareAddress(s *Share) string {
	if s == nil || len(s.ShareInfo.ShareData.PubKeyHash) == 0 {
		return ""
	}
	if s.decodedAddr != "" {
		return s.decodedAddr
	}
	ver := s.ShareInfo.ShareData.PubKeyHashVersion
	if ver == 0x00 || (ver <= 16 && int64(s.ShareInfo.Timestamp) >= TaprootPayoutActivationTime) {
		converted, err := bech32.ConvertBits(s.ShareInfo.ShareData.PubKeyHash, 8, 5, true)
		if err != nil {
			return ""
		}
		hrp := "vtc"
		if config.Active.Testnet {
			hrp = "tvtc"
		}
		encode := bech32.Encode
		if ver > 0 {
			encode = bech32.EncodeM
		}
		addr, _ := encode(hrp, append([]byte{ver}, converted...))
		s.decodedAddr = addr
		return addr
	}
	addr := base58.CheckEncode(s.ShareInfo.ShareData.PubKeyHash, s.ShareInfo.ShareData.PubKeyHashVersion)
	s.decodedAddr = addr
	return addr
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

	var info *rpc.MiningInfo
	err := fmt.Errorf("light")
	if !config.Active.LightMode && sc.rpcClient != nil {
		info, err = sc.rpcClient.GetMiningInfo()
	}
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
	w := sc.getOrderedChainWalkLocked(sc.TipHash, count)
	res := make([]*Share, len(w))
	copy(res, w)
	return res, nil
}

func (sc *ShareChain) GetTipHash() *chainhash.Hash {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.TipHash
}

func (sc *ShareChain) HasShareWithPrevBlock(blockHash string) bool {
	if blockHash == "" {
		return false
	}
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	for h, s := range sc.shares {
		if sc.verified[h] && s != nil && s.MinHeader.PreviousBlock != nil && s.MinHeader.PreviousBlock.String() == blockHash {
			return true
		}
	}
	return false
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
			cp.decodedAddr = DecodeShareAddress(&cp)
			h := sh.Hash.String()
			sc.shares[h] = &cp
			sc.verified[h] = true
		}
	}
	sc.recomputeWorkAndTipLocked()
	sc.mutex.Unlock()
	sc.updateStats()
}

// recomputeWorkAndTipLocked rebuilds the children index, every share's cumulative
// work and length, and the tip from scratch.
func (sc *ShareChain) recomputeWorkAndTipLocked() {
	sc.children = make(map[string][]string)
	sc.cumWork = make(map[string]float64, len(sc.shares))
	sc.chainLen = make(map[string]int, len(sc.shares))
	hs := make([]string, 0, len(sc.shares))
	for h, sh := range sc.shares {
		if sh != nil {
			sc.linkLocked(h, sh)
			hs = append(hs, h)
		}
	}
	sc.updateWorkLocked(hs)
	sc.selectTipLocked()
}

// addWorkLocked indexes newly stored shares and gives them and their descendants
// their cumulative work, so children stored before their parent gain its work.
func (sc *ShareChain) addWorkLocked(hs []string) {
	for _, h := range hs {
		sc.linkLocked(h, sc.shares[h])
	}
	sc.considerTipLocked(sc.updateWorkLocked(hs))
}

// removeSharesLocked deletes shares and drops their work from their descendants.
func (sc *ShareChain) removeSharesLocked(hs []string) {
	rescan := false
	for _, h := range hs {
		if sc.unlinkShareLocked(h) {
			rescan = true
		}
	}
	sc.dropWorkLocked(hs, rescan)
}

// unlinkShareLocked deletes a share from the maps and the children index, leaving
// its descendants' work to dropWorkLocked. It reports whether the share was a tip
// candidate.
func (sc *ShareChain) unlinkShareLocked(h string) bool {
	sh := sc.shares[h]
	if sh == nil {
		return false
	}
	cand := sc.isTipCandidateLocked(h)
	if p := sc.prevKeyLocked(sh); p != "" {
		cs := sc.children[p]
		for i, c := range cs {
			if c == h {
				cs[i] = cs[len(cs)-1]
				cs = cs[:len(cs)-1]
				break
			}
		}
		if len(cs) == 0 {
			delete(sc.children, p)
		} else {
			sc.children[p] = cs
		}
	}
	delete(sc.shares, h)
	delete(sc.verified, h)
	delete(sc.cumWork, h)
	delete(sc.chainLen, h)
	return cand
}

// dropWorkLocked recomputes the work of the descendants of unlinked shares and
// reselects the tip if a candidate lost work or rescan is set.
func (sc *ShareChain) dropWorkLocked(removed []string, rescan bool) {
	var seeds []string
	for _, h := range removed {
		seeds = append(seeds, sc.children[h]...)
	}
	for _, h := range sc.updateWorkLocked(seeds) {
		if rescan {
			break
		}
		rescan = sc.isTipCandidateLocked(h)
	}
	if rescan {
		sc.selectTipLocked()
	}
}

func (sc *ShareChain) linkLocked(h string, sh *Share) {
	if p := sc.prevKeyLocked(sh); p != "" {
		sc.children[p] = append(sc.children[p], h)
	}
}

func (sc *ShareChain) prevKeyLocked(sh *Share) string {
	if p := sh.ShareInfo.ShareData.PreviousShareHash; p == nil || p.IsEqual(sc.genesisHash) {
		return ""
	}
	return sh.getPrevKey()
}

// updateWorkLocked sets the cumulative work and length of the held shares in hs and
// all their descendants, each to its held parent's plus its own, and returns the
// shares it updated.
func (sc *ShareChain) updateWorkLocked(hs []string) []string {
	dirty := make(map[string]bool, len(hs))
	var all []string
	for _, h := range hs {
		if sc.shares[h] != nil && !dirty[h] {
			dirty[h] = true
			all = append(all, h)
		}
	}
	for i := 0; i < len(all); i++ {
		for _, c := range sc.children[all[i]] {
			if !dirty[c] {
				dirty[c] = true
				all = append(all, c)
			}
		}
	}
	var q []string
	for _, h := range all {
		if p := sc.prevKeyLocked(sc.shares[h]); p == "" || !dirty[p] {
			q = append(q, h)
		}
	}
	for i := 0; i < len(q); i++ {
		h := q[i]
		sh := sc.shares[h]
		var pw float64
		var pl int
		if p := sc.prevKeyLocked(sh); p != "" && sc.shares[p] != nil {
			pw, pl = sc.cumWork[p], sc.chainLen[p]
		}
		d := sh.getTargetDiff()
		inc := 1
		if sh.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
			d = 0
			inc = 0
		} else if d < MinPoolShareDiff {
			d = MinPoolShareDiff
		}
		sc.cumWork[h], sc.chainLen[h] = pw+d, pl+inc
		q = append(q, sc.children[h]...)
	}
	return all
}

func (sc *ShareChain) isTipCandidateLocked(h string) bool {
	sh := sc.shares[h]
	return sc.verified[h] && sh != nil && sh.Hash != nil && sh.ShareInfo.ShareData.StaleInfo == StaleInfoNone
}

func betterTip(w float64, h string, bestW float64, bestS string) bool {
	return w > bestW+1e-9 || (w >= bestW-1e-9 && h > bestS)
}

// selectTipLocked sets the tip to the verified, non-stale share with the most work,
// or leaves it if there is none.
func (sc *ShareChain) selectTipLocked() {
	var bestH *chainhash.Hash
	var bestW float64
	var bestS string
	for h, sh := range sc.shares {
		if sc.isTipCandidateLocked(h) && (bestH == nil || betterTip(sc.cumWork[h], h, bestW, bestS)) {
			bestH, bestW, bestS = sh.Hash, sc.cumWork[h], h
		}
	}
	if bestH != nil {
		sc.TipHash = bestH
	}
}

// considerTipLocked moves the tip to any of hs that beats it. Between selections
// work only grows and candidates are only added, so this picks what
// selectTipLocked would.
func (sc *ShareChain) considerTipLocked(hs []string) {
	var bestH *chainhash.Hash
	var bestW float64
	var bestS string
	if sc.TipHash != nil {
		if t := sc.TipHash.String(); sc.isTipCandidateLocked(t) {
			bestH, bestW, bestS = sc.TipHash, sc.cumWork[t], t
		}
	}
	for _, h := range hs {
		if sc.isTipCandidateLocked(h) && (bestH == nil || betterTip(sc.cumWork[h], h, bestW, bestS)) {
			bestH, bestW, bestS = sc.shares[h].Hash, sc.cumWork[h], h
		}
	}
	if bestH != nil {
		sc.TipHash = bestH
	}
}

func (sc *ShareChain) hasCompletePPLNSWindow(prev *chainhash.Hash) bool {
	if prev == nil || prev.IsEqual(sc.genesisHash) {
		return true
	}
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	pDiff := sc.getExpectedShareDifficultyLocked(prev)
	maxD := pDiff * 4.0
	if maxD < 16.0 {
		maxD = 16.0
	}
	h := prev.String()
	maxSteps := len(sc.shares) + 1
	var first *Share
	var capW float64
	var cut int64
	var acc float64
	validCount := 0
	for steps := 0; steps < maxSteps && h != ""; steps++ {
		s := sc.shares[h]
		if s == nil {
			return false
		}
		if s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			if first == nil {
				first = s
				capW = float64(DefaultPPLNSWindow) * pDiff
				if first.MinHeader.Bits != 0 {
					bd := TargetToDiff(blockchain.CompactToBig(first.MinHeader.Bits))
					if bd > 0 && capW > bd*12.0 {
						capW = bd * 12.0
					}
				}
				cut = int64(first.ShareInfo.Timestamp) - MaxPPLNSWindowAgeSecs
			}
			validCount++
			if int64(s.ShareInfo.Timestamp) < cut {
				return true
			}
			d := sc.GetEffectiveShareDiff(s)
			if d > 0 {
				if d > maxD {
					d = maxD
				}
				acc += d
				if acc >= capW {
					return true
				}
			}
			if validCount >= DefaultPPLNSWindow {
				return true
			}
		}
		p := s.ShareInfo.ShareData.PreviousShareHash
		if p == nil || p.IsEqual(sc.genesisHash) {
			return true
		}
		h = s.getPrevKey()
	}
	return false
}

func (sc *ShareChain) verifyPending() int {
	sc.verifyMu.Lock()
	defer sc.verifyMu.Unlock()
	sc.mutex.Lock()
	var pend []*Share
	for h, sh := range sc.shares {
		if !sc.verified[h] && sh != nil && sh.Hash != nil {
			pend = append(pend, sh)
		}
	}
	if len(pend) == 0 {
		sc.mutex.Unlock()
		return 0
	}
	sort.Slice(pend, func(i, j int) bool {
		return sc.chainLen[pend[i].Hash.String()] < sc.chainLen[pend[j].Hash.String()]
	})
	sc.mutex.Unlock()
	vn := 0
	var rejected []string
	for i, sh := range pend {
		h, p := sh.Hash.String(), sh.ShareInfo.ShareData.PreviousShareHash
		sc.mutex.RLock()
		if sc.verified[h] {
			sc.mutex.RUnlock()
			continue
		}
		gen := p == nil || p.IsEqual(sc.genesisHash)
		pVer := !gen && sc.verified[p.String()]
		pIn := !gen && sc.shares[p.String()] != nil
		hLen := 0
		if pIn {
			hLen = sc.chainLen[p.String()]
		}
		sc.mutex.RUnlock()
		if !gen && !pVer && hLen < TargetLookbehind {
			continue
		}
		_ = i
		sc.mutex.RLock()
		eff := sc.effectiveTimeLocked(sh, p)
		sc.mutex.RUnlock()
		if eff >= ConsensusV2ActivationTime && !sc.checkConsensus(sh, p, hLen, eff) {
			sc.mutex.Lock()
			sc.unlinkShareLocked(h)
			rejected = append(rejected, h)
			sc.changes++
			sc.mutex.Unlock()
			continue
		}
		sc.mutex.Lock()
		sc.markVerifiedLocked(h)
		sc.mutex.Unlock()
		vn++
	}
	if len(rejected) > 0 {
		// Descendants of rejected shares keep their work until the pass ends, so
		// later shares in the pass see the lengths they were sorted by.
		sc.mutex.Lock()
		sc.dropWorkLocked(rejected, false)
		sc.mutex.Unlock()
	}
	return vn
}

func (sc *ShareChain) markVerifiedLocked(h string) {
	sc.verified[h] = true
	sc.considerTipLocked([]string{h})
}

// effectiveTimeLocked returns the later of a share's timestamp and its parent's, so a
// share cannot opt out of rules that activated before its parent by backdating itself.
func (sc *ShareChain) effectiveTimeLocked(s *Share, p *chainhash.Hash) int64 {
	t := int64(s.ShareInfo.Timestamp)
	if p != nil && !p.IsEqual(sc.genesisHash) {
		if ps := sc.shares[p.String()]; ps != nil && int64(ps.ShareInfo.Timestamp) > t {
			t = int64(ps.ShareInfo.Timestamp)
		}
	}
	return t
}

// medianTimePastLocked returns the median timestamp of the MedianTimeShares non-stale
// shares ending at p, and false if fewer are held.
func (sc *ShareChain) medianTimePastLocked(p *chainhash.Hash) (int64, bool) {
	if p == nil || p.IsEqual(sc.genesisHash) {
		return 0, false
	}
	w := sc.getOrderedChainWalkLocked(p, MedianTimeShares)
	if len(w) < MedianTimeShares {
		return 0, false
	}
	ts := make([]int64, len(w))
	for i, s := range w {
		ts[i] = int64(s.ShareInfo.Timestamp)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	return ts[len(ts)/2], true
}

func (sc *ShareChain) checkConsensus(s *Share, p *chainhash.Hash, pLen int, eff int64) bool {

	h := s.Hash.String()
	if eff >= V2MandatoryActivationTime && s.Type < ShareVersionV2 {
		logging.Warnf("ShareChain: Rejected share %s: outdated share version", h[:12])
		return false
	}
	sc.mutex.RLock()
	mtp, mtpOK := sc.medianTimePastLocked(p)
	sc.mutex.RUnlock()
	if eff >= ConsensusV3ActivationTime && mtpOK && int64(s.ShareInfo.Timestamp) < mtp {
		logging.Warnf("ShareChain: Rejected share %s: timestamp %d before median time past %d", h[:12], s.ShareInfo.Timestamp, mtp)
		return false
	}
	if s.ShareInfo.ShareData.StaleInfo != StaleInfoNone {
		return true
	}
	sc.mutex.RLock()
	w := sc.getOrderedChainWalkLocked(p, TargetLookbehind)
	sc.mutex.RUnlock()
	if len(w) >= TargetLookbehind && int64(w[TargetLookbehind-1].ShareInfo.Timestamp) >= ConsensusV2ActivationTime {
		expD, clmD := sc.GetExpectedShareDifficulty(p), s.getTargetDiff()
		pi := new(big.Int).SetBytes(s.POWHash.CloneBytes())
		actD, netT := TargetToDiff(pi), blockchain.CompactToBig(s.MinHeader.Bits)
		if (actD < expD*0.999 || clmD < expD*0.999 || clmD > expD*1.001) && pi.Cmp(netT) > 0 {
			logging.Warnf("ShareChain: Rejected share %s: diff %.4f != consensus %.4f", h[:12], clmD, expD)
			return false
		}
	}
	if pLen >= TargetLookbehind && sc.hasCompletePPLNSWindow(p) && eff >= ConsensusV2ActivationTime+MaxPPLNSWindowAgeSecs {
		if ok, r := VerifyShareCoinbase(s, sc); !ok {
			logging.Warnf("ShareChain: Rejected share %s: %s", h[:12], r)
			return false
		}
	}
	return true
}

// GetMissingParentHashes returns up to max parent hashes the sharechain lacks, like
// p2pool's desired set: the missing parent at the end of each chain whose head is
// newer than maxShareAge and less than a full PPLNS window plus lookbehind deep.
func (sc *ShareChain) GetMissingParentHashes(max int) []string {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	cut := time.Now().Add(-maxShareAge).Unix()
	tails := make(map[string]string)
	seen := make(map[string]bool)
	var out []string
	for _, h := range sc.headsLocked() {
		sh := sc.shares[h]
		if int64(sh.ShareInfo.Timestamp) < cut || sc.chainLen[h] >= DefaultPPLNSWindow+TargetLookbehind {
			continue
		}
		if t := sc.missingTailLocked(h, tails); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
			if max > 0 && len(out) >= max {
				break
			}
		}
	}
	return out
}

// missingTailLocked returns the missing parent hash at the end of the chain below h,
// or "" if the chain reaches genesis. tails memoizes results across calls.
func (sc *ShareChain) missingTailLocked(h string, tails map[string]string) string {
	var path []string
	tail := ""
	for cur := h; ; {
		if t, ok := tails[cur]; ok {
			tail = t
			break
		}
		sh := sc.shares[cur]
		if sh == nil {
			tail = cur
			break
		}
		path = append(path, cur)
		p := sh.ShareInfo.ShareData.PreviousShareHash
		if p == nil || p.IsEqual(sc.genesisHash) || len(path) > len(sc.shares) {
			break
		}
		cur = sh.getPrevKey()
	}
	for _, p := range path {
		tails[p] = tail
	}
	return tail
}

// headsLocked returns the hashes of shares that no other share builds on, most
// cumulative work first.
func (sc *ShareChain) headsLocked() []string {
	hasChild := make(map[string]bool, len(sc.shares))
	for _, sh := range sc.shares {
		if sh != nil {
			if p := sh.getPrevKey(); p != "" {
				hasChild[p] = true
			}
		}
	}
	var heads []string
	for h, sh := range sc.shares {
		if sh != nil && !hasChild[h] {
			heads = append(heads, h)
		}
	}
	sort.Slice(heads, func(i, j int) bool {
		wi, wj := sc.cumWork[heads[i]], sc.cumWork[heads[j]]
		if wi != wj {
			return wi > wj
		}
		return heads[i] > heads[j]
	})
	return heads
}

// GetStopHashes returns up to max hashes for a share request's stops, like p2pool's
// download_shares: each chain head and its 10th ancestor, or its oldest one held.
func (sc *ShareChain) GetStopHashes(max int) []string {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	seen := make(map[string]bool)
	var out []string
	add := func(h string) {
		if !seen[h] && (max <= 0 || len(out) < max) {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, h := range sc.headsLocked() {
		if max > 0 && len(out) >= max {
			break
		}
		add(h)
		anc := h
		for i := 0; i < 10; i++ {
			p := sc.shares[anc].getPrevKey()
			if p == "" || sc.shares[p] == nil {
				break
			}
			anc = p
		}
		add(anc)
	}
	return out
}

// MaxShareReply caps the number of shares returned for one share request.
const MaxShareReply = 1000

// GetSharesByHashWithParents returns each requested share followed by up to parents of
// its ancestors, like p2pool's sharereq. A walk ends before any hash in stops, and the
// reply holds at most MaxShareReply shares.
func (sc *ShareChain) GetSharesByHashWithParents(hashes []string, parents int, stops []string) []Share {
	if len(hashes) == 0 {
		return nil
	}
	if parents < 0 {
		parents = 0
	}
	if p := MaxShareReply / len(hashes); parents > p {
		parents = p
	}
	stop := make(map[string]bool, len(stops))
	for _, h := range stops {
		stop[h] = true
	}
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	seen := make(map[string]bool)
	var out []Share
	for _, cur := range hashes {
		for i := 0; i <= parents && cur != "" && len(out) < MaxShareReply; i++ {
			if seen[cur] || stop[cur] || sc.shares[cur] == nil {
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

func (sc *ShareChain) SetCurrentBlockTarget(prevHash, bitsHex string) {
	if config.Active.LightMode {
		sc.SetNetworkBitsFromTemplate(bitsHex)
	}
	var b uint32
	_, _ = fmt.Sscanf(bitsHex, "%x", &b)
	sc.mutex.Lock()
	sc.curPrevBlock = prevHash
	if b != 0 {
		sc.curBits = b
	}
	sc.mutex.Unlock()
}
func (sc *ShareChain) SetNetworkBitsFromTemplate(bitsHex string) {
	var b uint32
	if _, err := fmt.Sscanf(bitsHex, "%x", &b); err == nil && b != 0 {
		nd := BitsToNetworkDiff(b)
		if nd > 0 {
			sc.poolStatsMutex.Lock()
			sc.networkDifficulty = nd
			sc.networkHashrate = (nd * 4294967296.0) / 150.0
			sc.poolStatsMutex.Unlock()
		}
	}
}

func (sc *ShareChain) KnowsBlockHash(blockHash string) bool {
	if blockHash == "" {
		return false
	}
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	if len(sc.verified) == 0 {
		return false
	}
	if s := sc.shares[blockHash]; s != nil && sc.verified[blockHash] {
		return true
	}
	for h, s := range sc.shares {
		if !sc.verified[h] || s == nil {
			continue
		}
		if s.MinHeader.PreviousBlock != nil && s.MinHeader.PreviousBlock.String() == blockHash {
			return true
		}
	}
	return false
}

func (sc *ShareChain) GetRecentNetworkBits() uint32 {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	if sc.TipHash != nil {
		if s := sc.shares[sc.TipHash.String()]; s != nil && s.MinHeader.Bits != 0 {
			return s.MinHeader.Bits
		}
	}
	for h, s := range sc.shares {
		if sc.verified[h] && s != nil && s.MinHeader.Bits != 0 {
			return s.MinHeader.Bits
		}
	}
	return 0
}

func (sc *ShareChain) GetRandomChallengeBlockHash() string {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	var candidates []string
	seen := make(map[string]bool)
	tipPrev := ""
	if sc.TipHash != nil {
		if ts := sc.shares[sc.TipHash.String()]; ts != nil && ts.MinHeader.PreviousBlock != nil {
			tipPrev = ts.MinHeader.PreviousBlock.String()
		}
	}
	for h, s := range sc.shares {
		if !sc.verified[h] || s == nil || s.MinHeader.PreviousBlock == nil {
			continue
		}
		pb := s.MinHeader.PreviousBlock.String()
		if pb == "" || pb == tipPrev || seen[pb] || sc.shares[pb] != nil {
			continue
		}
		seen[pb] = true
		candidates = append(candidates, pb)
		if len(candidates) >= 64 {
			break
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	idx := int(time.Now().UnixNano()&0x7fffffff) % len(candidates)
	return candidates[idx]
}
