package web

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/stratum"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

type Dashboard struct {
	workManager *work.WorkManager
	p2pNode     *p2p.Node // Updated from PeerManager to Node
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

func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sc := d.workManager.ShareChain
	poolStats := sc.GetStats()
	localHashrate := d.stratum.GetLocalHashrate()
	
	// Get Peer Count from libp2p
	peerCount := 0
	if d.p2pNode != nil {
		peerCount = len(d.p2pNode.Host.Network().Peers())
	}

	recentBlocks, _ := d.workManager.GetRecentBlocks(10)
	var dashboardBlocks []map[string]interface{}
	for _, b := range recentBlocks {
		dashboardBlocks = append(dashboardBlocks, map[string]interface{}{
			"hash":   b.BlockHash.String(),
			"height": b.BlockHeight,
			"found":  b.FoundTime.Unix(),
			"status": int(b.State),
		})
	}

	recentShares, _ := sc.GetRecentShares(10)
	var dashboardShares []map[string]interface{}
	for _, s := range recentShares {
		status := "valid"
		if s.ShareInfo.ShareData.StaleInfo != work.StaleInfoNone {
			status = "stale"
		}
		dashboardShares = append(dashboardShares, map[string]interface{}{
			"hash":   s.Hash.String(),
			"miner":  string(s.ShareInfo.ShareData.PubKeyHash),
			"found":  s.ShareInfo.Timestamp,
			"status": status,
		})
	}

	activeMiners := d.stratum.GetClients()
	var dashboardMiners []map[string]interface{}
	for _, m := range activeMiners {
		m.Mutex.Lock()
		hr := d.stratum.GetHashrateForClient(m.ID)
		dashboardMiners = append(dashboardMiners, map[string]interface{}{
			"address":  m.WorkerName,
			"hashrate": hr,
			"accepted": m.AcceptedShares,
			"rejected": m.RejectedShares,
		})
		m.Mutex.Unlock()
	}

	data := map[string]interface{}{
		"system": map[string]interface{}{
			"uptime":     time.Since(d.startTime).Seconds(),
			"goroutines": runtime.NumGoroutine(),
			"peers":      peerCount,
			"version":    "1.0-libp2p",
		},
		"pool": map[string]interface{}{
			"hashrate":          poolStats.PoolHashrate,
			"network_hashrate":  poolStats.NetworkHashrate,
			"efficiency":        poolStats.Efficiency,
			"time_to_block":     poolStats.TimeToBlock,
			"total_shares":      poolStats.SharesTotal,
		},
		"local": map[string]interface{}{
			"hashrate":      localHashrate,
			"miner_count":   len(activeMiners),
			"shares_per_s":  d.stratum.GetLocalSharesPerSecond(),
			"efficiency":    d.stratum.GetLocalEfficiency(),
		},
		"recent_blocks": dashboardBlocks,
		"recent_shares": dashboardShares,
		"miners":        dashboardMiners,
	}

	json.NewEncoder(w).Encode(data)
}
