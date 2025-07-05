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

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/rpc"
	"github.com/gertjaap/p2pool-go/stratum"
	"github.com/gertjaap/p2pool-go/web"
	"github.com/gertjaap/p2pool-go/work"
)

/* -------------------------------------------------------------------- */
/* Helpers                                                             */
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
	switch {
	case sec > 86400:
		return fmt.Sprintf("%.2f days", sec/86400)
	case sec > 3600:
		return fmt.Sprintf("%.2f hours", sec/3600)
	case sec > 60:
		return fmt.Sprintf("%.2f minutes", sec/60)
	default:
		return fmt.Sprintf("%.2f seconds", sec)
	}
}

func logStats(pm *p2p.PeerManager, sc *work.ShareChain, ss *stratum.StratumServer) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		stats := sc.GetStats()
		localHashrate := ss.GetLocalHashrate()

		logging.Infof("================================= Stats Update =================================")
		logging.Infof("Peers: %d  |  GoRoutines: %d", pm.GetPeerCount(), runtime.NumGoroutine())
		logging.Infof("--------------------------------------------------------------------------------")
		logging.Infof("Pool Hashrate: %s (%.2f%% efficient)", formatHashrate(stats.PoolHashrate), stats.Efficiency)
		logging.Infof("Your Hashrate: %s", formatHashrate(localHashrate))
		logging.Infof("Network Hashrate: %s", formatHashrate(stats.NetworkHashrate))
		logging.Infof("--------------------------------------------------------------------------------")
		logging.Infof("Share Chain: %d total, %d orphan, %d dead", stats.SharesTotal, stats.SharesOrphan, stats.SharesDead)
		logging.Infof("Est. Time to Block: %s", formatDuration(stats.TimeToBlock))
		logging.Infof("================================================================================")
	}
}

/* -------------------------------------------------------------------- */
/* main                                                                */
/* -------------------------------------------------------------------- */

func main() {
	/* ----- start‑up & logging ---------------------------------------- */
	logging.Infof("🚀  p2pool-go (single‑port build) starting up")
	logging.SetLogLevel(int(logging.LogLevelDebug))

	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	/* ----- configuration -------------------------------------------- */
	config.LoadConfig()
	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet)

	// Optional: override default P2P port
	p2pnet.ActiveNetwork.P2PPort = 19172

	/* ----- RPC, share chain, work manager --------------------------- */
	rpcClient := rpc.NewClient(config.Active)

	sc := work.NewShareChain(rpcClient)
	if err := sc.Load(); err != nil {
		logging.Fatalf("MAIN: Failed to load sharechain: %v", err)
	}

	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()
	go workManager.WatchMaturedBlocks()

	/* ----- peer network --------------------------------------------- */
	pm := p2p.NewPeerManager(p2pnet.ActiveNetwork, sc)
	go pm.ListenForPeers()

	/* ----- Stratum server instance ---------------------------------- */
	stratumSrv := stratum.NewStratumServer(workManager, pm)

	/* =================================================================
	   Single TCP listener on :9172, multiplexed via cmux
	   ================================================================= */
	addr := fmt.Sprintf(":%d", config.Active.StratumPort) // 9172
	baseListener, err := net.Listen("tcp", addr)
	if err != nil {
		logging.Fatalf("MAIN: Unable to listen on %s: %v", addr, err)
	}

	m := cmux.New(baseListener)
	httpL := m.Match(cmux.HTTP1Fast()) // Web dashboard
	stratumL := m.Match(cmux.Any())    // Everything else → miners

	/* ----- tiny placeholder dashboard ------------------------------- */
	httpSrv := &http.Server{
		Handler: web.NewDashboard(workManager, pm, stratumSrv),
	}

	go func() { _ = httpSrv.Serve(httpL) }()
	go func() { _ = stratumSrv.Serve(stratumL) }()
	go func() {
		if err := m.Serve(); err != nil {
			logging.Fatalf("MAIN: cmux error: %v", err)
		}
	}()

	/* ----- misc goroutines & shutdown handler ----------------------- */
	go logStats(pm, sc, stratumSrv)

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
