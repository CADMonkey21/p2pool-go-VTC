package wire

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"io"
	"math/big"
	"net"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-VTC/net"
)

type P2PoolConnection struct {
	Connection   net.Conn
	Network      p2pnet.Network
	Disconnected chan bool
	Incoming     chan P2PoolMessage
	Outgoing     chan P2PoolMessage
}

func NewP2PoolConnection(conn net.Conn, n p2pnet.Network) *P2PoolConnection {
	p := &P2PoolConnection{
		Connection:   conn,
		Network:      n,
		Disconnected: make(chan bool),
		Incoming:     make(chan P2PoolMessage, 100),
		Outgoing:     make(chan P2PoolMessage, 100),
	}
	gob.Register(&chainhash.Hash{})
	gob.Register(&big.Int{})
	gob.Register(&MsgShares{})

	go p.readLoop()
	go p.writeLoop()
	return p
}

func (p *P2PoolConnection) Close() {
	p.Connection.Close()
	close(p.Disconnected)
}

func (p *P2PoolConnection) readLoop() {
	reader := bufio.NewReader(p.Connection)
	for {
		msgTypeBytes := make([]byte, 12)
		_, err := io.ReadFull(reader, msgTypeBytes)
		if err != nil {
			p.Close()
			return
		}

		command := string(bytes.Trim(msgTypeBytes, "\x00"))
		msg, err := MakeMessage(command)
		if err != nil {
			logging.Warnf("Could not make message for command %s: %v", command, err)
			var length uint32
			binary.Read(reader, binary.LittleEndian, &length)
			io.CopyN(io.Discard, reader, int64(length+4)) //+4 for checksum
			continue
		}
		
		var msgLength uint32
		err = binary.Read(reader, binary.LittleEndian, &msgLength)
		if err != nil {
			p.Close()
			return
		}

		payload := make([]byte, msgLength)
		_, err = io.ReadFull(reader, payload)
		if err != nil {
			p.Close()
			return
		}

		checksum := make([]byte, 4)
		_, err = io.ReadFull(reader, checksum)
		if err != nil {
			p.Close()
			return
		}
		
		if command == "shares" {
			decoder := gob.NewDecoder(bytes.NewReader(payload))
			err = decoder.Decode(msg)
		} else {
			err = msg.FromBytes(payload)
		}

		if err != nil {
			logging.Errorf("Could not deserialize message [%s]: %v", command, err)
			continue
		}
		p.Incoming <- msg
	}
}

func (p *P2PoolConnection) writeLoop() {
	for msg := range p.Outgoing {
		var payload []byte
		var err error
		
		if msg.Command() == "shares" {
			var buf bytes.Buffer
			encoder := gob.NewEncoder(&buf)
			err = encoder.Encode(msg)
			payload = buf.Bytes()
		} else {
			payload, err = msg.ToBytes()
		}

		if err != nil {
			logging.Errorf("Failed to serialize message %s: %v", msg.Command(), err)
			continue
		}

		err = p.writeMessage(msg.Command(), payload)
		if err != nil {
			logging.Errorf("Failed to write message %s: %v", msg.Command(), err)
		}
	}
}

func (p *P2PoolConnection) writeMessage(command string, payload []byte) error {
	buf := new(bytes.Buffer)
	cmdBytes := make([]byte, 12)
	copy(cmdBytes, command)
	buf.Write(cmdBytes)
	binary.Write(buf, binary.LittleEndian, uint32(len(payload)))
	checksum := []byte{0x00, 0x00, 0x00, 0x00} // Placeholder checksum
	buf.Write(checksum)
	buf.Write(payload)

	_, err := p.Connection.Write(buf.Bytes())
	return err
}
