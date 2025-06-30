package main

import (
	"fmt"
	"os"
	"time"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/rpc"
	"github.com/gertjaap/p2pool-go/stratum"
	"github.com/gertjaap/p2pool-go/work"
)

func formatHashrate(hr float64) string {
	switch {
	case hr > 1e9:
		return fmt.Sprintf("%.2f GH/s", hr/1e9)
	case hr > 1e6:
		return fmt.Sprintf("%.2f MH/s", hr/1e6)
	case hr > 1e3:
		return fmt.Sprintf("%.2f kH/s", hr/1e3)
	default:
		return fmt.Sprintf("%.2f H/s", hr)
	}
}

func formatDuration(sec float64) string {
	switch {
	case sec > 86400:
		return fmt.Sprintf("%.2f days", sec/86400)
	case sec > 3600:
		return fmt.Sprintf("%.2f hours", sec/3600)
	case sec > 60:
		return fmt.Sprintf("%.2f minutes", sec/60)
	default:
		return fmt.Sprintf("%.2f seconds", sec)
	}
}

func logStats(pm *p2p.PeerManager, sc *work.ShareChain, ss *stratum.StratumServer) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		stats := sc.GetStats()
		local := ss.GetLocalHashrate()
		logging.Infof("P2Pool: %d shares in chain (%d verified/%d total)  |  Peers: %d",
			stats.SharesTotal, stats.SharesTotal, stats.SharesTotal, pm.GetPeerCount())
		logging.Infof(" Local: %s   Expected share ≈ %s",
			formatHashrate(local), formatDuration(stats.TimeToBlock))
		logging.Infof(" Shares: %d (%d orphan, %d dead)  Efficiency: %.2f %%  |  Payout: %.4f VTC",
			stats.SharesTotal, stats.SharesOrphan, stats.SharesDead, stats.Efficiency, stats.CurrentPayout)
		logging.Infof("  Pool: %s   Expected block ≈ %s",
			formatHashrate(stats.PoolHashrate), formatDuration(stats.TimeToBlock))
	}
}

func main() {
	logging.Infof("🚀  p2pool‑go (alt‑port build) starting up")
	logging.SetLogLevel(int(logging.LogLevelDebug))

	logFile, _ := os.OpenFile("p2pool.log",
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	// ------------------------------------------------------------------------
	// 1) Load config & network params
	// ------------------------------------------------------------------------
	logging.Debugf("MAIN: Loading configuration...")
	config.LoadConfig()
	logging.Debugf("MAIN: Setting network parameters for '%s'...", config.Active.Network)
	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet)
	logging.Debugf("MAIN: Verthash initialized and network set.")

	// ------------------------------------------------------------------------
	// 2) **Override just the listen/advertise port**
	// ------------------------------------------------------------------------
	p2pnet.ActiveNetwork.P2PPort = 19172 // <— our new inbound port
	logging.Debugf("MAIN: P2P listen port override set to %d.", p2pnet.ActiveNetwork.P2PPort)

	// ------------------------------------------------------------------------
	// 3) Spin everything up
	// ------------------------------------------------------------------------
	logging.Debugf("MAIN: Initializing RPC client...")
	rpcClient := rpc.NewClient(config.Active)

	logging.Debugf("MAIN: Initializing ShareChain...")
	sc := work.NewShareChain(rpcClient)
	logging.Debugf("MAIN: Loading sharechain from shares.dat...")
	err := sc.Load()
	if err != nil {
		logging.Fatalf("MAIN: Failed to load sharechain: %v", err)
	}
	logging.Debugf("MAIN: Sharechain loading complete.")

	logging.Debugf("MAIN: Initializing WorkManager...")
	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()
	logging.Debugf("MAIN: WorkManager is watching for new block templates.")

	logging.Debugf("MAIN: Initializing PeerManager...")
	pm := p2p.NewPeerManager(p2pnet.ActiveNetwork, sc)
	go pm.ListenForPeers()
	logging.Debugf("MAIN: PeerManager is listening for peers.")

	logging.Debugf("MAIN: Initializing StratumServer...")
	stratumSrv := stratum.NewStratumServer(workManager, pm)
	go stratumSrv.ListenForMiners()
	logging.Debugf("MAIN: StratumServer is listening for miners.")

	go logStats(pm, sc, stratumSrv)

	// periodic share‑db flush
	go func() {
		tick := time.NewTicker(5 * time.Minute)
		for range tick.C {
			logging.Infof("Committing sharechain to disk …")
			if err := sc.Commit(); err != nil {
				logging.Warnf("Could not commit sharechain: %v", err)
			}
		}
	}()

	logging.Infof("MAIN: Startup complete. Running...")
	// keep the main goroutine alive
	for {
		time.Sleep(1 * time.Hour)
	}
}
