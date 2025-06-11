package main

import (
	"os"
	"time"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/rpc"
	"github.com/gertjaap/p2pool-go/stratum" // <-- Import new Stratum package
	"github.com/gertjaap/p2pool-go/work"
)

func main() {
	logging.SetLogLevel(int(logging.LogLevelDebug))
	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	config.LoadConfig()
	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet)

	// Create RPC client
	rpcClient := rpc.NewClient(config.Active)

	// Create and start the Work Manager
	workManager := work.NewWorkManager(rpcClient)
	go workManager.WatchBlockTemplate()

	// --- NEW ---
	// Create and start the Stratum Server
	stratumServer := stratum.NewStratumServer(workManager)
	go stratumServer.ListenForMiners()
	// --- END NEW ---

	// Create and start the P2P Manager
	sc := work.NewShareChain()
	err := sc.Load()
	if err != nil {
		panic(err)
	}
	pm := p2p.NewPeerManager(p2pnet.ActiveNetwork, sc)

	// Keep the main process alive
	for {
		logging.Debugf("Number of active peers: %d", pm.GetPeerCount())
		time.Sleep(30 * time.Second) // We can slow this down now
	}
}
