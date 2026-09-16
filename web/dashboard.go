package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/stratum"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

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
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%d days", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%.0f minutes", d.Minutes())
}

// formatAvgTime matches the specific "0h 5m 0s" format expected by the UI
func formatAvgTime(seconds float64) string {
	if seconds <= 0 {
		return "Unknown"
	}
	if seconds > 86400*7 { // Cap at 1 week for extremely low hashrate miners
		return "> 1 week"
	}
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh %dm %ds", h, m, s)
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
		minShareDiff = 0.05
	}

	// Calculate baseline reward metrics for payout estimations
	poolFeePct := config.Active.Fee / 100.0
	netBlockReward := blockReward * (1.0 - poolFeePct)
	expectedBlocks24h := 0.0
	if poolStats.TimeToBlock > 0 {
		expectedBlocks24h = (24.0 * 3600.0) / poolStats.TimeToBlock
	}

	var dashboardMiners []map[string]interface{}
	var dashboardPayouts []map[string]interface{}

	for _, m := range activeMiners {
		hr := d.stratum.GetHashrateForClient(m.ID)

		m.Mutex.Lock()
		workerName := m.WorkerName
		accepted := m.AcceptedShares
		rejected := m.RejectedShares
		m.Mutex.Unlock()

		totalShares := accepted + rejected
		rejPct := 0.0
		if totalShares > 0 {
			rejPct = (float64(rejected) / float64(totalShares)) * 100.0
		}

		// Calculate Payout Estimations for this miner
		minerProportion := 0.0
		effectivePoolHashrate := poolStats.PoolHashrate
		
		// [FIX] Cold-start sanity check: If local real-time hashrate temporarily 
		// exceeds the rolling pool average, use local hashrate as the baseline.
		if hr > effectivePoolHashrate {
			effectivePoolHashrate = hr
		}

		if effectivePoolHashrate > 0 {
			minerProportion = hr / effectivePoolHashrate
		}
		
		payoutPerBlock := minerProportion * netBlockReward
		est24HourPayout := payoutPerBlock * expectedBlocks24h

		// Calculate Average Time to Share theoretically
		timeToShareSec := 0.0
		if hr > 0 && poolStats.NetworkDifficulty > 0 && poolStats.NetworkHashrate > 0 {
			timeToShareSec = (minShareDiff / poolStats.NetworkDifficulty) * 150.0 * (poolStats.NetworkHashrate / hr)
		}

		dashboardMiners = append(dashboardMiners, map[string]interface{}{
			"address":                workerName,
			"hashrate":               formatHashrate(hr),
			"rejected_percentage":    rejPct,
			"share_difficulty":       minShareDiff,
			"avg_time_to_share":      formatAvgTime(timeToShareSec),
			"est_24_hour_payout_vtc": est24HourPayout,
		})

		dashboardPayouts = append(dashboardPayouts, map[string]interface{}{
			"address":    workerName,
			"payout_vtc": payoutPerBlock,
		})
	}

	recentBlocks, _ := d.workManager.GetRecentBlocks(10)
	var dashboardBlocks []map[string]interface{}
	for _, b := range recentBlocks {
		dashboardBlocks = append(dashboardBlocks, map[string]interface{}{
			"block_number": b.BlockHeight,
			"found_ago":    formatDurationAgo(b.FoundTime),
		})
	}

	data := map[string]interface{}{
		"node_uptime":             formatUptime(uptimeSec),
		"network_difficulty":      poolStats.NetworkDifficulty,
		"connected_miners":        len(activeMiners),
		"last_block_found_ago":    formatDurationAgo(lastBlockTime),
		"pool_fee":                config.Active.Fee,
		"global_network_hashrate": formatHashrate(poolStats.NetworkHashrate),
		"p2pool_network_hashrate": formatHashrate(poolStats.PoolHashrate),
		"local_node_hashrate":     formatHashrate(localHashrate),
		"pool_shares_total":       poolStats.SharesTotal,
		"pool_shares_orphan":      poolStats.SharesOrphan,
		"pool_shares_dead":        poolStats.SharesDead,
		"pool_blocks_found_24h":   d.workManager.GetBlocksFoundInLast(24 * time.Hour),
		"block_reward":            blockReward,
		"min_share_difficulty":    minShareDiff,
		"pool_time_to_block":      formatTimeToBlock(poolStats.TimeToBlock),
		"stratum_port":            config.Active.StratumPort,
		"active_miners":           dashboardMiners,
		"payouts_list":            dashboardPayouts,
		"blocks_found_list":       dashboardBlocks,
	}

	json.NewEncoder(w).Encode(data)
}
