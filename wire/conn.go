package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
)

type P2PoolConnection struct {
	Connection   net.Conn
	Network      p2pnet.Network
	Incoming     chan P2PoolMessage
	Outgoing     chan P2PoolMessage
	Disconnected chan bool
}

func NewP2PoolConnection(conn net.Conn, network p2pnet.Network) *P2PoolConnection {
	pc := P2PoolConnection{
		Connection:   conn,
		Network:      network,
		Incoming:     make(chan P2PoolMessage),
		Outgoing:     make(chan P2PoolMessage),
		Disconnected: make(chan bool),
	}
	go pc.readMessage()
	go pc.writeMessage()
	return &pc
}

func (c *P2PoolConnection) handleError(err error) {
	logging.Errorf("Handling error: %v", err)
	select {
	case c.Disconnected <- true:
	default:
	}
	c.Connection.Close()
}

func (c *P2PoolConnection) readMessage() {
	for {
		c.Connection.SetReadDeadline(time.Now().Add(600 * time.Second))
		hdr := make([]byte, 28)
		_, err := io.ReadFull(c.Connection, hdr)
		if err != nil {
			c.handleError(err)
			return
		}

		r := bytes.NewReader(hdr)

		var magic []byte = make([]byte, 8)
		_, err = io.ReadFull(r, magic)
		if err != nil {
			c.handleError(err)
			return
		}
		if !bytes.Equal(magic, c.Network.MessagePrefix) {
			err = fmt.Errorf("received transport message with mismatching prefix. Expected %s, got %s", hex.EncodeToString(c.Network.MessagePrefix), hex.EncodeToString(magic))
			c.handleError(err)
			return
		}

		var cmd []byte = make([]byte, 12)
		_, err = io.ReadFull(r, cmd)
		if err != nil {
			c.handleError(err)
			return
		}

		var length uint32
		err = binary.Read(r, binary.LittleEndian, &length)
		if err != nil {
			c.handleError(err)
			return
		}

		var checksum []byte = make([]byte, 4)
		_, err = io.ReadFull(r, checksum)
		if err != nil {
			c.handleError(err)
			return
		}

		var payload []byte = make([]byte, length)
		if length > 0 {
			_, err = io.ReadFull(c.Connection, payload)
			if err != nil {
				c.handleError(err)
				return
			}
		}

		payloadChecksum := sha256.Sum256(payload)
		payloadChecksum = sha256.Sum256(payloadChecksum[:])
		if !bytes.Equal(checksum, payloadChecksum[0:4]) {
			c.handleError(errors.New("Checksum mismatch"))
			return
		}

		command := string(bytes.Trim(cmd, "\x00"))

		var msg P2PoolMessage
		switch command {
		case "version":
			msg = &MsgVersion{}
		case "verack":
			msg = &MsgVerAck{}
		case "addrs":
			msg = &MsgAddrs{}
		case "shares":
			msg = &MsgShares{}
		case "sharereply":
			msg = &MsgShareReply{}
		case "ping":
			msg = &MsgPing{}
		case "get_shares":
			msg = &MsgGetShares{}
		}

		if msg != nil {
			err := msg.FromBytes(payload)
			if err == nil {
				c.Incoming <- msg
			}
		}
	}
}

func (c *P2PoolConnection) writeMessage() {
	for m := range c.Outgoing {
		payload, _ := m.ToBytes()

		hdr := make([]byte, 28)
		w := bytes.NewBuffer(hdr[0:0])

		w.Write(c.Network.MessagePrefix)

		cmd := make([]byte, 12)
		copy(cmd, m.Command())
		w.Write(cmd)

		binary.Write(w, binary.LittleEndian, int32(len(payload)))

		checksum := sha256.Sum256(payload)
		checksum = sha256.Sum256(checksum[:])
		w.Write(checksum[0:4])

		_, err := c.Connection.Write(w.Bytes())
		if err != nil {
			c.handleError(err)
		}
		if len(payload) > 0 {
			_, err = c.Connection.Write(payload)
			if err != nil {
				c.handleError(err)
			}
		}
	}
}

func (c *P2PoolConnection) Close() {
	c.Connection.Close()
}
