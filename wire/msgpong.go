package wire

// MsgPong implements the Message interface and represents a p2pool pong message.
// It is sent in response to a ping message.
type MsgPong struct{}

// ToBytes returns an empty byte slice as pong has no payload.
func (m *MsgPong) ToBytes() ([]byte, error) {
	return []byte{}, nil
}

// FromBytes does nothing as pong has no payload.
func (m *MsgPong) FromBytes(b []byte) error {
	return nil
}

// Command returns the protocol command string for the message.
func (m *MsgPong) Command() string {
	return "pong"
}

