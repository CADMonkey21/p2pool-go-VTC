package net

import "github.com/gertjaap/p2pool-go/logging"

var ActiveNetwork Network

type Network struct {
	MessagePrefix []byte
	Identifier    []byte
	P2PPort       int
	RPCPort       int
	WorkerPort    int
	ChainLength   int
	Softforks     []string
	SeedHosts     []string
	POWHash       func([]byte) []byte
}

func SetNetwork(net string) {
	switch {
	case net == "vertcoin" || net == "Vertcoin":
		ActiveNetwork = Vertcoin()

	default:
		logging.Errorf("%s is currently not supported. See the README for supported networks", net)
		panic("ERROR: Invalid network name!")
	}
}
