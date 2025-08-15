package net

import (
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/verthash" // Import your new local verthash package
)

var ActiveNetwork Network
var ActiveChainConfig chaincfg.Params

type Network struct {
	MessagePrefix   []byte
	Identifier      []byte
	P2PPort         int
	StandardP2PPort int
	ProtocolVersion int32
	RPCPort         int
	WorkerPort      int
	ChainLength     int
	SeedHosts       []string
	POWHash         func([]byte) []byte
	Verthash        verthash.Verthasher
}

// FIX: SetNetwork now accepts the initialized verthasher as an argument
func SetNetwork(net string, testnet bool, vh verthash.Verthasher) {
	switch {
	case net == "vertcoin" || net == "Vertcoin":
		ActiveNetwork = Vertcoin(testnet)
		ActiveNetwork.Verthash = vh // Set the verthasher instance
		ActiveChainConfig = getVtcChainConfig(testnet)
	default:
		logging.Errorf("%s is currently not supported. See the README for supported networks", net)
		panic("ERROR: Invalid network name!")
	}
}

func getVtcChainConfig(testnet bool) chaincfg.Params {
	if testnet {
		return chaincfg.TestNet3Params
	}

	params := chaincfg.MainNetParams
	params.Name = "vertcoin"
	params.PubKeyHashAddrID = 0x47
	params.ScriptHashAddrID = 0x05
	params.Bech32HRPSegwit = "vtc"
	params.WitnessPubKeyHashAddrID = 0x06
	params.WitnessScriptHashAddrID = 0x0A

	return params
}
