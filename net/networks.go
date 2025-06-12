package net

import (
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/gertjaap/p2pool-go/logging"
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
	Softforks        []string
	SeedHosts        []string
	POWHash          func([]byte) []byte
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

// getVtcChainConfig now creates a complete and correct set of parameters for Vertcoin.
func getVtcChainConfig(testnet bool) chaincfg.Params {
	if testnet {
		// TODO: Add testnet params later
		return chaincfg.TestNet3Params
	}

	// Start with a copy of Bitcoin's mainnet parameters
	params := chaincfg.MainNetParams

	// Overwrite the fields specific to Vertcoin
	params.Name = "vertcoin"
	params.Net = 0xdab5bffa // Magic number from Vertcoin source
	params.PubKeyHashAddrID = 0x47 // 71 (V...)
	params.ScriptHashAddrID = 0x05 // 5 (3...)
	params.Bech32HRPSegwit = "vtc" // vtc1...

	// These are the new, critical parameters for Segwit address decoding
	params.WitnessPubKeyHashAddrID = 0x06 // P2WPKH prefix
	params.WitnessScriptHashAddrID = 0x0A // P2WSH prefix
	
	return params
}
