package config

import (
	"flag"
	"os"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"gopkg.in/yaml.v3"
)

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
	LogLevel        string        `yaml:"loglevel"` // This can be overridden by the flag
	SoloMode        bool          `yaml:"solomode"`
	P2PSyncTimeout  int           `yaml:"p2pSyncTimeout"`
	ForceBlockFind  bool          `yaml:"forceBlockFind"` // [NEW] Add this field
	DataDir         string        `yaml:"datadir"`
}

var Active Config

func LoadConfig() {
	dataDir := flag.String("datadir", "", "Data directory for config.yaml, sharechain.json, blocks.json, peer.key, and p2pool.log")
	net := flag.String("n", "", "Network")
	testnet := flag.Bool("testnet", false, "Testnet")
	user := flag.String("u", "", "RPC Username")
	pass := flag.String("p", "", "RPC Password")
	logLevel := flag.String("loglevel", "", "Set logging level (debug, info, error)")
	soloMode := flag.Bool("solo", false, "Enable solo mode to bypass P2P sync for the first node")
	forceBlockFind := flag.Bool("forceblock", false, "TESTING ONLY: Force every share to be a block")

	flag.Parse()

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
		logging.Warnf("No config.yaml file found.")
	} else {
		defer file.Close()
		decoder := yaml.NewDecoder(file)
		err = decoder.Decode(&Active)
		if err != nil {
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
	}
	if *user != "" {
		Active.RPCUser = *user
	}
	if *pass != "" {
		Active.RPCPass = *pass
	}
	// FIX: If the loglevel flag is used, it overrides the config file
	if *logLevel != "" {
		Active.LogLevel = *logLevel
	}
	// [NEW] Handle the solomode flag
	if *soloMode {
		Active.SoloMode = true
	}
	// [NEW] Handle the forceBlockFind flag
	if *forceBlockFind {
		Active.ForceBlockFind = true
	}
}
