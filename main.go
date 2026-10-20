package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/btcsuite/btcd/blockchain"
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

		peerCount := 0
		if p2pNode != nil && p2pNode.Host != nil {
			peerCount = len(p2pNode.Host.Network().Peers())
		}

		effectivePoolHashrate := stats.PoolHashrate
		if peerCount == 0 || localHashrate > effectivePoolHashrate {
			effectivePoolHashrate = localHashrate
		}

		effectiveTimeToBlock := stats.TimeToBlock
		if effectivePoolHashrate > 0 && stats.NetworkHashrate > 0 {
			effectiveTimeToBlock = (stats.NetworkHashrate / effectivePoolHashrate) * 150.0
		}

		workManager := ss.GetWorkManager()
		connectedMiners := len(ss.GetClients())
		blocksFound24h := workManager.GetBlocksFoundInLast(24 * time.Hour)
		lastBlockFoundTime := workManager.GetLastBlockFoundTime()

		logging.Infof("================================= Stats Update =================================")
		logging.Infof("Peers: %d  |  Miners: %d  |  GoRoutines: %d", peerCount, connectedMiners, runtime.NumGoroutine())
		logging.Infof("--------------------------------------------------------------------------------")
		logging.Infof("P2Pool Network Hashrate: %s (%.2f%% efficient)", formatHashrate(effectivePoolHashrate), stats.Efficiency)
		logging.Infof("Your Node's Hashrate: %s", formatHashrate(localHashrate))
		logging.Infof("Global VTC Hashrate: %s", formatHashrate(stats.NetworkHashrate))
		logging.Infof("--------------------------------------------------------------------------------")
		logging.Infof("Share Chain: %d total, %d orphan, %d dead", stats.SharesTotal, stats.SharesOrphan, stats.SharesDead)
		logging.Infof("Est. Time to Block: %s", formatDuration(effectiveTimeToBlock))
		logging.Infof("Blocks Found (24h): %d  |  Last Block Found: %s", blocksFound24h, formatDurationAgo(lastBlockFoundTime))
		logging.Infof("================================================================================")
	}
}

func main() {
	startTime := time.Now()

	origDir, _ := os.Getwd()
	staticDir := filepath.Join(origDir, "web", "static")
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		if exePath, err := os.Executable(); err == nil {
			staticDir = filepath.Join(filepath.Dir(exePath), "web", "static")
		}
	}

	config.LoadConfig()

	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

	logging.Infof("🚀  p2pool-go (GossipSub mesh) starting up")

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

	rpcClient := rpc.NewClient(config.Active)
	sc := work.NewShareChain(rpcClient)

	if err := sc.Load(); err != nil {
		logging.Fatalf("MAIN: Failed to load sharechain: %v", err)
	}

	workManager := work.NewWorkManager(rpcClient, sc)
	go workManager.WatchBlockTemplate()
	go workManager.WatchMaturedBlocks()
	go workManager.WatchFoundBlocks()

	p2pPort := config.Active.P2PPort
	if p2pPort <= 0 {
		p2pPort = 4001
	}
	p2pNode, err := p2p.NewNode(p2pPort)
	if err != nil {
		logging.Fatalf("Failed to start p2p networking: %v", err)
	}
	defer p2pNode.Close()

	// Wire bidirectional P2P ShareChain and Blocks synchronization
	p2pNode.GetSyncDataFunc = func() ([]byte, []byte) {
		shares := sc.GetSharesForSync(10000)
		sBytes, _ := json.Marshal(shares)
		bBytes := workManager.GetPersistedBlocksJSON()
		return sBytes, bBytes
	}

	p2pNode.GetSharesByHashFunc = func(hashes []string, parents int) []byte {
		b, _ := json.Marshal(sc.GetSharesByHashWithParents(hashes, parents))
		return b
	}
	p2pNode.OnSyncDataFunc = func(sharesJSON []byte, blocksJSON []byte) {
		if len(sharesJSON) > 0 {
			var ps []work.Share
			if json.Unmarshal(sharesJSON, &ps) == nil && len(ps) > 0 {
				if added := sc.AddShares(ps); added > 0 {
					logging.Successf("P2P Sync: Hydrated %d verified shares from peer!", added)
					_ = sc.Commit()
				}
				if miss := sc.GetMissingParentHashes(16); len(miss) > 0 {
					go p2pNode.RequestShares(miss, 250)
				}
			}
		}
		if len(blocksJSON) > 0 {
			if ab := workManager.MergeRemoteBlocksJSON(blocksJSON); ab > 0 {
				logging.Successf("P2P Sync: Hydrated %d missing found block(s) from peer!", ab)
			}
		}
	}
	stratumSrv := stratum.NewStratumServer(workManager, p2pNode)
	go p2pNode.Bootstrap(config.Active.Peers)
	go func() {
		for shareBytes := range p2pNode.IncomingShares {
			var remoteShare work.Share
			if json.Unmarshal(shareBytes, &remoteShare) != nil {
				continue
			}
			if remoteShare.CalculateHashes() != nil {
				continue
			}
			added := sc.AddShares([]work.Share{remoteShare})
			if miss := sc.GetMissingParentHashes(16); len(miss) > 0 {
				go p2pNode.RequestShares(miss, 250)
			}
			powInt := new(big.Int).SetBytes(remoteShare.POWHash.CloneBytes())
			netTarget := blockchain.CompactToBig(remoteShare.MinHeader.Bits)
			if added > 0 {
				stratumSrv.NotifyNewShare()
				logging.Infof("P2P: Validated and added remote peer share %s to ShareChain", remoteShare.Hash.String()[:12])

				if powInt.Cmp(netTarget) <= 0 {
					select {
					case sc.FoundBlockChan <- &remoteShare:
					default:
					}
				}
			}
		}
	}()

	addr := fmt.Sprintf(":%d", config.Active.StratumPort)
	baseListener, err := net.Listen("tcp", addr)
	if err != nil {
		logging.Fatalf("MAIN: Unable to listen on %s: %v", addr, err)
	}

	m := cmux.New(baseListener)
	httpL := m.Match(cmux.HTTP1Fast())
	stratumL := m.Match(cmux.Any())

	mux := http.NewServeMux()
	apiHandler := web.NewDashboard(workManager, p2pNode, stratumSrv, startTime)

	mux.Handle("/api/stats", apiHandler)
	mux.HandleFunc("/api/status", apiHandler.ServeStatus)
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))

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

	go logStats(p2pNode, sc, stratumSrv)

	// Periodic background autosave every 2 minutes so shares are never lost on unexpected exit
	go func() {
		saveTicker := time.NewTicker(2 * time.Minute)
		for range saveTicker.C {
			if err := sc.Commit(); err != nil {
				logging.Warnf("Autosave: Failed to commit sharechain: %v", err)
			} else {
				logging.Debugf("Autosave: Sharechain committed to disk.")
			}
		}
	}()

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
