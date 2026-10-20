package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/stratum"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

const hashrateConstant = 16777216.0 // 2^24 hashes per 1.0 difficulty

type Dashboard struct {
	workManager *work.WorkManager
	p2pNode     *p2p.Node
	stratum     *stratum.StratumServer
	startTime   time.Time
}

func NewDashboard(wm *work.WorkManager, node *p2p.Node, strat *stratum.StratumServer, start time.Time) *Dashboard {
	return &Dashboard{
		workManager: wm,
		p2pNode:     node,
		stratum:     strat,
		startTime:   start,
	}
}

func formatHashrate(hr float64) string {
	switch {
	case hr > 1e12:
		return fmt.Sprintf("%.2f TH/s", hr/1e12)
	case hr > 1e9:
		return fmt.Sprintf("%.2f GH/s", hr/1e9)
	case hr > 1e6:
		return fmt.Sprintf("%.2f MH/s", hr/1e6)
	case hr > 1e3:
		return fmt.Sprintf("%.2f kH/s", hr/1e3)
	default:
		return fmt.Sprintf("%.2f H/s", hr)
	}
}

func formatUptime(sec float64) string {
	if sec <= 0 {
		return "0 seconds"
	}
	d := time.Duration(sec) * time.Second
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d days %d hours", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d hours %d minutes", hours, minutes)
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func formatDurationAgo(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	d := time.Since(t)
	switch {
	case d.Hours() > 48:
		return fmt.Sprintf("%.0f days ago", d.Hours()/24)
	case d.Hours() >= 1:
		return fmt.Sprintf("%.0f hours ago", d.Hours())
	case d.Minutes() >= 1:
		return fmt.Sprintf("%.0f minutes ago", d.Minutes())
	default:
		return fmt.Sprintf("%.0f seconds ago", d.Seconds())
	}
}

func formatTimeToBlock(seconds float64) string {
	if seconds <= 0 {
		return "Unknown"
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case seconds > 86400*2:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	case seconds >= 3600:
		return fmt.Sprintf("%.1f hours", d.Hours())
	case seconds >= 60:
		return fmt.Sprintf("%.1f minutes", d.Minutes())
	default:
		return fmt.Sprintf("%.1f seconds", d.Seconds())
	}
}

func formatAvgTime(seconds float64) string {
	if seconds <= 0 {
		return "Unknown"
	}
	if seconds > 86400*7 {
		return "> 1 week"
	}
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if s == 0 && seconds > 0 && h == 0 && m == 0 {
		return fmt.Sprintf("0h 0m %.1fs", seconds)
	}
	return fmt.Sprintf("%dh %dm %ds", h, m, s)
}

type aggregatedMiner struct {
	address       string
	totalHashrate float64
	totalAccepted uint64
	totalRejected uint64
	currentDiff   float64
}

func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sc := d.workManager.ShareChain
	poolStats := sc.GetStats()
	localHashrate := d.stratum.GetLocalHashrate()

	uptimeSec := time.Since(d.startTime).Seconds()
	activeMiners := d.stratum.GetClients()
	lastBlockTime := d.workManager.GetLastBlockFoundTime()

	blockReward := d.workManager.GetCurrentBlockReward()
	minShareDiff := config.Active.Vardiff.MinDiff
	if minShareDiff <= 0 {
		minShareDiff = 0.01
	}

	// 1. Aggregate ONLY local Stratum client connections for the "Active Miners" table
	minerMap := make(map[string]*aggregatedMiner)
	var defaultMinerAddr string
	for _, m := range activeMiners {
		hr := d.stratum.GetHashrateForClient(m.ID)

		m.Mutex.Lock()
		workerName := m.WorkerName
		accepted := m.AcceptedShares
		rejected := m.RejectedShares
		curDiff := m.CurrentDifficulty
		m.Mutex.Unlock()

		if workerName == "" {
			workerName = "Unknown"
		} else if defaultMinerAddr == "" {
			defaultMinerAddr = workerName
		}
		if curDiff <= 0 {
			curDiff = minShareDiff
		}

		if _, exists := minerMap[workerName]; !exists {
			minerMap[workerName] = &aggregatedMiner{
				address:     workerName,
				currentDiff: curDiff,
			}
		}
		minerMap[workerName].totalHashrate += hr
		minerMap[workerName].totalAccepted += accepted
		minerMap[workerName].totalRejected += rejected
		if curDiff > minerMap[workerName].currentDiff {
			minerMap[workerName].currentDiff = curDiff
		}
	}

	// 2. Calculate true P2Pool Network Hashrate combining local + remote P2P ShareChain miners
	netMiners := sc.GetNetworkMiners(10 * time.Minute)
	var networkMinersHashrate float64
	for addr, nm := range netMiners {
		if localMiner, isLocal := minerMap[addr]; isLocal && localMiner.totalHashrate > nm.Hashrate {
			networkMinersHashrate += localMiner.totalHashrate
		} else {
			networkMinersHashrate += nm.Hashrate
		}
	}
	for addr, lm := range minerMap {
		if _, inNet := netMiners[addr]; !inNet {
			networkMinersHashrate += lm.totalHashrate
		}
	}

	effectivePoolHashrate := poolStats.PoolHashrate
	if networkMinersHashrate > effectivePoolHashrate {
		effectivePoolHashrate = networkMinersHashrate
	}
	if localHashrate > effectivePoolHashrate {
		effectivePoolHashrate = localHashrate
	}

	effectiveTimeToBlock := poolStats.TimeToBlock
	if effectivePoolHashrate > 0 && poolStats.NetworkHashrate > 0 {
		effectiveTimeToBlock = (poolStats.NetworkHashrate / effectivePoolHashrate) * 150.0
	}

	poolFeePct := config.Active.Fee / 100.0
	netBlockReward := blockReward * (1.0 - poolFeePct)
	expectedBlocks24h := 0.0
	if effectiveTimeToBlock > 0 {
		expectedBlocks24h = (24.0 * 3600.0) / effectiveTimeToBlock
	}

	// 3. Compute real PPLNS payouts from the synchronized ShareChain (includes all nodes' miners)
	totalRewardSats := int64(math.Round(blockReward * 1e8))
	if defaultMinerAddr == "" {
		defaultMinerAddr = config.Active.PoolAddress
	}
	pplnsOutputs := work.CalculatePPLNSOutputs(totalRewardSats, defaultMinerAddr, sc)
	pplnsPayoutMap := make(map[string]float64)
	for _, out := range pplnsOutputs {
		pplnsPayoutMap[out.Address] = float64(out.Amount) / 1e8
	}

	var dashboardMiners []map[string]interface{}
	var dashboardPayouts []map[string]interface{}

	sortedMinerAddrs := make([]string, 0, len(minerMap))
	for addr := range minerMap {
		sortedMinerAddrs = append(sortedMinerAddrs, addr)
	}
	sort.Strings(sortedMinerAddrs)

	for _, addr := range sortedMinerAddrs {
		am := minerMap[addr]
		totalShares := am.totalAccepted + am.totalRejected
		rejPct := 0.0
		if totalShares > 0 {
			rejPct = (float64(am.totalRejected) / float64(totalShares)) * 100.0
		}

		payoutPerBlock, hasPPLNS := pplnsPayoutMap[am.address]
		if !hasPPLNS {
			minerProportion := 0.0
			if effectivePoolHashrate > 0 {
				minerProportion = am.totalHashrate / effectivePoolHashrate
				if minerProportion > 1.0 {
					minerProportion = 1.0
				}
			}
			payoutPerBlock = minerProportion * netBlockReward
			if payoutPerBlock > 0 {
				pplnsPayoutMap[am.address] = payoutPerBlock
			}
		}

		est24HourPayout := payoutPerBlock * expectedBlocks24h

		timeToShareSec := 0.0
		if am.totalHashrate > 0 {
			timeToShareSec = (am.currentDiff * hashrateConstant) / am.totalHashrate
		}

		dashboardMiners = append(dashboardMiners, map[string]interface{}{
			"address":                am.address,
			"hashrate":               formatHashrate(am.totalHashrate),
			"rejected_percentage":    rejPct,
			"share_difficulty":       am.currentDiff,
			"avg_time_to_share":      formatAvgTime(timeToShareSec),
			"payout_per_block_vtc":   payoutPerBlock,
			"est_24_hour_payout_vtc": est24HourPayout,
		})
	}

	type payoutItem struct {
		addr   string
		amount float64
	}
	var allPayouts []payoutItem
	for addr, amt := range pplnsPayoutMap {
		if addr != "" && addr != "Unknown" && amt > 0 {
			allPayouts = append(allPayouts, payoutItem{addr: addr, amount: amt})
		}
	}
	sort.Slice(allPayouts, func(i, j int) bool {
		if allPayouts[i].amount == allPayouts[j].amount {
			return allPayouts[i].addr < allPayouts[j].addr
		}
		return allPayouts[i].amount > allPayouts[j].amount
	})
	if len(allPayouts) > 50 {
		allPayouts = allPayouts[:50]
	}
	for _, p := range allPayouts {
		dashboardPayouts = append(dashboardPayouts, map[string]interface{}{
			"address":    p.addr,
			"payout_vtc": p.amount,
		})
	}

	recentBlocks, _ := d.workManager.GetRecentBlocks(50)
	var dashboardBlocks []map[string]interface{}
	for _, b := range recentBlocks {
		if time.Since(b.FoundTime) <= 24*time.Hour {
			dashboardBlocks = append(dashboardBlocks, map[string]interface{}{
				"block_number": b.BlockHeight,
				"found_ago":    formatDurationAgo(b.FoundTime),
			})
		}
	}

	data := map[string]interface{}{
		"node_uptime":             formatUptime(uptimeSec),
		"network_difficulty":      poolStats.NetworkDifficulty,
		"connected_miners":        len(minerMap),
		"last_block_found_ago":    formatDurationAgo(lastBlockTime),
		"pool_fee":                config.Active.Fee,
		"global_network_hashrate": formatHashrate(poolStats.NetworkHashrate),
		"p2pool_network_hashrate": formatHashrate(effectivePoolHashrate),
		"local_node_hashrate":     formatHashrate(localHashrate),
		"pool_shares_total":       poolStats.SharesTotal,
		"pool_shares_orphan":      poolStats.SharesOrphan,
		"pool_shares_dead":        poolStats.SharesDead,
		"pool_blocks_found_24h":   d.workManager.GetBlocksFoundInLast(24 * time.Hour),
		"block_reward":            blockReward,
		"min_share_difficulty":    minShareDiff,
		"pool_time_to_block":      formatTimeToBlock(effectiveTimeToBlock),
		"stratum_port":            config.Active.StratumPort,
		"active_miners":           dashboardMiners,
		"payouts_list":            dashboardPayouts,
		"blocks_found_list":       dashboardBlocks,
	}

	json.NewEncoder(w).Encode(data)
}


// ServeStatus provides a lightweight readiness and sync check endpoint for OCM integration.
func (d *Dashboard) ServeStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	peerCount := 0
	if d.p2pNode != nil && d.p2pNode.Host != nil {
		peerCount = len(d.p2pNode.Host.Network().Peers())
	}

	sc := d.workManager.ShareChain
	stats := sc.GetStats()

	d.workManager.TemplateMutex.Lock()
	var currentHeight int64
	hasTemplate := len(d.workManager.Templates) > 0
	for _, tpl := range d.workManager.Templates {
		if tpl != nil && tpl.Height > currentHeight {
			currentHeight = tpl.Height
		}
	}
	d.workManager.TemplateMutex.Unlock()

	ready := hasTemplate && (config.Active.SoloMode || peerCount > 0 || stats.SharesTotal > 0)

	resp := map[string]interface{}{
		"ready":          ready,
		"has_template":   hasTemplate,
		"synced":         ready,
		"peers":          peerCount,
		"shares_total":   stats.SharesTotal,
		"height":         currentHeight,
		"stratum_port":   config.Active.StratumPort,
		"p2p_port":       config.Active.P2PPort,
		"network":        config.Active.Network,
		"testnet":        config.Active.Testnet,
		"uptime_seconds": int64(time.Since(d.startTime).Seconds()),
	}
	_ = json.NewEncoder(w).Encode(resp)
}
