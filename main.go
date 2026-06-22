package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/soheilhy/cmux"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/CADMonkey21/p2pool-go-VTC/stratum"
	"github.com/CADMonkey21/p2pool-go-VTC/verthash"
	"github.com/CADMonkey21/p2pool-go-VTC/web"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

/* -------------------------------------------------------------------- */
/* Helpers                                                              */
/* -------------------------------------------------------------------- */

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

func formatDuration(sec float64) string {
	if sec <= 0 {
		return "N/A"
	}
	d := time.Duration(sec) * time.Second
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

func formatDurationAgo(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	d := time.Since(t)
	switch {
	case d.Hours() > 48:
		return fmt.Sprintf("%.0f days, %d hours ago", d.Hours()/24, int(d.Hours())%24)
	case d.Minutes() > 120:
		return fmt.Sprintf("%.0f hours, %d minutes ago", d.Hours(), int(d.Minutes())%60)
	case d.Seconds() > 120:
		return fmt.Sprintf("%.0f minutes, %d seconds ago", d.Minutes(), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%.0f seconds ago", d.Seconds())
	}
}

func logStats(p2pNode *p2p.Node, sc *work.ShareChain, ss *stratum.StratumServer) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		stats := sc.GetStats()
		localHashrate := ss.GetLocalHashrate()

		workManager := ss.GetWorkManager()
		connectedMiners := len(ss.GetClients())
		blocksFound24h := workManager.GetBlocksFoundInLast(24 * time.Hour)
		lastBlockFoundTime := workManager.GetLastBlockFoundTime()

		// Get peer count from libp2p Host
		peerCount := 0
		if p2pNode != nil {
			peerCount = len(p2pNode.Host.Network().Peers())
		}

		logging.Infof("================================= Stats Update =================================")
		logging.Infof("Peers: %d  |  Miners: %d  |  GoRoutines: %d", peerCount, connectedMiners, runtime.NumGoroutine())
		logging.Infof("--------------------------------------------------------------------------------")
		logging.Infof("P2Pool Network Hashrate: %s (%.2f%% efficient)", formatHashrate(stats.PoolHashrate), stats.Efficiency)
		logging.Infof("Your Node's Hashrate: %s", formatHashrate(localHashrate))
		logging.Infof("Global VTC Hashrate: %s", formatHashrate(stats.NetworkHashrate))
		logging.Infof("--------------------------------------------------------------------------------")
		logging.Infof("Share Chain: %d total, %d orphan, %d dead", stats.SharesTotal, stats.SharesOrphan, stats.SharesDead)
		logging.Infof("Est. Time to Block: %s", formatDuration(stats.TimeToBlock))
		logging.Infof("Blocks Found (24h): %d  |  Last Block Found: %s", blocksFound24h, formatDurationAgo(lastBlockFoundTime))
		logging.Infof("================================================================================")
	}
}

/* -------------------------------------------------------------------- */
/* main                                                                 */
/* -------------------------------------------------------------------- */

func main() {
	startTime := time.Now()

	/* ----- start-up & logging ---------------------------------------- */
	logging.Infof("🚀  p2pool-go (GossipSub mesh) starting up")

	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	/* ----- configuration -------------------------------------------- */
	config.LoadConfig()

	switch config.Active.LogLevel {
	case "debug":
		logging.SetLogLevel(3)
	case "info":
		logging.SetLogLevel(2)
	case "warn":
		logging.SetLogLevel(1)
	case "error":
		logging.SetLogLevel(0)
	default:
		logging.SetLogLevel(2)
	}

	verthashEngine, err := verthash.New(config.Active.VerthashDatFile)
	if err != nil {
		logging.Fatalf("CRITICAL: Failed to initialize Verthash: %v", err)
	}

	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet, verthashEngine)

	/* ----- RPC, share chain, work manager --------------------------- */
	rpcClient := rpc.NewClient(config.Active)
	sc := work.NewShareChain(rpcClient)

	if err := sc.Load(); err != nil {
		logging.Fatalf("MAIN: Failed to load sharechain: %v", err)
	}

	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()
	go workManager.WatchMaturedBlocks()
	go workManager.WatchFoundBlocks()

	/* ----- NEW LIBP2P NETWORKING ------------------------------------ */
	// Initialize the new P2P Node on port 4001 (or any port you prefer)
	p2pNode, err := p2p.NewNode(4001)
	if err != nil {
		logging.Fatalf("Failed to start p2p networking: %v", err)
	}
	defer p2pNode.Close()

	// Connect to the mesh (Leave empty for local testing, add IPs later)
	bootstrapPeers := []string{}
	go p2pNode.Bootstrap(bootstrapPeers)

	// Listen for shares coming from the network
	go func() {
		for shareBytes := range p2pNode.IncomingShares {
			// Right now, just print it so we know it works!
			// Later we will deserialize this and pass it to verthash.
			logging.Infof("Received remote share over GossipSub: %s", string(shareBytes))
		}
	}()
	/* ---------------------------------------------------------------- */

	/* ----- Stratum server instance ---------------------------------- */
	stratumSrv := stratum.NewStratumServer(workManager, p2pNode)

	/* =================================================================
	   Single TCP listener, multiplexed via cmux
	   ================================================================= */
	addr := fmt.Sprintf(":%d", config.Active.StratumPort)
	baseListener, err := net.Listen("tcp", addr)
	if err != nil {
		logging.Fatalf("MAIN: Unable to listen on %s: %v", addr, err)
	}

	m := cmux.New(baseListener)
	httpL := m.Match(cmux.HTTP1Fast())
	stratumL := m.Match(cmux.Any())

	mux := http.NewServeMux()

	// NOTE: Passing 'nil' for the old PeerManager argument temporarily to prevent compiler crashes.
	// We will update the Dashboard to read from p2pNode next!
	apiHandler := web.NewDashboard(workManager, nil, stratumSrv, startTime)
	
	mux.Handle("/api/stats", apiHandler)
	mux.Handle("/", http.FileServer(http.Dir("./web/static")))

	httpSrv := &http.Server{
		Handler: mux,
	}

	go func() { _ = httpSrv.Serve(httpL) }()
	go func() { _ = stratumSrv.Serve(stratumL) }()
	go func() {
		if err := m.Serve(); err != nil {
			logging.Fatalf("MAIN: cmux error: %v", err)
		}
	}()

	/* ----- misc goroutines & shutdown handler ----------------------- */
	go logStats(p2pNode, sc, stratumSrv)

	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)
	logging.Infof("MAIN: Startup complete – Stratum + Web UI on %s. Press Ctrl+C to exit.", addr)

	<-shutdownChan
	logging.Warnf("\nShutdown signal received. Saving share chain and exiting…")

	if err := sc.Commit(); err != nil {
		logging.Errorf("Could not commit sharechain on shutdown: %v", err)
	} else {
		logging.Infof("Sharechain committed successfully.")
	}
}
