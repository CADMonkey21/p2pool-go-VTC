package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
)

type P2PoolMessage interface {
	Command() string
	FromBytes(b []byte) error
	ToBytes() ([]byte, error)
}

type P2PoolConnection struct {
	conn         net.Conn
	network      p2pnet.Network
	connLock     sync.Mutex
	Incoming     chan P2PoolMessage
	Outgoing     chan P2PoolMessage
	Disconnected chan bool
}

func NewP2PoolConnection(c net.Conn, n p2pnet.Network) *P2PoolConnection {
	in := make(chan P2PoolMessage, 10)
	out := make(chan P2PoolMessage, 10)
	dis := make(chan bool, 1) // Need a buffer here. Client could be processing a message when disconnect happens
	p2pc := &P2PoolConnection{
		conn:         c,
		network:      n,
		connLock:     sync.Mutex{},
		Incoming:     in,
		Outgoing:     out,
		Disconnected: dis,
	}

	go p2pc.IncomingLoop()
	go p2pc.OutgoingLoop()
	return p2pc
}

func (c *P2PoolConnection) ReadBytes(len int) ([]byte, error) {
	buf := make([]byte, len)
	if len == 0 {
		return buf, nil
	}
	read := 0
	for {
		r, err := c.conn.Read(buf[read:])
		if err != nil {
			return nil, err
		}
		if r == 0 {
			return nil, fmt.Errorf("Could not read enough bytes")
		}
		read += r
		if read == len {
			break
		}
	}

	return buf, nil
}

func (c *P2PoolConnection) IncomingLoop() {
	defer func() {
		select {
		case c.Disconnected <- true:
		default:
		}
	}()

	for {
		prefix, err := c.ReadBytes(len(c.network.MessagePrefix))
		if err != nil {
			// Do not log here, as this is the normal way a connection closes.
			// The Disconnected channel will signal the error.
			break
		}

		if !bytes.Equal(prefix, c.network.MessagePrefix) {
			logging.Errorf("Received transport message with mismatching prefix")
			break
		}

		commandBytes, err := c.ReadBytes(12)
		if err != nil {
			break
		}
		command := string(bytes.Trim(commandBytes, "\x00"))

		var length int32
		err = binary.Read(c.conn, binary.LittleEndian, &length)
		if err != nil {
			break
		}

		checksum, err := c.ReadBytes(4)
		if err != nil {
			break
		}

		payload, err := c.ReadBytes(int(length))
		if err != nil {
			break
		}
		calcChecksum := sha256.Sum256(payload)
		calcChecksum = sha256.Sum256(calcChecksum[:])
		if !bytes.Equal(checksum, calcChecksum[:4]) {
			logging.Errorf("Wrong checksum - expected [%x] got [%x]", calcChecksum, checksum)
			break
		}

		logging.Debugf("Received message of type [%s] length [%d]", command, length)

		msg, err := c.ParseMessage(command, payload)
		if err != nil {
			logging.Errorf("Could not parse message: %s", err.Error())
			break
		}
		c.Incoming <- msg
	}
}

func (c *P2PoolConnection) ParseMessage(command string, payload []byte) (P2PoolMessage, error) {
	var msg P2PoolMessage
	switch command {
	case "version":
		msg = &MsgVersion{}
	case "ping":
		msg = &MsgPing{}
	case "addrme":
		msg = &MsgAddrMe{}
	case "getaddrs":
		msg = &MsgGetAddrs{}
	case "addrs":
		msg = &MsgAddrs{}
	case "have_tx":
		msg = &MsgHaveTx{}
	case "bestblock":
		msg = &MsgBestBlock{}
	case "remember_tx":
		msg = &MsgRememberTx{}
	case "forget_tx":
		msg = &MsgForgetTx{}
	case "losing_tx":
		msg = &MsgLosingTx{}
	case "shares":
		msg = &MsgShares{}
	case "sharereply":
		msg = &MsgShareReply{}
	case "sharereq":
		msg = &MsgShareReq{}
	default:
		return msg, fmt.Errorf("Unknown command %s", command)
	}
	err := msg.FromBytes(payload)
	return msg, err
}

func (c *P2PoolConnection) OutgoingLoop() {
	for msg := range c.Outgoing {
		payload, err := msg.ToBytes()
		if err != nil {
			continue
		}

		calcChecksum := sha256.Sum256(payload)
		calcChecksum = sha256.Sum256(calcChecksum[:])
		command := make([]byte, 12)
		copy(command, []byte(msg.Command()))

		logging.Debugf("Sending p2pool message [%s] length [%d]", msg.Command(), len(payload))

		c.conn.Write(c.network.MessagePrefix)
		c.conn.Write(command)
		binary.Write(c.conn, binary.LittleEndian, int32(len(payload)))
		c.conn.Write(calcChecksum[:4])
		c.conn.Write(payload)
	}
}

func (c *P2PoolConnection) Close() error {
	return c.conn.Close()
}

// GetLocalAddr is the new helper function needed by the Peer object.
func (c *P2PoolConnection) GetLocalAddr() net.Addr {
	return c.conn.LocalAddr()
}
