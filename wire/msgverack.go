package wire

// MsgVerAck defines a p2pool verack message. It is sent in reply to a version
// message which the receiving peer finds acceptable.
type MsgVerAck struct{}

// ToBytes returns an empty byte slice as verack has no payload.
func (m *MsgVerAck) ToBytes() ([]byte, error) {
	return []byte{}, nil
}

// FromBytes does nothing as verack has no payload.
func (m *MsgVerAck) FromBytes(b []byte) error {
	return nil
}

// Command returns the protocol command string for the message.
func (m *MsgVerAck) Command() string {
	return "verack"
}
