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
	if hr > 1000000000 {
		return fmt.Sprintf("%.2f GH/s", hr/1000000000)
	}
	if hr > 1000000 {
		return fmt.Sprintf("%.2f MH/s", hr/1000000)
	}
	if hr > 1000 {
		return fmt.Sprintf("%.2f kH/s", hr/1000)
	}
	return fmt.Sprintf("%.2f H/s", hr)
}

func formatDuration(sec float64) string {
	if sec > 3600*24 {
		return fmt.Sprintf("%.2f days", sec/(3600*24))
	}
	if sec > 3600 {
		return fmt.Sprintf("%.2f hours", sec/3600)
	}
	if sec > 60 {
		return fmt.Sprintf("%.2f minutes", sec/60)
	}
	return fmt.Sprintf("%.2f seconds", sec)
}

func logStats(pm *p2p.PeerManager, sc *work.ShareChain, ss *stratum.StratumServer) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		stats := sc.GetStats()
		localRate := ss.GetLocalHashrate()
		
		logging.Infof("P2Pool: %d shares in chain (%d verified/%d total) Peers: %d", stats.SharesTotal, stats.SharesTotal, stats.SharesTotal, pm.GetPeerCount())
		logging.Infof(" Local: %s Expected time to share: %s", formatHashrate(localRate), formatDuration(stats.TimeToBlock))
		logging.Infof("  Shares: %d (%d orphan, %d dead) Efficiency: %.2f%% Current payout: %.4f VTC", stats.SharesTotal, stats.SharesOrphan, stats.SharesDead, stats.Efficiency, stats.CurrentPayout)
		logging.Infof("    Pool: %s Expected time to block: %s", formatHashrate(stats.PoolHashrate), formatDuration(stats.TimeToBlock))
	}
}

func main() {
	logging.Infof("!!!!!!!!!! RUNNING LATEST CORRECT VERSION - 10 !!!!!!!!!!!")
	logging.SetLogLevel(int(logging.LogLevelDebug))
	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	config.LoadConfig()
	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet)

	rpcClient := rpc.NewClient(config.Active)
	sc := work.NewShareChain(rpcClient)
	sc.Load()

	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()

	pm := p2p.NewPeerManager(p2pnet.ActiveNetwork, sc)
	// CORRECTED: Comment out the listener to prevent "address already in use" error
	// go pm.ListenForPeers()

	stratumServer := stratum.NewStratumServer(workManager, pm)
	go stratumServer.ListenForMiners()

	go logStats(pm, sc, stratumServer)

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			logging.Infof("Committing sharechain to disk...")
			if err := sc.Commit(); err != nil {
				logging.Warnf("Could not commit sharechain to disk: %v", err)
			}
		}
	}()

	for {
		time.Sleep(60 * time.Second)
	}
}
