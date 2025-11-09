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

// [MODIFIED] This function now matches the web dashboard's "ago" formatting
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

func logStats(pm *p2p.PeerManager, sc *work.ShareChain, ss *stratum.StratumServer) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		stats := sc.GetStats()
		localHashrate := ss.GetLocalHashrate()

		// --- [NEW] Get additional stats for logging ---
		// [FIX] Use the public getter method
		workManager := ss.GetWorkManager()
		connectedMiners := len(ss.GetClients())
		blocksFound24h := workManager.GetBlocksFoundInLast(24 * time.Hour)
		lastBlockFoundTime := workManager.GetLastBlockFoundTime()
		// --- [END NEW] ---

		logging.Infof("================================= Stats Update =================================")
		logging.Infof("Peers: %d  |  Miners: %d  |  GoRoutines: %d", pm.GetPeerCount(), connectedMiners, runtime.NumGoroutine())
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
	logging.Infof("🚀  p2pool-go (single-port build) starting up")

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
		logging.SetLogLevel(2) // Default to Info
	}

	verthashEngine, err := verthash.New(config.Active.VerthashDatFile)
	if err != nil {
		logging.Fatalf("CRITICAL: Failed to initialize Verthash: %v", err)
	}

	p2pnet.SetNetwork(config.Active.Network, config.Active.Testnet, verthashEngine)

	/* ----- RPC, share chain, work manager --------------------------- */
	rpcClient := rpc.NewClient(config.Active)
	sc := work.NewShareChain(rpcClient)

	/* ----- peer network --------------------------------------------- */
	pm := p2p.NewPeerManager(p2pnet.ActiveNetwork, sc)
	go pm.ListenForPeers()

	if err := sc.Load(); err != nil {
		logging.Fatalf("MAIN: Failed to load sharechain: %v", err)
	}

	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()
	go workManager.WatchMaturedBlocks()
	go workManager.WatchFoundBlocks()

	/* ----- BLOCKING LOGIC WITHOUT TIMEOUT --------------------------- */
	//
	// [MODIFIED] This block now includes a configurable timeout
	//
	if !config.Active.SoloMode {
		// [NEW] Set up a timeout for P2P sync
		var timeoutChan <-chan time.Time
		if config.Active.P2PSyncTimeout > 0 {
			logging.Infof("MAIN: Waiting for P2P sync... (timeout: %d seconds)", config.Active.P2PSyncTimeout)
			timeoutChan = time.After(time.Duration(config.Active.P2PSyncTimeout) * time.Second)
			
			// [MODIFIED] Log message now includes the timeout duration
			logging.Warnf("MAIN: Stratum server (for miners) is PAUSED until P2P sync is complete or the %d second timeout is reached.", config.Active.P2PSyncTimeout)
		} else {
			logging.Infof("MAIN: Waiting for P2P sync... (no timeout)")
			// [MODIFIED] Log message for the "wait forever" case
			logging.Warnf("MAIN: Stratum server (for miners) is PAUSED until P2P sync is complete.")
			// nil channel blocks forever, preserving original behavior
		}

		select {
		case <-pm.SyncedChannel():
			// This is the normal, successful sync
			logging.Infof("MAIN: ✅ Sync complete! Starting Stratum server and web UI.")
		case <-timeoutChan:
			// This is the new timeout case
			logging.Warnf("MAIN: P2P sync TIMEOUT. No peers found after %d seconds.", config.Active.P2PSyncTimeout)
			logging.Warnf("MAIN: Starting in solo mode automatically to allow miners to connect.")
			// Manually force the PeerManager into a "synced" state
			// This is crucial so the Stratum server's authorization check passes.
			pm.ForceSyncState(true)
			logging.Infof("MAIN: ✅ Sync FAKED! Starting Stratum server and web UI.")
		}
	} else {
		logging.Warnf("MAIN: SoloMode=true. Bypassing P2P sync check.")
		logging.Warnf("MAIN: This node will act as the FIRST node on a new network.")
		// Manually force the PeerManager into a "synced" state.
		// This is crucial so the Stratum server's authorization check passes.
		pm.ForceSyncState(true)
		logging.Infof("MAIN: ✅ Sync FAKED! Starting Stratum server and web UI.")
	}
	/* ---------------------------------------------------------------- */

	/* ----- Stratum server instance ---------------------------------- */
	stratumSrv := stratum.NewStratumServer(workManager, pm)

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

	/* ----- tiny placeholder dashboard ------------------------------- */
	httpSrv := &http.Server{
		Handler: web.NewDashboard(workManager, pm, stratumSrv, startTime),
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
