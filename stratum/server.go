package stratum

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/gertjaap/p2pool-go/config" // <-- Corrected
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/work"
)

type StratumServer struct {
	workManager *work.WorkManager
}

func NewStratumServer(wm *work.WorkManager) *StratumServer {
	return &StratumServer{
		workManager: wm,
	}
}

func (s *StratumServer) ListenForMiners() {
	addr := fmt.Sprintf(":%d", config.Active.StratumPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logging.Fatalf("Stratum: Failed to start listener on %s: %v", addr, err)
		return
	}
	defer listener.Close()

	logging.Infof("Stratum: Listening for miners on port %s", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			logging.Warnf("Stratum: Failed to accept new connection: %v", err)
			continue
		}
		go s.handleMinerConnection(conn)
	}
}

func (s *StratumServer) handleMinerConnection(conn net.Conn) {
	client := NewClient(conn)
	logging.Infof("Stratum: New miner connection from %s (ID: %d)", client.Conn.RemoteAddr(), client.ID)
	defer conn.Close()

	go s.vardiffLoop(client)

	decoder := json.NewDecoder(client.Reader)
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Minute))

		var req JSONRPCRequest
		err := decoder.Decode(&req)
		if err != nil {
			if err != io.EOF {
				logging.Warnf("Stratum: Error reading from miner %s: %v", conn.RemoteAddr(), err)
			}
			break
		}

		client.LastActivity = time.Now()

		switch req.Method {
		case "mining.subscribe":
			s.handleSubscribe(client, &req)
		case "mining.authorize":
			s.handleAuthorize(client, &req)
		case "mining.submit":
			s.handleSubmit(client, &req)
		default:
			logging.Warnf("Stratum: Received unhandled method '%s' from %s", req.Method, conn.RemoteAddr())
		}
	}

	logging.Infof("Stratum: Miner %s disconnected.", conn.RemoteAddr())
}

func (s *StratumServer) handleSubmit(c *Client, req *JSONRPCRequest) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	logging.Infof("Stratum: Received mining.submit from %s", c.WorkerName)

	c.ShareTimestamps = append(c.ShareTimestamps, time.Now().Unix())

	response := JSONRPCResponse{ID: req.ID, Result: true, Error: nil}
	err := c.Encoder.Encode(response)
	if err != nil {
		logging.Warnf("Stratum: Failed to send submit response to %s: %v", c.Conn.RemoteAddr(), err)
	}
}

func (s *StratumServer) handleSubscribe(c *Client, req *JSONRPCRequest) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	logging.Infof("Stratum: Received mining.subscribe from %s", c.Conn.RemoteAddr())

	c.SubscriptionID = c.ExtraNonce1
	subscriptionDetails := []string{"mining.notify", c.SubscriptionID}

	result := []interface{}{
		subscriptionDetails,
		c.ExtraNonce1,
		c.Nonce2Size,
	}

	response := JSONRPCResponse{ID: req.ID, Result: result}
	err := c.Encoder.Encode(response)
	if err != nil {
		logging.Warnf("Stratum: Failed to send subscribe response to %s: %v", c.Conn.RemoteAddr(), err)
	}
}

func (s *StratumServer) handleAuthorize(c *Client, req *JSONRPCRequest) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	logging.Infof("Stratum: Received mining.authorize from %s", c.Conn.RemoteAddr())

	var params []string
	err := json.Unmarshal(*req.Params, &params)
	if err != nil || len(params) < 1 {
		logging.Warnf("Stratum: Failed to parse authorize params from %s", c.Conn.RemoteAddr())
		return
	}

	c.WorkerName = params[0]
	c.Authorized = true

	response := JSONRPCResponse{ID: req.ID, Result: true}
	err = c.Encoder.Encode(response)
	if err != nil {
		logging.Warnf("Stratum: Failed to send authorize response to %s: %v", c.Conn.RemoteAddr(), err)
	}

	logging.Infof("Stratum: Miner %s successfully authorized for worker %s", c.Conn.RemoteAddr(), c.WorkerName)
	
	s.sendDifficulty(c, config.Active.Vardiff.MinDiff)
	s.sendMiningJob(c)
}

func (s *StratumServer) sendDifficulty(c *Client, diff float64) {
	c.CurrentDifficulty = diff
	params := []interface{}{c.CurrentDifficulty}
	diffResponse := JSONRPCResponse{Method: "mining.set_difficulty", Params: params}
	err := c.Encoder.Encode(diffResponse)
	if err != nil {
		logging.Warnf("Stratum: Failed to send difficulty to %s: %v", c.Conn.RemoteAddr(), err)
	}
	logging.Infof("Stratum: Difficulty set to %f for miner %s", c.CurrentDifficulty, c.Conn.RemoteAddr())
}

func (s *StratumServer) sendMiningJob(c *Client) {
	s.workManager.TemplateMutex.RLock()
	var latestTemplate *work.BlockTemplate
	for _, t := range s.workManager.Templates {
		if latestTemplate == nil || t.Height > latestTemplate.Height {
			latestTemplate = t
		}
	}
	s.workManager.TemplateMutex.RUnlock()

	if latestTemplate == nil {
		logging.Warnf("Stratum: No block template available to send job to miner %s", c.Conn.RemoteAddr())
		return
	}

	jobID := fmt.Sprintf("job%d", rand.Intn(10000))
	newJob := &Job{
		ID:            jobID,
		BlockTemplate: latestTemplate,
		ExtraNonce1:   c.ExtraNonce1,
	}
	c.ActiveJobs[jobID] = newJob

	prevhash := latestTemplate.PreviousBlockHash
	coinb1 := "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0704"
	coinb2 := "000000000000000000000000000000"
	merkleBranch := []string{}
	blockVersion := "20000000"
	nbits := latestTemplate.Bits
	ntime := fmt.Sprintf("%x", latestTemplate.CurTime)
	cleanJobs := true

	params := []interface{}{jobID, prevhash, coinb1, coinb2, merkleBranch, blockVersion, nbits, ntime, cleanJobs}

	jobResponse := JSONRPCResponse{Method: "mining.notify", Params: params}
	err := c.Encoder.Encode(jobResponse)
	if err != nil {
		logging.Warnf("Stratum: Failed to send job to %s: %v", c.Conn.RemoteAddr(), err)
	}
	logging.Infof("Stratum: Sent new job %s to worker %s", jobID, c.WorkerName)
}

func (s *StratumServer) vardiffLoop(c *Client) {
	ticker := time.NewTicker(time.Duration(config.Active.Vardiff.RetargetTime) * time.Second)
	for {
		<-ticker.C

		c.Mutex.Lock()

		if !c.Authorized {
			c.Mutex.Unlock()
			return
		}

		if len(c.ShareTimestamps) < 4 {
			c.Mutex.Unlock()
			continue
		}

		timeWindow := c.ShareTimestamps[len(c.ShareTimestamps)-1] - c.ShareTimestamps[0]
		if timeWindow < 1 {
			timeWindow = 1
		}
		
		avgTime := float64(timeWindow) / float64(len(c.ShareTimestamps)-1)

		lowerBound := config.Active.Vardiff.TargetTime * (1 - config.Active.Vardiff.Variance)
		upperBound := config.Active.Vardiff.TargetTime * (1 + config.Active.Vardiff.Variance)

		if avgTime > upperBound || avgTime < lowerBound {
			newDiff := (c.CurrentDifficulty * config.Active.Vardiff.TargetTime) / avgTime

			if newDiff < config.Active.Vardiff.MinDiff {
				newDiff = config.Active.Vardiff.MinDiff
			}
			
			if newDiff != c.CurrentDifficulty {
				logging.Infof("Stratum: Retargeting miner %s from diff %f to %f (avg share time: %.2fs)", c.Conn.RemoteAddr(), c.CurrentDifficulty, newDiff, avgTime)
				c.Mutex.Unlock()
				s.sendDifficulty(c, newDiff)
				c.Mutex.Lock()
			}
		}

		if len(c.ShareTimestamps) > 20 {
			c.ShareTimestamps = c.ShareTimestamps[len(c.ShareTimestamps)-20:]
		}

		c.Mutex.Unlock()
	}
}
