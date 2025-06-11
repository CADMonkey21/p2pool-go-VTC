package net

import (
	"encoding/hex"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	verthash "github.com/gertjaap/verthash-go"
)

func Vertcoin(testnet bool) Network {
	if testnet {
		return TestnetVertcoin()
	}

	logging.Infof("Initializing Verthash... This will take a moment and consume >1GB of RAM.")
	vh, err := verthash.NewVerthash("verthash.dat", true)
	if err != nil {
		logging.Fatalf("Could not initialize verthash. Ensure verthash.dat exists and is valid: %v", err)
	}

	n := Network{
		P2PPort:          config.Active.P2PPort,
		StandardP2PPort:  9171,
		ProtocolVersion:  3501,
		RPCPort:          config.Active.RPCPort,
		WorkerPort:       config.Active.StratumPort,
		MessagePrefix:    mustDecodeHex("7c3614a6bcdcf784"),
		Identifier:       mustDecodeHex("a06a81c827cab983"),
		ChainLength:      5100,
		// --- Start of Patched Section ---
		// Updated with the new, active seed nodes you found.
		// Your local node is first for easy debugging.
		SeedHosts: []string{
			"127.0.0.1",
			"vtc-fl.javerity.com",
			"p2p-usa.xyz",
			"mindcraftblocks.com",
			"p2p-spb.xyz",
			"boofpool.ddns.net",
		},
		// --- End of Patched Section ---
		Softforks: []string{"bip34", "bip66", "bip65", "csv", "segwit", "taproot"},
		POWHash: func(b []byte) []byte {
			res, _ := vh.SumVerthash(b)
			return res[:]
		},
	}

	return n
}

func TestnetVertcoin() Network {
	logging.Fatalf("Testnet is not yet configured in vertcoin.go!")
	return Network{}
}

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
