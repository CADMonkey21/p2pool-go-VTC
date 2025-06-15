package net

import (
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/gertjaap/p2pool-go/logging"
	// Import the verthash library here
	"github.com/gertjaap/verthash-go"
)

var ActiveNetwork Network
var ActiveChainConfig chaincfg.Params

type Network struct {
	MessagePrefix    []byte
	Identifier       []byte
	P2PPort          int
	StandardP2PPort  int
	ProtocolVersion  int32
	RPCPort          int
	WorkerPort       int
	ChainLength      int
	SeedHosts        []string
	// The POWHash function signature is correct
	POWHash          func([]byte) []byte
	// NEW: A field to hold our initialized Verthash instance
	Verthash         *verthash.Verthash
}

func SetNetwork(net string, testnet bool) {
	switch {
	case net == "vertcoin" || net == "Vertcoin":
		ActiveNetwork = Vertcoin(testnet)
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
