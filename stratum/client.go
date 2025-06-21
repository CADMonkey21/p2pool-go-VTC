package stratum

import (
	"bufio"
	crand "crypto/rand" // Use crypto/rand for secure ExtraNonce1
	"encoding/hex"
	"encoding/json" 
	"fmt" // Added missing import
	"math/big" 
	"math/rand" // Added missing import for rand.Uint64
	"net"
	"sync"
	"time"
)

// ShareDatum represents a single data point for rate monitoring,
// mirroring the 'datum' dictionary in Python's RateMonitor.
type ShareDatum struct {
	Work        float64    // Equivalent to bitcoin_data.target_to_average_attempts(target)
	IsDead      bool       // Equivalent to 'dead' (not on_time)
	WorkerName  string     // Equivalent to 'user'
	ShareTarget *big.Int   // Corrected: Changed to *big.Int to store the target directly
	PubKeyHash  []byte     // For per-address rate tracking
}

// RateMonitor tracks historical data points over a specified time window,
// mirroring Python's RateMonitor.
type RateMonitor struct {
	maxLookbackTime time.Duration // Max time to keep data points
	datums          []struct {
		Timestamp float64    // High-precision Unix timestamp (seconds)
		Datum     ShareDatum
	}
	firstTimestamp float64 // Timestamp of the very first datum added
	mutex sync.Mutex // Mutex to protect internal state
}

// NewRateMonitor creates a new RateMonitor instance.
func NewRateMonitor(maxLookbackTime time.Duration) *RateMonitor {
	return &RateMonitor{
		maxLookbackTime: maxLookbackTime,
		datums:          make([]struct{ Timestamp float64; Datum ShareDatum }, 0),
	}
}

// AddDatum adds a new data point to the RateMonitor.
func (rm *RateMonitor) AddDatum(datum ShareDatum) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	rm.prune() // Clean up old data points first

	t := float64(time.Now().UnixNano()) / float64(time.Second)
	if rm.firstTimestamp == 0 { // Check if it's the very first datum
		rm.firstTimestamp = t
	}

	rm.datums = append(rm.datums, struct{ Timestamp float64; Datum ShareDatum }{Timestamp: t, Datum: datum})
}

// GetDatumsInLast returns data points within the last 'dt' duration.
// Returns the list of datums and the actual duration of the window.
func (rm *RateMonitor) GetDatumsInLast(dt time.Duration) ([]ShareDatum, time.Duration) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	rm.prune() // Clean up old data points

	now := float64(time.Now().UnixNano()) / float64(time.Second)
	
	// Ensure dt is not greater than maxLookbackTime
	if dt > rm.maxLookbackTime {
		dt = rm.maxLookbackTime
	}

	filteredDatums := make([]ShareDatum, 0)
	earliestTime := now - dt.Seconds()

	for _, entry := range rm.datums {
		if entry.Timestamp > earliestTime {
			filteredDatums = append(filteredDatums, entry.Datum)
		}
	}
	
	actualDuration := dt
	if rm.firstTimestamp != 0 && (now - rm.firstTimestamp) < dt.Seconds() {
		actualDuration = time.Duration((now - rm.firstTimestamp) * float64(time.Second))
	} else if len(rm.datums) > 0 && (rm.datums[len(rm.datums)-1].Timestamp - rm.datums[0].Timestamp) > 0 {
		// Use the actual span of collected data if it's less than dt
		actualDuration = time.Duration((rm.datums[len(rm.datums)-1].Timestamp - rm.datums[0].Timestamp) * float64(time.Second))
	} else if len(rm.datums) > 0 {
		actualDuration = time.Duration(1 * float64(time.Second)) // At least 1 second if data exists but span is 0
	} else {
		actualDuration = time.Duration(0) // No data
	}

	// Prevent division by zero if duration is extremely small
	if actualDuration <= 0 {
		actualDuration = time.Duration(1 * float64(time.Second)) 
	}


	return filteredDatums, actualDuration
}

// prune removes data points older than maxLookbackTime.
func (rm *RateMonitor) prune() {
	if len(rm.datums) == 0 {
		return
	}
	
	earliestTime := float64(time.Now().UnixNano())/float64(time.Second) - rm.maxLookbackTime.Seconds()

	// Find the index of the first datum that is still within the lookback window
	firstValidIndex := 0
	for i, entry := range rm.datums {
		if entry.Timestamp > earliestTime {
			firstValidIndex = i
			break
		}
	}

	// Trim the slice to keep only valid datums
	if firstValidIndex > 0 {
		rm.datums = rm.datums[firstValidIndex:]
	}
}


// Client represents a connected Stratum miner client.
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
	ActiveJobs       map[string]*Job
	CurrentDifficulty float64
	ShareTimestamps   []float64 // Corrected: Changed to float64 for high-precision timestamps
	
	// New: RateMonitors for Vardiff calculations
	LocalRateMonitor    *RateMonitor // Tracks overall local shares
	LocalAddrRateMonitor *RateMonitor // Tracks shares per payout address
}

// NewClient creates a new Stratum Client instance.
func NewClient(conn net.Conn) *Client {
	extraNonce1Bytes := make([]byte, 4)
	// Corrected: Use crypto/rand for cryptographically secure random bytes
	_, err := crand.Read(extraNonce1Bytes) 
	if err != nil {
		panic(fmt.Sprintf("Failed to generate ExtraNonce1: %v", err)) // Should not happen
	}

	return &Client{
		Conn:              conn,
		Encoder:           json.NewEncoder(conn),
		Reader:            bufio.NewReader(conn),
		ID:                rand.Uint64(), // Uses math/rand, which is fine for non-cryptographic IDs
		ExtraNonce1:       hex.EncodeToString(extraNonce1Bytes),
		Nonce2Size:        4,
		Authorized:        false,
		LastActivity:      time.Now(),
		ActiveJobs:        make(map[string]*Job),
		CurrentDifficulty: -1,
		ShareTimestamps:   make([]float64, 0), // Corrected: Initialized as float64 slice
		
		// Initialize RateMonitors
		LocalRateMonitor:    NewRateMonitor(10 * time.Minute), // Example: 10 minutes lookback
		LocalAddrRateMonitor: NewRateMonitor(10 * time.Minute), // Example: 10 minutes lookback
	}
}
