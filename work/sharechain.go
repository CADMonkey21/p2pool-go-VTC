package work

import (
	"encoding/json"
	"math/big"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
)

const maxShareAge = 72 * time.Hour

type PoolStats struct {
	PoolHashrate    float64 `json:"pool_hashrate"`
	NetworkHashrate float64 `json:"network_hashrate"`
	SharesTotal     int     `json:"shares_total"`
	SharesOrphan    int     `json:"shares_orphan"`
	SharesDead      int     `json:"shares_dead"`
	Efficiency      float64 `json:"efficiency"`
	TimeToBlock     float64 `json:"time_to_block_seconds"`
}

type ShareChain struct {
	shares           map[string]*Share
	mutex            sync.RWMutex
	rpcClient        *rpc.Client
	genesisHash      *chainhash.Hash
	TipHash          *chainhash.Hash
	FoundBlockChan   chan *Share
	poolHashrate     float64
	networkHashrate  float64
	poolStatsMutex   sync.RWMutex
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
	ticker := time.NewTicker(30 * time.Second)
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
	defer sc.mutex.Unlock()
	added := 0
	for i := range newShares {
		s := newShares[i]
		if s.Hash == nil { continue }
		hStr := s.Hash.String()
		if _, exists := sc.shares[hStr]; !exists {
			sc.shares[hStr] = &s
			added++
			sc.updateTip(&s)
		}
	}
	return added
}

func (sc *ShareChain) updateTip(s *Share) {
	if sc.TipHash == nil || sc.TipHash.IsEqual(sc.genesisHash) {
		sc.TipHash = s.Hash
		return
	}
	if s.ShareInfo.AbsHeight > sc.GetShare(sc.TipHash.String()).ShareInfo.AbsHeight {
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
		if os.IsNotExist(err) { return nil }
		return err
	}
	var fileShares []Share
	if err := json.Unmarshal(b, &fileShares); err != nil { return err }
	sc.AddShares(fileShares)
	logging.Infof("Loaded %d shares from disk", len(fileShares))
	return nil
}

func (sc *ShareChain) Commit() error {
	sc.mutex.RLock()
	sharesToSave := make([]Share, 0, len(sc.shares))
	for _, s := range sc.shares { sharesToSave = append(sharesToSave, *s) }
	sc.mutex.RUnlock()

	path := "sharechain.json"
	b, err := json.MarshalIndent(sharesToSave, "", "  ")
	if err != nil { return err }
	return os.WriteFile(path, b, 0644)
}

func (sc *ShareChain) GetSharesForPayout(blockFindShareHash *chainhash.Hash, windowSize int) ([]*Share, error) {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	var payoutShares []*Share
	currentHash := blockFindShareHash.String()
	sharesGathered := 0

	for sharesGathered < windowSize {
		share, exists := sc.shares[currentHash]
		if !exists { break }
		if share.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			payoutShares = append(payoutShares, share)
			sharesGathered++
		}
		if share.ShareInfo.ShareData.PreviousShareHash == nil || share.ShareInfo.ShareData.PreviousShareHash.IsEqual(sc.genesisHash) {
			break
		}
		currentHash = share.ShareInfo.ShareData.PreviousShareHash.String()
	}
	return payoutShares, nil
}

func (sc *ShareChain) updateStats() {
	sc.mutex.RLock()
	
	// Quick slice so we can release lock
	shares := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares { shares = append(shares, s) }
	sc.mutex.RUnlock()

	orphanCount := 0
	doaCount := 0

	now := time.Now().Unix()
	cutoff24h := now - (24 * 3600)
	var work24h float64

	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	for _, s := range shares {
		switch s.ShareInfo.ShareData.StaleInfo {
		case StaleInfoOrphan: orphanCount++
		case StaleInfoDOA: doaCount++
		}

		if int64(s.ShareInfo.Timestamp) > cutoff24h && s.ShareInfo.ShareData.StaleInfo == StaleInfoNone {
			if s.Target != nil && s.Target.Sign() > 0 {
				difficulty := new(big.Int).Div(maxTarget, s.Target)
				diffFloat, _ := difficulty.Float64()
				work24h += diffFloat
			}
		}
	}

	sc.poolStatsMutex.Lock()
	sc.poolHashrate = (work24h * 16777216) / float64(24*3600)

	info, err := sc.rpcClient.GetMiningInfo()
	if err == nil {
		sc.networkHashrate = info.NetworkHashPS
	}
	sc.poolStatsMutex.Unlock()
}

func (sc *ShareChain) GetStats() PoolStats {
	sc.mutex.RLock()
	total := len(sc.shares)
	sc.mutex.RUnlock()

	orphan := 0
	doa := 0
	sc.mutex.RLock()
	for _, s := range sc.shares {
		switch s.ShareInfo.ShareData.StaleInfo {
		case StaleInfoOrphan: orphan++
		case StaleInfoDOA: doa++
		}
	}
	sc.mutex.RUnlock()

	eff := 100.0
	if total > 0 { eff = float64(total-orphan-doa) / float64(total) * 100.0 }

	sc.poolStatsMutex.RLock()
	pH := sc.poolHashrate
	nH := sc.networkHashrate
	sc.poolStatsMutex.RUnlock()

	ttb := 0.0
	if pH > 0 && nH > 0 { ttb = (nH / pH) * 150.0 }

	return PoolStats{
		PoolHashrate:    pH,
		NetworkHashrate: nH,
		SharesTotal:     total,
		SharesOrphan:    orphan,
		SharesDead:      doa,
		Efficiency:      eff,
		TimeToBlock:     ttb,
	}
}

func (sc *ShareChain) GetRecentShares(count int) ([]*Share, error) {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	shareList := make([]*Share, 0, len(sc.shares))
	for _, s := range sc.shares { shareList = append(shareList, s) }

	sort.Slice(shareList, func(i, j int) bool {
		return shareList[i].ShareInfo.Timestamp > shareList[j].ShareInfo.Timestamp
	})

	if count > len(shareList) { count = len(shareList) }
	return shareList[:count], nil
}

func (sc *ShareChain) GetTipHash() *chainhash.Hash {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.TipHash
}
