package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/stratum"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

const dashboardTemplate = `
<!DOCTYPE html>
<html>
<head>
    <title>P2Pool-Go-VTC Dashboard</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Oxygen', 'Ubuntu', 'Cantarell', 'Fira Sans', 'Droid Sans', 'Helvetica Neue', sans-serif; background-color: #121212; color: #e0e0e0; }
        pre { background-color: #1e1e1e; padding: 1em; border-radius: 5px; white-space: pre-wrap; word-wrap: break-word; }
        #status { position: fixed; top: 10px; right: 10px; font-style: italic; color: #888; }
    </style>
</head>
<body>
    <h1>P2Pool-Go-VTC Dashboard</h1>
    <div id="status">Loading...</div>
    <pre id="json-container"></pre>

    <script>
        const statusDiv = document.getElementById('status');
        const jsonContainer = document.getElementById('json-container');
        const refreshInterval = 5000; // 5 seconds

        function fetchData() {
            statusDiv.textContent = 'Refreshing...';
            fetch('/?json=1')
                .then(response => response.json())
                .then(data => {
                    jsonContainer.textContent = JSON.stringify(data, null, 2); // Pretty print with 2-space indent
                    statusDiv.textContent = 'Last updated: ' + new Date().toLocaleTimeString();
                })
                .catch(error => {
                    console.error('Error fetching stats:', error);
                    statusDiv.textContent = 'Error fetching stats. See console.';
                });
        }

        // Fetch data on initial load
        fetchData();

        // Set interval to refresh data
        setInterval(fetchData, refreshInterval);
    </script>
</body>
</html>
`

// [NEW] DashboardHandler struct to hold dependencies and the cache
type DashboardHandler struct {
	wm           *work.WorkManager
	pm           *p2p.PeerManager
	ss           *stratum.StratumServer
	startTime    time.Time
	htmlTemplate *template.Template
	cacheMutex   sync.RWMutex
	cachedStats  *DashboardStats
}

// [NEW] statUpdater runs in a separate goroutine, caching stats every 15 seconds
func (dh *DashboardHandler) statUpdater() {
	ticker := time.NewTicker(15 * time.Second) // Refresh stats every 15 seconds
	defer ticker.Stop()

	for {
		<-ticker.C
		logging.Debugf("WEB: Re-caching dashboard stats...")
		newStats := dh.buildStats()

		dh.cacheMutex.Lock()
		dh.cachedStats = newStats
		dh.cacheMutex.Unlock()
		logging.Debugf("WEB: Dashboard stats re-caching complete.")
	}
}

// [NEW] buildStats performs the expensive calculations.
// This is now only called once every 15 seconds.
func (dh *DashboardHandler) buildStats() *DashboardStats {
	chainStats := dh.wm.ShareChain.GetStats()
	stratumClients := dh.ss.GetClients()

	// [OPTIMIZATION]
	// Call GetProjectedPayouts ONCE.
	// We will re-use this map for both the Top 50 list and the
	// individual miner payout estimates to avoid re-calculating.
	allPayouts, _ := dh.wm.ShareChain.GetProjectedPayouts(50)

	activeMiners := make([]MinerStats, 0, len(stratumClients))
	for _, client := range stratumClients {
		if !client.Authorized {
			continue
		}

		hashrate := dh.ss.GetHashrateForClient(client.ID)
		rejectedRate := client.GetRejectedRate()
		avgShareTime := client.GetAverageShareTime()

		// [OPTIMIZATION] Use the map we already fetched.
		payout := allPayouts[client.WorkerName]
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

	recentBlocks, _ := dh.wm.GetRecentBlocks(20)
	blocksFound := make([]BlockFoundStats, len(recentBlocks))
	for i, block := range recentBlocks {
		blocksFound[i] = BlockFoundStats{
			BlockNumber: int64(block.BlockHeight),
			FoundAgo:    formatDuration(time.Since(block.FoundTime)),
		}
	}

	// [OPTIMIZATION] Use the map we already fetched.
	payoutsList := make([]PayoutStats, 0, len(allPayouts))
	for addr, amount := range allPayouts {
		payoutsList = append(payoutsList, PayoutStats{Address: addr, Payout: amount})
	}
	sort.Slice(payoutsList, func(i, j int) bool {
		return payoutsList[i].Payout > payoutsList[j].Payout
	})
	// Limit to top 50 if necessary (though GetProjectedPayouts already did)
	if len(payoutsList) > 50 {
		payoutsList = payoutsList[:50]
	}

	lastBlock := dh.wm.GetLastBlockFoundTime()
	lastBlockAgo := "Never"
	if !lastBlock.IsZero() {
		lastBlockAgo = formatDuration(time.Since(lastBlock))
	}

	latestTmpl := dh.wm.GetLatestTemplate()
	reward := 0.0
	if latestTmpl != nil {
		reward = float64(latestTmpl.CoinbaseValue) / 1e8
	}

	stats := &DashboardStats{
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
		BlocksFound24h:   dh.wm.GetBlocksFoundInLast(24 * time.Hour),

		NodeUptime:        formatDuration(time.Since(dh.startTime)),
		LocalNodeHashrate: formatHashrate(dh.ss.GetLocalHashrate()), // <-- [FIXED] Wrapped in formatHashrate()
		ConnectedMiners:    len(stratumClients),
		MinShareDifficulty: config.Active.Vardiff.MinDiff,
		GoRoutines:         runtime.NumGoroutine(),

		ActiveMiners: activeMiners,
		BlocksFound:  blocksFound,
		Payouts:      payoutsList,
	}

	return stats
}

// [MODIFIED] NewDashboard now returns the handler struct
func NewDashboard(wm *work.WorkManager, pm *p2p.PeerManager, ss *stratum.StratumServer, startTime time.Time) http.Handler {
	// Parse the HTML template once on startup
	tmpl, err := template.New("dashboard").Parse(dashboardTemplate)
	if err != nil {
		// If the template fails to parse, we must panic as the server can't function.
		panic(fmt.Sprintf("failed to parse dashboard template: %v", err))
	}

	handler := &DashboardHandler{
		wm:           wm,
		pm:           pm,
		ss:           ss,
		startTime:    startTime,
		htmlTemplate: tmpl,
	}

	// [NEW] Prime the cache on startup
	handler.cachedStats = handler.buildStats()

	// [NEW] Start the caching goroutine
	go handler.statUpdater()

	return handler
}

// [NEW] ServeHTTP makes DashboardHandler satisfy the http.Handler interface
func (dh *DashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if the request is for the JSON data API
	if r.URL.Query().Get("json") == "1" {
		w.Header().Set("Content-Type", "application/json")

		// [OPTIMIZATION]
		// Get a Read Lock, copy the cached pointer, and release.
		// This is extremely fast and blocks for almost no time.
		dh.cacheMutex.RLock()
		statsToServe := dh.cachedStats
		dh.cacheMutex.RUnlock()

		if statsToServe == nil {
			// This should only happen in the first few moments of startup
			http.Error(w, `{"error": "Stats are being generated, please try again in a moment."}`, http.StatusServiceUnavailable)
			return
		}

		// Marshal with indentation for pretty-printing
		prettyJSON, err := json.MarshalIndent(statsToServe, "", "  ")
		if err != nil {
			http.Error(w, "Failed to generate stats", http.StatusInternalServerError)
			return
		}
		w.Write(prettyJSON)

	} else {
		// Otherwise, serve the HTML page
		w.Header().Set("Content-Type", "text/html")
		err := dh.htmlTemplate.Execute(w, nil)
		if err != nil {
			http.Error(w, "Failed to render dashboard", http.StatusInternalServerError)
		}
	}
}

// formatDuration is a helper to make time intervals human-readable.
func formatDuration(d time.Duration) string {
	sec := d.Seconds()
	if sec <= 0 {
		return "N/A"
	}
	switch {
	case sec > 86400*2:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	case sec > 3600*2:
		return fmt.Sprintf("%.1f hours", d.Hours())
	case sec > 60*2:
		return fmt.Sprintf("%.1f minutes", d.Minutes())
	default:
		return fmt.Sprintf("%.1f seconds", d.Seconds())
	}
}

// CORRECTED: This function now matches the one in main.go and includes TH/s.
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
