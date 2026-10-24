package config

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"gopkg.in/yaml.v3"
)

const Version = "v1.1.0"

type VardiffConfig struct {
	TargetTime   float64 `yaml:"targetTime"`
	RetargetTime float64 `yaml:"retargetTime"`
	Variance     float64 `yaml:"variance"`
	MinDiff      float64 `yaml:"minDiff"`
}

type Config struct {
	Network         string        `yaml:"network"`
	Peers           []string      `yaml:"peers"`
	RPCUser         string        `yaml:"rpcUser"`
	RPCPass         string        `yaml:"rpcPass"`
	Testnet         bool          `yaml:"testnet"`
	RPCHost         string        `yaml:"rpcHost"`
	RPCPort         int           `yaml:"rpcPort"`
	PoolAddress     string        `yaml:"poolAddress"`
	P2PPort         int           `yaml:"p2pPort"`
	StratumPort     int           `yaml:"stratumPort"`
	Fee             float64       `yaml:"fee"`
	PPLNSWindow     int           `yaml:"pplns_window"`
	Vardiff         VardiffConfig `yaml:"vardiff"`
	VerthashDatFile string        `yaml:"verthash_dat_file"`
	LogLevel        string        `yaml:"loglevel"`
	SoloMode        bool          `yaml:"solomode"`
	P2PSyncTimeout  int           `yaml:"p2pSyncTimeout"`
	ForceBlockFind  bool          `yaml:"forceBlockFind"`
	DataDir         string        `yaml:"datadir"`
	LightMode       bool          `yaml:"light"`
	StratumBind     string        `yaml:"stratumBind"`
	RPCCookieFile   string        `yaml:"rpcCookieFile"`
	ParentPID       int           `yaml:"parentPid"`
	ShutdownToken   string        `yaml:"shutdownToken"`
	TrustedPeers    []string      `yaml:"trustedPeers"`
}

var Active Config

func LoadConfig() {
	dataDir := flag.String("datadir", "", "Data directory for config.yaml, sharechain.json, blocks.json, peer.key, and p2pool.log")
	net := flag.String("n", "", "Network (default: vertcoin)")
	testnet := flag.Bool("testnet", false, "Use Vertcoin testnet")
	rpcHost := flag.String("rpchost", "", "Vertcoind RPC Host (default: 127.0.0.1)")
	rpcPort := flag.Int("rpcport", 0, "Vertcoind RPC Port (default: 5888 mainnet, 15888 testnet)")
	user := flag.String("u", "", "Vertcoind RPC Username")
	pass := flag.String("p", "", "Vertcoind RPC Password")
	stratumPort := flag.Int("stratum", 0, "Stratum & Web UI port (default: 9172)")
	p2pPort := flag.Int("p2p", 0, "Libp2p P2P port (default: 9348)")
	verthashDat := flag.String("verthash", "", "Path to verthash.dat file")
	poolAddr := flag.String("pooladdr", "", "Pool operator VTC payout address")
	fee := flag.Float64("fee", -1, "Pool operator fee percentage")
	logLevel := flag.String("loglevel", "", "Set logging level (debug, info, warn, error)")
	lightMode := flag.Bool("light", false, "Run in daemon-less light mode without local vertcoind")
	soloMode := flag.Bool("solo", false, "Enable solo mode to bypass P2P sync for the first node")
	showVer := flag.Bool("version", false, "Print version and exit")
	bindAddr := flag.String("bind", "", "Bind address for Stratum & Web UI (default: 0.0.0.0)")
	cookieFile := flag.String("cookie", "", "Path to Vertcoin Core .cookie file")
	parentPID := flag.Int("parentpid", 0, "Exit cleanly when this parent PID terminates")
	shutdownTok := flag.String("shutdown-token", "", "Token for loopback POST /api/shutdown")
	peersFlag := flag.String("peers", "", "Comma-separated list of bootstrap libp2p multiaddrs (replaces defaults and config.yaml peers)")
	trustedPeersFlag := flag.String("trustedpeers", "", "Comma-separated list of pinned full-node libp2p Peer IDs")
	forceBlockFind := flag.Bool("forceblock", false, "TESTING ONLY: Force every share to be a block")

	flag.Parse()
	if *showVer {
		fmt.Println("p2pool-go-vtc " + Version)
		os.Exit(0)
	}

	// Set sensible defaults so headless launches (e.g., One-Click Miner) work even without config.yaml
	Active = Config{
		Network: "vertcoin",
		Peers: []string{
			"/ip4/138.68.53.157/tcp/9348/p2p/12D3KooWNiQ2TSXEhFgZrsJuuk9xxNheph8b23pK4RHaPSQFNDwU",
			"/dns4/p2poolvtc.com/tcp/9348/p2p/12D3KooWNiQ2TSXEhFgZrsJuuk9xxNheph8b23pK4RHaPSQFNDwU",
		},
		TrustedPeers: []string{
			"12D3KooWNiQ2TSXEhFgZrsJuuk9xxNheph8b23pK4RHaPSQFNDwU",
			"12D3KooWL65LCCkfLnN2PaEjdXUm4BBHj24nzpABPhDatjNzPKwi",
		},
		RPCHost:     "127.0.0.1",
		RPCPort:     5888,
		StratumPort: 9172,
		P2PPort:     9348,
		Fee:         1.0,
		PPLNSWindow: 8640,
		LogLevel:    "info",
		Vardiff: VardiffConfig{
			TargetTime:   5.0,
			RetargetTime: 30.0,
			Variance:     0.30,
			MinDiff:      0.01,
		},
	}

	if *dataDir != "" {
		if err := os.MkdirAll(*dataDir, 0755); err != nil {
			logging.Errorf("Failed to create datadir %s: %v", *dataDir, err)
		} else if err := os.Chdir(*dataDir); err != nil {
			logging.Errorf("Failed to change working directory to datadir %s: %v", *dataDir, err)
		} else {
			Active.DataDir = *dataDir
		}
	}

	file, err := os.Open("config.yaml")
	if err != nil {
		logging.Warnf("No config.yaml file found; using CLI flags and built-in defaults.")
	} else {
		defer file.Close()
		decoder := yaml.NewDecoder(file)
		if err := decoder.Decode(&Active); err != nil {
			logging.Errorf("Failed to decode config.yaml: %v", err)
		}
	}

	if *dataDir != "" {
		Active.DataDir = *dataDir
	}
	if *net != "" {
		Active.Network = *net
	}
	if *testnet {
		Active.Testnet = true
		if Active.RPCPort == 5888 && *rpcPort == 0 {
			Active.RPCPort = 15888
		}
	}
	if *rpcHost != "" {
		Active.RPCHost = *rpcHost
	}
	if *rpcPort > 0 {
		Active.RPCPort = *rpcPort
	}
	if *user != "" {
		Active.RPCUser = *user
	}
	if *pass != "" {
		Active.RPCPass = *pass
	}
	if *stratumPort > 0 {
		Active.StratumPort = *stratumPort
	}
	if *p2pPort > 0 {
		Active.P2PPort = *p2pPort
	}
	if *verthashDat != "" {
		Active.VerthashDatFile = *verthashDat
	}
	if *poolAddr != "" {
		Active.PoolAddress = *poolAddr
	}
	if *fee >= 0 {
		Active.Fee = *fee
	}
	if *logLevel != "" {
		Active.LogLevel = *logLevel
	}
	if *lightMode {
		Active.LightMode = true
	}
	if *soloMode {
		Active.SoloMode = true
	}
	if *forceBlockFind {
		Active.ForceBlockFind = true
	}
	if *bindAddr != "" {
		Active.StratumBind = *bindAddr
	}
	if *cookieFile != "" {
		Active.RPCCookieFile = *cookieFile
	}
	if *parentPID > 0 {
		Active.ParentPID = *parentPID
	}
	if *shutdownTok != "" {
		Active.ShutdownToken = *shutdownTok
	}
	if *peersFlag != "" {
		if peers := parsePeerList(*peersFlag); len(peers) > 0 {
			Active.Peers = peers
		} else {
			logging.Warnf("No valid entries in -peers; keeping existing bootstrap peers.")
		}
	}
	if *trustedPeersFlag != "" {
		Active.TrustedPeers = []string{}
		for _, p := range strings.Split(*trustedPeersFlag, ",") {
			if p = strings.TrimSpace(p); p != "" {
				Active.TrustedPeers = append(Active.TrustedPeers, p)
			}
		}
	}
}

// parsePeerList splits a comma-separated list of libp2p multiaddrs, dropping
// empty entries and warning about any that lack a valid /p2p/<PeerID>.
func parsePeerList(s string) []string {
	peers := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(p)
		if err != nil {
			logging.Warnf("Ignoring invalid -peers multiaddr '%s': %v", p, err)
			continue
		}
		if _, err := peer.AddrInfoFromP2pAddr(ma); err != nil {
			logging.Warnf("Ignoring -peers entry '%s' without /p2p/<PeerID>: %v", p, err)
			continue
		}
		peers = append(peers, p)
	}
	return peers
}

func IsTrustedPeer(peerID string) bool {
	if peerID == "" {
		return false
	}
	// Only explicitly pinned IDs are trusted; an empty list trusts no one.
	for _, tp := range Active.TrustedPeers {
		tp = strings.TrimSpace(tp)
		if idx := strings.LastIndex(tp, "/p2p/"); idx != -1 {
			tp = strings.TrimSpace(tp[idx+5:])
		}
		if tp == peerID {
			return true
		}
	}
	return false
}
