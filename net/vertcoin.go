package net

import (
	"encoding/hex"

	"github.com/CADMonkey21/p2pool-go-vtc/config"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	"github.com/gertjaap/verthash-go"
)

// This variable will hold our single, stable Verthash engine.
var verthashEngine *verthash.Verthash

// This is a standard Go function that runs automatically and exactly once
// when the 'net' package is first used. This is the perfect place to
// initialize the Verthash engine.
func init() {
	logging.Infof("Initializing Verthash engine...")
	vh, err := verthash.NewVerthash("verthash.dat", true) // true = read-only mode, which is safer
	if err != nil {
		// Log a fatal error. The program cannot continue if this fails.
		logging.Fatalf("CRITICAL: Failed to initialize Verthash. Make sure 'verthash.dat' is in the same directory as p2pool-go-VTC. Error: %v", err)
		return
	}
	verthashEngine = vh
	logging.Successf("Verthash engine initialized successfully.")
}

// Your original Vertcoin function remains, but is now much safer.
func Vertcoin(testnet bool) Network {
	n := Network{
		P2PPort:         config.Active.P2PPort,
		StandardP2PPort: 9348,
		ProtocolVersion: 3501,
		RPCPort:         config.Active.RPCPort,
		WorkerPort:      config.Active.StratumPort,
		ChainLength:     5100,
		// It now uses the stable engine we created above.
		Verthash: verthashEngine,
	}

	n.POWHash = func(data []byte) []byte {
		// Add a safety check in case initialization failed.
		if n.Verthash == nil {
			logging.Errorf("Verthash engine is not available, cannot compute hash.")
			return nil
		}
		hash, err := n.Verthash.SumVerthash(data)
		if err != nil {
			logging.Errorf("Verthash failed during hashing: %v", err)
			return nil
		}
		return hash[:]
	}

	n.SeedHosts = config.Active.Peers
	n.MessagePrefix, _ = hex.DecodeString("e4c3b2a1f0d9e8c7")
	n.Identifier, _ = hex.DecodeString("d8b3c4e5f6a7b8c9")

	return n
}
