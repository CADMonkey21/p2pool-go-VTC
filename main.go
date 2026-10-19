package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
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

	logging.Infof("🚀  p2pool-go (GossipSub mesh) starting up")

	logFile, _ := os.OpenFile("p2pool.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	defer logFile.Close()
	logging.SetLogFile(logFile)

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

	// Wire automatic P2P historical ShareChain and Blocks synchronization
	p2pNode.GetSyncDataFunc = func() ([]byte, []byte) {
		shares := sc.GetAllShares()
		sBytes, _ := json.Marshal(shares)
		bBytes := workManager.GetPersistedBlocksJSON()
		return sBytes, bBytes
	}

	p2pNode.OnSyncDataFunc = func(sharesJSON []byte, blocksJSON []byte) {
		if len(sharesJSON) > 0 {
			var peerShares []work.Share
			if err := json.Unmarshal(sharesJSON, &peerShares); err == nil && len(peerShares) > 0 {
				var validShares []work.Share
				for i := range peerShares {
					s := peerShares[i]
					if !sc.HasShare(s.Hash.String()) {
						if err := s.CalculateHashes(); err == nil {
							if ok, _ := s.IsValid(); ok {
								validShares = append(validShares, s)
							}
						}
					}
				}
				if len(validShares) > 0 {
					added := sc.AddShares(validShares)
					if added > 0 {
						logging.Successf("P2P Sync: Hydrated %d missing historical shares from peer!", added)
						_ = sc.Commit()
					}
				}
			}
		}
		if len(blocksJSON) > 0 {
			addedBlocks := workManager.MergeRemoteBlocksJSON(blocksJSON)
			if addedBlocks > 0 {
				logging.Successf("P2P Sync: Hydrated %d missing historical found block(s) from peer!", addedBlocks)
			}
		}
	}

	go p2pNode.Bootstrap(config.Active.Peers)

	go func() {
		for shareBytes := range p2pNode.IncomingShares {
			var remoteShare work.Share
			if err := json.Unmarshal(shareBytes, &remoteShare); err != nil {
				logging.Warnf("P2P: Failed to unmarshal incoming peer share: %v", err)
				continue
			}
			if err := remoteShare.CalculateHashes(); err != nil {
				logging.Warnf("P2P: Failed to calculate hashes for incoming peer share: %v", err)
				continue
			}
			if valid, reason := remoteShare.IsValid(); !valid {
				logging.Warnf("P2P: Rejected invalid peer share (%s)", reason)
				continue
			}

			added := sc.AddShares([]work.Share{remoteShare})
			if added > 0 {
				logging.Infof("P2P: Validated and added remote peer share %s to ShareChain", remoteShare.Hash.String()[:12])

				netTarget := blockchain.CompactToBig(remoteShare.MinHeader.Bits)
				powInt := new(big.Int).SetBytes(remoteShare.POWHash.CloneBytes())
				if powInt.Cmp(netTarget) <= 0 {
					select {
					case sc.FoundBlockChan <- &remoteShare:
					default:
					}
				}
			}
		}
	}()

	stratumSrv := stratum.NewStratumServer(workManager, p2pNode)

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
