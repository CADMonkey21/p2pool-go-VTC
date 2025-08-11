package wire

import (
	"fmt"
	"net"

	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
)

type P2PoolListener struct {
	listen  net.Listener
	network p2pnet.Network
}

func NewP2PoolListener(port int, network p2pnet.Network) (*P2PoolListener, error) {
	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	return &P2PoolListener{
		listen:  listen,
		network: network,
	}, nil
}

func (p2pl *P2PoolListener) Accept() (*P2PoolConnection, error) {
	conn, err := p2pl.listen.Accept()
	if err != nil {
		return nil, err
	}

	return NewP2PoolConnection(conn, p2pl.network), nil
}
