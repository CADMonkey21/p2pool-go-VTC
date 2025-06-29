package net

import (
	"encoding/hex"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/verthash-go"
)

func Vertcoin(testnet bool) Network {
	logging.Infof("--> [DEBUG] Attempting to initialize Verthash...")
	
	vh, err := verthash.NewVerthash("verthash.dat", false)
	
	logging.Infof("--> [DEBUG] Verthash initialization function has returned.")

	if err != nil {
		logging.Errorf("--> [DEBUG] Verthash initialization returned an error: %v", err)
		panic(err)
	}

	logging.Infof("--> [DEBUG] Verthash initialization successful!")

	n := Network{
		P2PPort:         config.Active.P2PPort,
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
	
	n.MessagePrefix, _ = hex.DecodeString("1c0c1c71cc197bc1") 
	
	n.Identifier, _ = hex.DecodeString("a06a81c827cab983")

	return n
}
