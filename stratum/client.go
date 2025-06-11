package stratum

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"net"
	"sync"
	"time"
)

// Client represents a single connected miner.
type Client struct {
	Conn             net.Conn
	Encoder          *json.Encoder
	Reader           *bufio.Reader
	ID               uint64
	SubscriptionID   string
	ExtraNonce1      string
	Nonce2Size       int
	Mutex            sync.Mutex
	WorkerName       string
	Authorized       bool
	LastActivity     time.Time
	ActiveJobs       map[string]bool // NEW: Keep track of jobs sent to this miner
}

// NewClient creates a new Stratum client object.
func NewClient(conn net.Conn) *Client {
	extraNonce1Bytes := make([]byte, 4)
	rand.Read(extraNonce1Bytes)

	return &Client{
		Conn:           conn,
		Encoder:        json.NewEncoder(conn),
		Reader:         bufio.NewReader(conn),
		ID:             rand.Uint64(),
		ExtraNonce1:    hex.EncodeToString(extraNonce1Bytes),
		Nonce2Size:     4,
		Authorized:     false,
		LastActivity:   time.Now(),
		ActiveJobs:     make(map[string]bool), // NEW: Initialize the job map
	}
}
