package wire

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
)

type P2PoolConnection struct {
	Connection   net.Conn
	Network      p2pnet.Network
	Disconnected chan bool
	Incoming     chan P2PoolMessage
	Outgoing     chan P2PoolMessage
	closeOnce    sync.Once
}

func NewP2PoolConnection(conn net.Conn, n p2pnet.Network) *P2PoolConnection {
	p := &P2PoolConnection{
		Connection:   conn,
		Network:      n,
		Disconnected: make(chan bool),
		Incoming:     make(chan P2PoolMessage, 100),
		Outgoing:     make(chan P2PoolMessage, 100),
	}

	go p.readLoop()
	go p.writeLoop()
	return p
}

func (p *P2PoolConnection) Close() {
	p.closeOnce.Do(func() {
		p.Connection.Close()
		close(p.Disconnected)
		close(p.Incoming)
	})
}

func (p *P2PoolConnection) readLoop() {
	defer p.Close()
	for {
		// Read message length (4 bytes)
		lenBytes := make([]byte, 4)
		_, err := io.ReadFull(p.Connection, lenBytes)
		if err != nil {
			if err != io.EOF {
				logging.Errorf("P2P Read Error (length): %v", err)
			}
			return
		}
		msgLen := binary.LittleEndian.Uint32(lenBytes)

		// Read command (12 bytes, null-padded)
		cmdBytes := make([]byte, 12)
		_, err = io.ReadFull(p.Connection, cmdBytes)
		if err != nil {
			logging.Errorf("P2P Read Error (command): %v", err)
			return
		}
		command := string(bytes.TrimRight(cmdBytes, "\x00"))

		// Read payload
		payloadLen := msgLen
		payload := make([]byte, payloadLen)
		_, err = io.ReadFull(p.Connection, payload)
		if err != nil {
			logging.Errorf("P2P Read Error (payload for %s): %v", command, err)
			return
		}

		msg, err := MakeMessage(command)
		if err != nil {
			logging.Warnf("Received unknown message command: %s", command)
			continue
		}

		if err := msg.FromBytes(payload); err != nil {
			logging.Errorf("Failed to decode message %s: %v", command, err)
			continue
		}

		p.Incoming <- msg
	}
}

func (p *P2PoolConnection) writeLoop() {
	for msg := range p.Outgoing {
		payload, err := msg.ToBytes()
		if err != nil {
			logging.Errorf("Failed to serialize message %s: %v", msg.Command(), err)
			continue
		}

		// Prepare command (12 bytes, null-padded)
		cmdBytes := make([]byte, 12)
		copy(cmdBytes, msg.Command())

		// Prepare length (4 bytes)
		lenBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(lenBytes, uint32(len(payload)))

		// Write full message to the socket
		fullMessage := append(lenBytes, cmdBytes...)
		fullMessage = append(fullMessage, payload...)

		_, err = p.Connection.Write(fullMessage)
		if err != nil {
			logging.Errorf("Failed to write message %s: %v", msg.Command(), err)
		}
	}
}
