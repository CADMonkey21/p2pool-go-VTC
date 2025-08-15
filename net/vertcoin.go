package net

import (
	"encoding/hex"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	// "github.com/CADMonkey21/p2pool-go-VTC/verthash" // FIX: This import is no longer needed here
)

// The Vertcoin function now correctly calls the Hash method from the passed-in engine
func Vertcoin(testnet bool) Network {
	n := Network{
		P2PPort:         config.Active.P2PPort,
		StandardP2PPort: 9348,
		ProtocolVersion: 3501,
		RPCPort:         config.Active.RPCPort,
		WorkerPort:      config.Active.StratumPort,
		ChainLength:     5100,
		// Verthash engine is now set in main after config is loaded
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
