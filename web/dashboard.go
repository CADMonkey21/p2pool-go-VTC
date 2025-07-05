package web

import (
	"encoding/json"
	"net/http"
	"runtime"

	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/stratum"
	"github.com/gertjaap/p2pool-go/work"
)

type status struct {
	Peers             int     `json:"peers"`
	GoRoutines        int     `json:"go_routines"`
	LocalHashrate     float64 `json:"local_hashrate_hs"`
	LocalSharesPS     float64 `json:"local_shares_ps"`
	PoolHashrate      float64 `json:"pool_hashrate_hs"`
	PoolEfficiency    float64 `json:"pool_efficiency_percent"`
	SharesTotal       int     `json:"pool_shares_total"`
	SharesOrphan      int     `json:"pool_shares_orphan"`
	SharesDead        int     `json:"pool_shares_dead"`
	TimeToBlockSecs   float64 `json:"pool_time_to_block_secs"`
	NetworkHashrate   float64 `json:"network_hashrate_hs"`
	NetworkDifficulty float64 `json:"network_difficulty"`
}

// NewDashboard returns a trivial JSON status page at "/".
func NewDashboard(wm *work.WorkManager, pm *p2p.PeerManager, ss *stratum.StratumServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		stats := wm.ShareChain.GetStats()
		s := status{
			Peers:             pm.GetPeerCount(),
			GoRoutines:        runtime.NumGoroutine(),
			LocalHashrate:     ss.GetLocalHashrate(),
			LocalSharesPS:     ss.GetLocalSharesPerSecond(),
			PoolHashrate:      stats.PoolHashrate,
			PoolEfficiency:    stats.Efficiency,
			SharesTotal:       stats.SharesTotal,
			SharesOrphan:      stats.SharesOrphan,
			SharesDead:        stats.SharesDead,
			TimeToBlockSecs:   stats.TimeToBlock,
			NetworkHashrate:   stats.NetworkHashrate,
			NetworkDifficulty: stats.NetworkDifficulty,
		}
		_ = json.NewEncoder(w).Encode(s)
	})
}
