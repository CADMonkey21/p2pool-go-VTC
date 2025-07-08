package wire

import (
	"fmt"
	"net"
	"time"

	p2pnet "github.com/CADMonkey21/p2pool-go-vtc/net"
)

func NewP2PoolClient(ip net.IP, port int, network p2pnet.Network) (*P2PoolConnection, error) {
	if port == 0 {
		port = network.P2PPort
	}
	d := net.Dialer{Timeout: time.Second * 5}
	conn, err := d.Dial("tcp", fmt.Sprintf("%s:%d", ip.String(), port))
	if err != nil {
		return nil, err
	}
	return NewP2PoolConnection(conn, network), nil
}
