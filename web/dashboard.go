package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/CADMonkey21/p2pool-go-vtc/config"
	"github.com/CADMonkey21/p2pool-go-vtc/p2p"
	"github.com/CADMonkey21/p2pool-go-vtc/stratum"
	"github.com/CADMonkey21/p2pool-go-vtc/work"
)

// formatDuration is a helper to make time intervals human-readable.
func formatDuration(d time.Duration) string {
	if d.Hours() > 48 {
		return fmt.Sprintf("%.0f days, %d hours", d.Hours()/24, int(d.Hours())%24)
	}
	if d.Minutes() > 120 {
		return fmt.Sprintf("%.0f hours, %d minutes", d.Hours(), int(d.Minutes())%60)
	}
	if d.Seconds() > 120 {
		return fmt.Sprintf("%.0f minutes, %d seconds", d.Minutes(), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%.0f seconds", d.Seconds())
}

// formatHashrate is a helper to make hashrate values human-readable.
func formatHashrate(hr float64) string {
	switch {
	case hr >= 1e9:
		return fmt.Sprintf("%.2f GH/s", hr/1e9)
	case hr >= 1e6:
		return fmt.Sprintf("%.2f MH/s", hr/1e6)
	case hr >= 1e3:
		return fmt.Sprintf("%.2f kH/s", hr/1e3)
	default:
		return fmt.Sprintf("%.2f H/s", hr)
	}
}

// DashboardStats is the main structure for the API response, containing all stats.
type DashboardStats struct {
	GlobalNetworkHashrate string  `json:"global_network_hashrate"`
	P2PoolNetworkHashrate string  `json:"p2pool_network_hashrate"`
	NetworkDifficulty     float64 `json:"network_difficulty"`
	BlockReward           float64 `json:"block_reward"`
	LastBlockFoundAgo     string  `json:"last_block_found_ago"`

	PoolEfficiency    string `json:"pool_efficiency"`
	TimeToBlock       string `json:"pool_time_to_block"`
	PoolSharesTotal   int    `json:"pool_shares_total"`
	PoolSharesOrphan  int    `json:"pool_shares_orphan"`
	PoolSharesDead    int    `json:"pool_shares_dead"`
	BlocksFound24h    int    `json:"pool_blocks_found_24h"`

	NodeUptime         string  `json:"node_uptime"`
	LocalNodeHashrate  string  `json:"local_node_hashrate"`
	ConnectedMiners    int     `json:"connected_miners"`
	MinShareDifficulty float64 `json:"min_share_difficulty"`
	GoRoutines         int     `json:"go_routines"`

	ActiveMiners []MinerStats      `json:"active_miners"`
	BlocksFound  []BlockFoundStats `json:"blocks_found_list"`
	Payouts      []PayoutStats     `json:"payouts_list"`
}

type MinerStats struct {
	Address            string  `json:"address"`
	Hashrate           string  `json:"hashrate"`
	RejectedPercentage float64 `json:"rejected_percentage"`
	ShareDifficulty    float64 `json:"share_difficulty"`
	AvgTimeToShare     string  `json:"avg_time_to_share"`
	Est24HourPayout    float64 `json:"est_24_hour_payout_vtc"`
}

type BlockFoundStats struct {
	BlockNumber int64  `json:"block_number"`
	FoundAgo    string `json:"found_ago"`
}

type PayoutStats struct {
	Address string  `json:"address"`
	Payout  float64 `json:"payout_vtc"`
}

func NewDashboard(wm *work.WorkManager, pm *p2p.PeerManager, ss *stratum.StratumServer, startTime time.Time) http.Handler {
	_ = pm // Acknowledge unused variable to satisfy compiler
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		chainStats := wm.ShareChain.GetStats()
		stratumClients := ss.GetClients()

		activeMiners := make([]MinerStats, 0, len(stratumClients))
		for _, client := range stratumClients {
			if !client.Authorized {
				continue
			}

			hashrate := ss.GetHashrateForClient(client.ID)
			rejectedRate := client.GetRejectedRate()
			avgShareTime := client.GetAverageShareTime()

			payout, _ := wm.ShareChain.GetProjectedPayoutForAddress(client.WorkerName)
			est24hPayout := 0.0
			if chainStats.TimeToBlock > 0 {
				est24hPayout = payout * (86400 / chainStats.TimeToBlock)
			}

			activeMiners = append(activeMiners, MinerStats{
				Address:            client.WorkerName,
				Hashrate:           formatHashrate(hashrate),
				RejectedPercentage: rejectedRate * 100,
				ShareDifficulty:    client.CurrentDifficulty,
				AvgTimeToShare:     formatDuration(avgShareTime),
				Est24HourPayout:    est24hPayout,
			})
		}

		recentBlocks, _ := wm.GetRecentBlocks(20)
		blocksFound := make([]BlockFoundStats, len(recentBlocks))
		for i, block := range recentBlocks {
			blocksFound[i] = BlockFoundStats{
				BlockNumber: int64(block.BlockHeight),
				FoundAgo:    formatDuration(time.Since(block.FoundTime)),
			}
		}

		payoutProjections, _ := wm.ShareChain.GetProjectedPayouts(50)
		payoutsList := make([]PayoutStats, 0, len(payoutProjections))
		for addr, amount := range payoutProjections {
			payoutsList = append(payoutsList, PayoutStats{Address: addr, Payout: amount})
		}
		sort.Slice(payoutsList, func(i, j int) bool {
			return payoutsList[i].Payout > payoutsList[j].Payout
		})

		lastBlock := wm.GetLastBlockFoundTime()
		lastBlockAgo := "Never"
		if !lastBlock.IsZero() {
			lastBlockAgo = formatDuration(time.Since(lastBlock))
		}

		tmpl := wm.GetLatestTemplate()
		reward := 0.0
		if tmpl != nil {
			reward = float64(tmpl.CoinbaseValue) / 1e8
		}

		s := DashboardStats{
			GlobalNetworkHashrate: formatHashrate(chainStats.NetworkHashrate),
			P2PoolNetworkHashrate: formatHashrate(chainStats.PoolHashrate),
			NetworkDifficulty:     chainStats.NetworkDifficulty,
			BlockReward:           reward,
			LastBlockFoundAgo:     lastBlockAgo,

			PoolEfficiency:   fmt.Sprintf("%.2f%%", chainStats.Efficiency),
			TimeToBlock:      formatDuration(time.Duration(chainStats.TimeToBlock) * time.Second),
			PoolSharesTotal:  chainStats.SharesTotal,
			PoolSharesOrphan: chainStats.SharesOrphan,
			PoolSharesDead:   chainStats.SharesDead,
			BlocksFound24h:   wm.GetBlocksFoundInLast(24 * time.Hour),

			NodeUptime:         formatDuration(time.Since(startTime)),
			LocalNodeHashrate:  formatHashrate(ss.GetLocalHashrate()),
			ConnectedMiners:    len(stratumClients),
			MinShareDifficulty: config.Active.Vardiff.MinDiff,
			GoRoutines:         runtime.NumGoroutine(),

			ActiveMiners: activeMiners,
			BlocksFound:  blocksFound,
			Payouts:      payoutsList,
		}
		_ = json.NewEncoder(w).Encode(s)
	})
}
