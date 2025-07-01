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
	Peers          int     `json:"peers"`
	LocalHashrate  float64 `json:"local_hashrate_hs"`
	LocalSharesPS  float64 `json:"local_shares_ps"`
	PoolHashrate   float64 `json:"pool_hashrate_hs"`
	GoRoutines     int     `json:"go_routines"`
	ShareChainSize int     `json:"share_chain_len"`
}

// NewDashboard returns a trivial JSON status page at "/".
func NewDashboard(wm *work.WorkManager, pm *p2p.PeerManager, ss *stratum.StratumServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats := wm.ShareChain.GetStats()
		s := status{
			Peers:          pm.GetPeerCount(),
			LocalHashrate:  ss.GetLocalHashrate(),
			LocalSharesPS:  ss.GetLocalSharesPerSecond(),
			PoolHashrate:   stats.PoolHashrate,
			GoRoutines:     runtime.NumGoroutine(),
			ShareChainSize: stats.SharesTotal,
		}
		_ = json.NewEncoder(w).Encode(s)
	})
}

