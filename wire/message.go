package wire

// P2PoolMessage is a common interface for all p2pool messages.
// This allows us to handle them generically.
type P2PoolMessage interface {
	// ToBytes returns the serialized message payload.
	ToBytes() ([]byte, error)
	// FromBytes populates the message from a byte slice.
	FromBytes(b []byte) error
	// Command returns the protocol command string for the message.
	Command() string
}
