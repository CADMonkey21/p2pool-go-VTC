package main

import (
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

func main() {
	logging.Infof("!!!!!!!!!! RUNNING LATEST CORRECT VERSION - 10 !!!!!!!!!!!")
	logging.SetLogLevel(int(logging.LogLevelDebug))
	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	config.LoadConfig()
	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet)

	rpcClient := rpc.NewClient(config.Active)
	sc := work.NewShareChain()
	sc.Load() // Load existing sharechain from disk

	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()

	pm := p2p.NewPeerManager(p2pnet.ActiveNetwork, sc)

	stratumServer := stratum.NewStratumServer(workManager, pm)
	go stratumServer.ListenForMiners()

	// Periodically commit the sharechain to disk
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
		logging.Debugf("Number of active peers: %d", pm.GetPeerCount())
		time.Sleep(30 * time.Second)
	}
}
