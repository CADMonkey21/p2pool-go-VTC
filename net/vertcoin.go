package net

import (
	"encoding/hex"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/verthash-go" // Corrected import path
)

func Vertcoin(testnet bool) Network {
	logging.Infof("Initializing Verthash... This will take a moment and consume >1GB of RAM.")
	vh, err := verthash.NewVerthash("verthash.dat", false)
	if err != nil {
		panic(err)
	}

	n := Network{
		P2PPort:         config.Active.P2PPort,
		// The standard P2P port for your running Python pool is 9346.
		StandardP2PPort: 9346,
		ProtocolVersion: 3501,
		RPCPort:         config.Active.RPCPort,
		WorkerPort:      config.Active.StratumPort,
		ChainLength:     5100,
		Verthash:        vh,
	}

	n.POWHash = func(data []byte) []byte {
		hash, err := n.Verthash.SumVerthash(data)
		if err != nil {
			logging.Errorf("Verthash failed during hashing: %v", err)
			return nil
		}
		return hash[:]
	}

	n.SeedHosts = config.Active.Peers
	
	// Using the correct magic bytes that your Python pool is broadcasting.
	n.MessagePrefix, _ = hex.DecodeString("1c0c1c71cc197bc1")
	
	n.Identifier, _ = hex.DecodeString("a06a81c827cab983")

	return n
}

