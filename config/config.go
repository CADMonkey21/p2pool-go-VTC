package config

import (
	"flag"
	"os"

	"github.com/gertjaap/p2pool-go/logging"
	"gopkg.in/yaml.v3"
)

type VardiffConfig struct {
	TargetTime   float64 `yaml:"targetTime"`
	RetargetTime float64 `yaml:"retargetTime"`
	Variance     float64 `yaml:"variance"`
	MinDiff      float64 `yaml:"minDiff"`
}

type Config struct {
	Network     string        `yaml:"network"`
	Peers       []string      `yaml:"peers"`
	RPCUser     string        `yaml:"rpcUser"`
	RPCPass     string        `yaml:"rpcPass"`
	Testnet     bool          `yaml:"testnet"`
	RPCHost     string        `yaml:"rpcHost"`
	RPCPort     int           `yaml:"rpcPort"`
	PoolAddress string        `yaml:"poolAddress"`
	P2PPort     int           `yaml:"p2pPort"` // <-- Re-added this line
	StratumPort int           `yaml:"stratumPort"`
	Fee         float64       `yaml:"fee"`
	Vardiff     VardiffConfig `yaml:"vardiff"`
}

var Active Config

func LoadConfig() {
	// This function remains unchanged
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
	
	net := flag.String("n", "", "Network")
	testnet := flag.Bool("testnet", false, "Testnet")
	user := flag.String("u", "", "RPC Username")
	pass := flag.String("p", "", "RPC Password")
	flag.Parse()

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
}
