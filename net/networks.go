package net

import "github.com/gertjaap/p2pool-go/logging"

var ActiveNetwork Network

type Network struct {
	MessagePrefix    []byte
	Identifier       []byte
	P2PPort          int    // The port *our* node will listen on
	StandardP2PPort  int    // The port that other nodes on the network listen on
	ProtocolVersion  int32  // The P2P protocol version
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

	default:
		logging.Errorf("%s is currently not supported. See the README for supported networks", net)
		panic("ERROR: Invalid network name!")
	}
}
