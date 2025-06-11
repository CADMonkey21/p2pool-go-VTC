package config

import (
	"flag"
	"os"

	"github.com/gertjaap/p2pool-go/logging"
	"gopkg.in/yaml.v3"
)

// Config struct holds all configuration variables loaded from config.yaml
// The `yaml:"..."` tags are essential and must match the keys in your config file.
type Config struct {
	Network     string  `yaml:"network"`
	Testnet     bool    `yaml:"testnet"`
	RPCUser     string  `yaml:"rpcUser"`
	RPCPass     string  `yaml:"rpcPass"`
	RPCHost     string  `yaml:"rpcHost"`
	RPCPort     int     `yaml:"rpcPort"`
	PoolAddress string  `yaml:"poolAddress"`
	P2PPort     int     `yaml:"p2pPort"`
	StratumPort int     `yaml:"stratumPort"`
	Fee         float64 `yaml:"fee"`
}

// Active is the globally accessible configuration instance.
var Active Config

// LoadConfig reads the config.yaml file, decodes it into the Active Config struct,
// and then overrides any values with command-line flags if they are provided.
func LoadConfig() {
	// Load config file first
	file, err := os.Open("config.yaml")
	if err != nil {
		logging.Warnf("No config.yaml file found. Using defaults and command-line flags.")
	} else {
		defer file.Close()
		decoder := yaml.NewDecoder(file)
		err = decoder.Decode(&Active)
		if err != nil {
			// This is a fatal error because the config is unreadable.
			logging.Fatalf("Failed to decode config.yaml: %v", err)
		}
	}

	// Override config file if started with flags
	// Note: We are not adding flags for all the new options yet,
	// but the framework is here. The config file is the primary method.
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

	// It's good practice to log the network being used.
	if Active.Network == "" {
		logging.Fatalf("Error: Network is not specified in config.yaml or command-line flags.")
	} else {
		logging.Infof("Network configured for: %s", Active.Network)
	}
}
