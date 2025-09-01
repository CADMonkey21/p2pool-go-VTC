package work

import (
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/CADMonkey21/p2pool-go-VTC/wire"
)

const hashrateConstant = 4294967296 // 2^32

type PeerManager interface {
	Broadcast(msg wire.P2PoolMessage)
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
	SharesChannel      chan []wire.Share
	NeedShareChannel   chan *chainhash.Hash
	FoundBlockChan     chan *wire.Share
	Tip                *ChainShare
	Tail               *ChainShare
	AllShares          map[string]*ChainShare
	AllSharesByPrev    map[string]*ChainShare
	disconnectedShares []*wire.Share
	rpcClient          *rpc.Client
	pm                 PeerManager
	chainLock          sync.Mutex
}

type ChainShare struct {
	Share    *wire.Share
	Previous *ChainShare
	Next     *ChainShare
}

func NewShareChain(client *rpc.Client) *ShareChain {
	sc := &ShareChain{
		disconnectedShares: make([]*wire.Share, 0),
		AllSharesByPrev:    make(map[string]*ChainShare),
		AllShares:          make(map[string]*ChainShare),
		SharesChannel:      make(chan []wire.Share, 10),
		NeedShareChannel:   make(chan *chainhash.Hash, 10),
		FoundBlockChan:     make(chan *wire.Share, 10),
		rpcClient:          client,
	}
	go sc.ReadShareChan()
	return sc
}

func (sc *ShareChain) SetPeerManager(pm PeerManager) {
	sc.pm = pm
}

func (sc *ShareChain) ReadShareChan() {
	for s := range sc.SharesChannel {
		sc.AddShares(s)
	}
}

func (sc *ShareChain) addChainShare(newChainShare *ChainShare) {
	sc.AllShares[newChainShare.Share.Hash.String()] = newChainShare
	if newChainShare.Share.ShareInfo.ShareData.PreviousShareHash != nil {
		sc.AllSharesByPrev[newChainShare.Share.ShareInfo.ShareData.PreviousShareHash.String()] = newChainShare
	}
}

func (sc *ShareChain) resolve(skipCommit bool) {
	logging.Debugf("Resolving sharechain")
	if len(sc.disconnectedShares) == 0 {
		return
	}

	if sc.Tip == nil {
		if len(sc.disconnectedShares) > 0 {
			newChainShare := &ChainShare{Share: sc.disconnectedShares[0]}
			sc.Tip = newChainShare
			sc.disconnectedShares = sc.disconnectedShares[1:]
			sc.addChainShare(newChainShare)
			sc.Tail = sc.Tip
		}
	}

	for {
		extended := false
		newDisconnectedShares := make([]*wire.Share, 0)
		for _, s := range sc.disconnectedShares {
			if _, ok := sc.AllShares[s.Hash.String()]; ok {
				continue
			}

			if s.ShareInfo.ShareData.PreviousShareHash == nil {
				newDisconnectedShares = append(newDisconnectedShares, s)
				continue
			}

			if es, ok := sc.AllShares[s.ShareInfo.ShareData.PreviousShareHash.String()]; ok {
				newChainShare := &ChainShare{Share: s, Previous: es}
				es.Next = newChainShare
				if es.Share.Hash.IsEqual(sc.Tip.Share.Hash) {
					sc.Tip = newChainShare
				}
				sc.addChainShare(newChainShare)
				extended = true
			} else if es, ok := sc.AllSharesByPrev[s.Hash.String()]; ok {
				newChainShare := &ChainShare{Share: s, Next: es}
				es.Previous = newChainShare
				if es.Share.Hash.IsEqual(sc.Tail.Share.Hash) {
					sc.Tail = newChainShare
				}
				sc.addChainShare(newChainShare)
				extended = true
			} else {
				newDisconnectedShares = append(newDisconnectedShares, s)
			}
		}

		sc.disconnectedShares = newDisconnectedShares
		if !extended || len(sc.disconnectedShares) == 0 {
			break
		}
	}

	if sc.Tip != nil {
		logging.Debugf("Tip is now %s - disconnected: %d - Length: %d", sc.Tip.Share.Hash.String(), len(sc.disconnectedShares), len(sc.AllShares))
	}

	if len(sc.AllShares) < config.Active.PPLNSWindow && sc.Tail != nil && sc.Tail.Share.ShareInfo.ShareData.PreviousShareHash != nil {
		sc.NeedShareChannel <- sc.Tail.Share.ShareInfo.ShareData.PreviousShareHash
	}
	if !skipCommit {
		sc.commit()
	}
}

func (sc *ShareChain) commit() error {
	shares := make([]wire.Share, 0, len(sc.AllShares))
	s := sc.Tip
	for s != nil {
		shares = append(shares, *(s.Share))
		s = s.Previous
	}

	f, err := os.Create("sharechain-new.dat")
	if err != nil {
		return err
	}
	defer f.Close()

	if err := wire.WriteShares(f, shares); err != nil {
		return err
	}

	if _, err := os.Stat("sharechain.dat"); err == nil {
		if err := os.Remove("sharechain.dat"); err != nil {
			return err
		}
	}

	return os.Rename("sharechain-new.dat", "sharechain.dat")
}

func (sc *ShareChain) Commit() error {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()
	return sc.commit()
}

func (sc *ShareChain) Load() error {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

	if _, err := os.Stat("sharechain.dat"); os.IsNotExist(err) {
		return nil
	}

	f, err := os.Open("sharechain.dat")
	if err != nil {
		return err
	}
	defer f.Close()

	shares, err := wire.ReadShares(f)
	if err != nil {
		return err
	}

	for i := range shares {
		s := &shares[i]
		s.CalculateHashes()
	}

	sc.disconnectedShares = make([]*wire.Share, len(shares))
	for i := range shares {
		sc.disconnectedShares[i] = &shares[i]
	}

	logging.Debugf("Loaded %d shares from disk", len(sc.disconnectedShares))
	sc.resolve(true)
	return nil
}

func (sc *ShareChain) AddShares(s []wire.Share) {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

	for i := range s {
		share := s[i]
		share.CalculateHashes()
		if _, ok := sc.AllShares[share.Hash.String()]; !ok {
			sc.disconnectedShares = append(sc.disconnectedShares, &share)
		}
	}

	sc.resolve(false)
}

func (sc *ShareChain) GetTipHash() *chainhash.Hash {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

	if sc.Tip != nil {
		return sc.Tip.Share.Hash
	}
	return &chainhash.Hash{}
}

func (sc *ShareChain) GetStats() ChainStats {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

	stats := ChainStats{
		SharesOrphan: len(sc.disconnectedShares),
		SharesTotal:  len(sc.AllShares),
	}

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

	powLimit := p2pnet.ActiveChainConfig.PowLimit
	if powLimit == nil || powLimit.Sign() == 0 {
		logging.Warnf("ActiveChainConfig.PowLimit is nil or zero; falling back to compact 0x1d00ffff")
		powLimit = blockchain.CompactToBig(0x1d00ffff)
	}

	var earliestShareTime time.Time = time.Now()

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
		difficulty := new(big.Int).Div(new(big.Int).Set(powLimit), shareTarget)
		totalDifficulty.Add(totalDifficulty, difficulty)

		t := time.Unix(int64(current.Share.ShareInfo.Timestamp), 0)
		if t.Before(earliestShareTime) {
			earliestShareTime = t
		}

		current = current.Previous
	}

	stats.SharesDead = deadShares
	if sharesInWindow > 0 {
		stats.Efficiency = 100 * (1 - (float64(stats.SharesDead) / float64(sharesInWindow)))
	} else if stats.SharesOrphan == 0 && stats.SharesTotal > 0 {
		stats.Efficiency = 100.0
	}

	elapsedSeconds := lookbackDuration.Seconds()
	if sharesInWindow > 0 {
		measured := time.Since(earliestShareTime).Seconds()
		if measured < 1.0 {
			measured = 1.0
		} else if measured > lookbackDuration.Seconds() {
			measured = lookbackDuration.Seconds()
		}
		elapsedSeconds = measured
	}

	const hrConst = float64(4294967296) // 2^32

	if totalDifficulty.Sign() > 0 {
		totalDifficultyFloat := new(big.Float).SetInt(totalDifficulty)
		hashrateFloat := new(big.Float).Quo(
			new(big.Float).Mul(totalDifficultyFloat, big.NewFloat(hrConst)),
			big.NewFloat(elapsedSeconds),
		)
		stats.PoolHashrate, _ = hashrateFloat.Float64()
	}

	if stats.PoolHashrate > 0 && stats.NetworkDifficulty > 0 {
		stats.TimeToBlock = (stats.NetworkDifficulty * hrConst) / stats.PoolHashrate
	}

	logging.Debugf("[DIAG] GetStats: sharesWindow=%d earliest=%v elapsed=%.2fs totalDifficulty=%s powLimit=%s poolHashrate=%.6f H/s",
		sharesInWindow,
		earliestShareTime.Format(time.RFC3339),
		elapsedSeconds,
		totalDifficulty.String(),
		powLimit.Text(16),
		stats.PoolHashrate,
	)

	stats.CurrentPayout = 0.0
	return stats
}

func (sc *ShareChain) GetSharesForPayout(blockFindShareHash *chainhash.Hash, windowSize int) ([]*wire.Share, error) {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

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

func (sc *ShareChain) GetProjectedPayouts(limit int) (map[string]float64, error) {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

	tip := sc.Tip
	if tip == nil {
		return nil, fmt.Errorf("sharechain has no tip, cannot calculate payouts")
	}

	totalPayout := uint64(12.5 * 1e8)
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
	powLimit := p2pnet.ActiveChainConfig.PowLimit

	for _, share := range payoutShares {
		shareTarget := blockchain.CompactToBig(share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			continue
		}
		difficulty := new(big.Int).Div(new(big.Int).Set(powLimit), shareTarget)
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
		difficulty := new(big.Int).Div(new(big.Int).Set(powLimit), shareTarget)

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

func (sc *ShareChain) GetShare(hashStr string) *wire.Share {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()
	if cs, ok := sc.AllShares[hashStr]; ok {
		return cs.Share
	}
	return nil
}

func (sc *ShareChain) GetNeededHashes() []*chainhash.Hash {
	sc.chainLock.Lock()
	defer sc.chainLock.Unlock()

	needed := make(map[chainhash.Hash]bool)
	for _, s := range sc.disconnectedShares {
		if s.ShareInfo.ShareData.PreviousShareHash != nil {
			if _, exists := sc.AllShares[s.ShareInfo.ShareData.PreviousShareHash.String()]; !exists {
				needed[*s.ShareInfo.ShareData.PreviousShareHash] = true
			}
		}
	}

	if len(needed) == 0 {
		return nil
	}

	hashes := make([]*chainhash.Hash, 0, len(needed))
	for h := range needed {
		hashCopy := h
		hashes = append(hashes, &hashCopy)
	}
	return hashes
}
