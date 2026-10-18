package stratum

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
)

type ShareDatum struct {
	Work        float64
	IsDead      bool
	WorkerName  string
	ShareTarget *big.Int
	PubKeyHash  []byte
}

type RateMonitor struct {
	maxLookbackTime time.Duration
	datums          []struct {
		Timestamp float64
		Datum     ShareDatum
	}
	firstTimestamp float64
	mutex          sync.Mutex
}

func NewRateMonitor(maxLookbackTime time.Duration) *RateMonitor {
	return &RateMonitor{
		maxLookbackTime: maxLookbackTime,
		datums: make([]struct {
			Timestamp float64
			Datum     ShareDatum
		}, 0),
	}
}

func (rm *RateMonitor) AddDatum(datum ShareDatum) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	rm.prune()

	t := float64(time.Now().UnixNano()) / float64(time.Second)
	if rm.firstTimestamp == 0 {
		rm.firstTimestamp = t
	}
	rm.datums = append(rm.datums, struct {
		Timestamp float64
		Datum     ShareDatum
	}{Timestamp: t, Datum: datum})
}

func (rm *RateMonitor) GetDatumsInLast(dt time.Duration) ([]ShareDatum, time.Duration) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	rm.prune()

	now := float64(time.Now().UnixNano()) / float64(time.Second)

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

	// Calculate the true active duration for the requested window (dt)
	var actualDuration time.Duration
	if rm.firstTimestamp == 0 {
		actualDuration = time.Second
	} else {
		elapsedSinceFirst := now - rm.firstTimestamp
		if elapsedSinceFirst < dt.Seconds() {
			actualDuration = time.Duration(elapsedSinceFirst * float64(time.Second))
		} else {
			actualDuration = dt
		}
	}

	if actualDuration < time.Second {
		actualDuration = time.Second
	}
	return filteredDatums, actualDuration
}

func (rm *RateMonitor) prune() {
	if len(rm.datums) == 0 {
		return
	}
	earliestTime := float64(time.Now().UnixNano())/float64(time.Second) - rm.maxLookbackTime.Seconds()
	firstValidIndex := -1
	for i, entry := range rm.datums {
		if entry.Timestamp > earliestTime {
			firstValidIndex = i
			break
		}
	}
	if firstValidIndex == -1 {
		rm.datums = rm.datums[:0]
	} else if firstValidIndex > 0 {
		rm.datums = rm.datums[firstValidIndex:]
	}
}

type Client struct {
	Conn                 net.Conn
	Encoder              *json.Encoder
	Reader               *bufio.Reader
	Writer               *bufio.Writer
	ID                   uint64
	SubscriptionID       string
	ExtraNonce1          string
	Nonce2Size           int
	Mutex                sync.Mutex
	WorkerName           string
	Authorized           bool
	LastActivity         time.Time
	ActiveJobs           map[string]*Job
	CurrentDifficulty    float64
	ShareTimestamps      []float64
	LocalRateMonitor     *RateMonitor
	LocalAddrRateMonitor *RateMonitor
	closed               atomic.Bool
	AcceptedShares       uint64
	RejectedShares       uint64
}

func NewClient(conn net.Conn) *Client {
	extraNonce1Bytes := make([]byte, 4)
	_, err := rand.Read(extraNonce1Bytes)
	if err != nil {
		panic(fmt.Sprintf("Failed to generate ExtraNonce1: %v", err))
	}

	writer := bufio.NewWriter(conn)
	client := &Client{
		Conn:                 conn,
		Writer:               writer,
		Encoder:              json.NewEncoder(writer),
		Reader:               bufio.NewReader(conn),
		ID:                   uint64(time.Now().UnixNano()),
		ExtraNonce1:          hex.EncodeToString(extraNonce1Bytes),
		Nonce2Size:           4,
		Authorized:           false,
		LastActivity:         time.Now(),
		ActiveJobs:           make(map[string]*Job),
		CurrentDifficulty:    -1,
		ShareTimestamps:      make([]float64, 0),
		LocalRateMonitor:     NewRateMonitor(10 * time.Minute),
		LocalAddrRateMonitor: NewRateMonitor(10 * time.Minute),
	}
	client.closed.Store(false)

	return client
}

func (c *Client) GetRejectedRate() float64 {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	totalShares := c.AcceptedShares + c.RejectedShares
	if totalShares == 0 {
		return 0.0
	}
	return float64(c.RejectedShares) / float64(totalShares)
}

func (c *Client) GetAverageShareTime() time.Duration {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	datums, span := c.LocalRateMonitor.GetDatumsInLast(10 * time.Minute)
	if len(datums) == 0 || span.Seconds() <= 0 {
		return 0
	}
	return time.Duration(span.Seconds()/float64(len(datums))) * time.Second
}

func (c *Client) send(v interface{}) error {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	if c.Conn == nil || c.closed.Load() {
		return errors.New("connection is closed")
	}

	if logging.GetLogLevel() >= logging.LogLevelDebug {
		jsonData, err := json.Marshal(v)
		if err != nil {
			logging.Errorf("Stratum: FAILED TO MARSHAL JSON FOR DEBUG LOG: %v", err)
		} else {
			logging.Debugf("Stratum: SENDING RAW JSON -> %s", string(jsonData))
		}
	}

	c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	if err := c.Encoder.Encode(v); err != nil {
		return err
	}
	return c.Writer.Flush()
}
