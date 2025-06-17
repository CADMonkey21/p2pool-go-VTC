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
		StandardP2PPort: 9346, // Matches legacy Python p2pool-vtc/p2pool/networks/vertcoin.py
		ProtocolVersion: 3501, // Keeping at 3501 as per your preference.
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
	
	// Corrected to match the OBSERVED legacy Python P2Pool message prefix (from runtime logs)
	n.MessagePrefix, _ = hex.DecodeString("1c0c1c71cc197bc1") 
	
	n.Identifier, _ = hex.DecodeString("a06a81c827cab983")

	return n
}
