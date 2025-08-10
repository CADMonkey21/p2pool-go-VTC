package net

import (
	"encoding/hex"

	"github.com/CADMonkey21/p2pool-go-vtc/config"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/verthash" // Use your new local package
)

// Use the Verthasher interface from your new package
var verthashEngine verthash.Verthasher

// The init function now uses NewRealVerthasher
func init() {
	logging.Infof("Initializing Verthash engine from local package...")
	// The hasher_impl.go will find verthash.dat in default locations if "" is passed
	vh, err := verthash.NewRealVerthasher("verthash.dat")
	if err != nil {
		logging.Fatalf("CRITICAL: Failed to initialize Verthash. Make sure 'verthash.dat' is present. Error: %v", err)
		return
	}
	verthashEngine = vh
	logging.Successf("Verthash engine initialized successfully.")
}

// The Vertcoin function now correctly calls the Hash method
func Vertcoin(testnet bool) Network {
	n := Network{
		P2PPort:         config.Active.P2PPort,
		StandardP2PPort: 9348,
		ProtocolVersion: 3501,
		RPCPort:         config.Active.RPCPort,
		WorkerPort:      config.Active.StratumPort,
		ChainLength:     5100,
		Verthash:        verthashEngine, // Assign the engine
	}

	n.POWHash = func(data []byte) []byte {
		if n.Verthash == nil {
			logging.Errorf("Verthash engine is not available, cannot compute hash.")
			return nil
		}
		hash, err := n.Verthash.Hash(data) // Use the .Hash() method from the interface
		if err != nil {
			logging.Errorf("Verthash failed during hashing: %v", err)
			return nil
		}
		return hash
	}

	n.SeedHosts = config.Active.Peers
	n.MessagePrefix, _ = hex.DecodeString("e4c3b2a1f0d9e8c7")
	n.Identifier, _ = hex.DecodeString("d8b3c4e5f6a7b8c9")

	return n
}
