package stratum

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/work"
	"github.com/gertjaap/p2pool-go/wire"
)

// copyRaw creates a safe copy of a json.RawMessage to prevent buffer reuse issues.
func copyRaw(v *json.RawMessage) *json.RawMessage {
	if v == nil {
		return nil
	}
	tmp := make(json.RawMessage, len(*v))
	copy(tmp, *v)
	return &tmp
}

type StratumServer struct {
	workManager  *work.WorkManager
	peerManager  *p2p.PeerManager
	clients      map[uint64]*Client
	clientsMutex sync.RWMutex
	lastJob      *Job
	lastJobMutex sync.RWMutex
}

func NewStratumServer(wm *work.WorkManager, pm *p2p.PeerManager) *StratumServer {
	s := &StratumServer{
		workManager: wm,
		peerManager: pm,
		clients:     make(map[uint64]*Client),
	}
	go s.jobBroadcaster()
	go s.keepAliveLoop()
	return s
}

// GetLocalHashrate calculates the total hashrate of all connected miners.
func (s *StratumServer) GetLocalHashrate() float64 {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	totalHashrate := 0.0
	for _, client := range s.clients {
		client.Mutex.Lock()
		// Use a 10-minute lookback for hashrate calculation
		datums, actualDuration := client.LocalRateMonitor.GetDatumsInLast(10 * time.Minute)

		if len(datums) < 1 || actualDuration.Seconds() < 1 {
			client.Mutex.Unlock()
			continue
		}

		totalWork := 0.0
		for _, datum := range datums {
			totalWork += datum.Work
		}

		// Hashrate = (Total Difficulty Submitted * 2^32) / Time Period
		// The "Work" unit in RateMonitor is equivalent to difficulty.
		hashrate := (totalWork * math.Pow(2, 32)) / actualDuration.Seconds()
		totalHashrate += hashrate
		client.Mutex.Unlock()
	}
	return totalHashrate
}


func (s *StratumServer) dropClient(c *Client) {
	c.closed.Store(true)
	s.clientsMutex.Lock()
	if _, ok := s.clients[c.ID]; ok {
		c.Conn.Close()
		delete(s.clients, c.ID)
		logging.Infof("Stratum: Miner %s disconnected.", c.Conn.RemoteAddr())
	}
	s.clientsMutex.Unlock()
}

func (s *StratumServer) isClientActive(id uint64) bool {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()
	_, ok := s.clients[id]
	return ok
}

func (s *StratumServer) keepAliveLoop() {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.lastJobMutex.RLock()
		job := s.lastJob
		s.lastJobMutex.RUnlock()

		if job == nil {
			continue
		}

		s.clientsMutex.RLock()
		currentClients := make([]*Client, 0, len(s.clients))
		for _, client := range s.clients {
			currentClients = append(currentClients, client)
		}
		s.clientsMutex.RUnlock()

		if len(currentClients) > 0 {
			logging.Debugf("Stratum: Sending keep-alive job to %d miners", len(currentClients))
			for _, client := range currentClients {
				if client.Authorized && s.isClientActive(client.ID) {
					s.sendMiningJob(client, job.BlockTemplate, false)
				}
			}
		}
	}
}

func (s *StratumServer) jobBroadcaster() {
	for {
		template := <-s.workManager.NewBlockChan

		newJob := &Job{
			ID:            fmt.Sprintf("job%d", rand.Intn(10000)),
			BlockTemplate: template,
		}
		s.lastJobMutex.Lock()
		s.lastJob = newJob
		s.lastJobMutex.Unlock()

		s.clientsMutex.RLock()
		currentClients := make([]*Client, 0, len(s.clients))
		for _, c := range s.clients {
			currentClients = append(currentClients, c)
		}
		s.clientsMutex.RUnlock()
		
		if len(currentClients) > 0 {
			logging.Infof("Stratum: Broadcasting new job for height %d to %d miners", template.Height, len(currentClients))
			for _, client := range currentClients {
				if client.Authorized && s.isClientActive(client.ID) {
					s.sendMiningJob(client, template, true)
				}
			}
		}
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
	s.clientsMutex.Lock()
	s.clients[client.ID] = client
	s.clientsMutex.Unlock()

	defer s.dropClient(client)

	go s.vardiffLoop(client)
	decoder := json.NewDecoder(client.Reader)
	for {
		client.Conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		var req JSONRPCRequest
		err := decoder.Decode(&req)
		if err != nil {
			if err != io.EOF {
				logging.Warnf("Stratum: Error reading from miner %s: %v", client.Conn.RemoteAddr(), err)
			}
			return
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
}

func (s *StratumServer) handleSubmit(c *Client, req *JSONRPCRequest) {
	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 5 {
		logging.Warnf("Stratum: Malformed submit params from %s", c.Conn.RemoteAddr())
		return
	}

	workerName, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]
	logging.Infof("Stratum: Received mining.submit from %s for job %s", workerName, jobID)

	c.Mutex.Lock()
	job, jobExists := c.ActiveJobs[jobID]
	c.Mutex.Unlock()

	if !jobExists {
		logging.Warnf("Stratum: Received submission for unknown jobID %s", jobID)
		return
	}

	header, _, err := work.CreateHeader(job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, config.Active.PoolAddress)
	if err != nil {
		logging.Errorf("Stratum: Failed to create block header for validation: %v", err)
		return
	}

	powHash := p2pnet.ActiveNetwork.POWHash(header)
	logging.Debugf("PoW hash for submission: %s", hex.EncodeToString(work.ReverseBytes(powHash)))

	shareTargetBigInt := work.DiffToTarget(c.CurrentDifficulty)
	powHashInt := new(big.Int)
	powHashInt.SetBytes(work.ReverseBytes(powHash))

	shareAccepted := false
	if powHashInt.Cmp(shareTargetBigInt) <= 0 {
		logging.Infof("Stratum: SHARE ACCEPTED! Valid work from %s", c.WorkerName)
		c.LocalRateMonitor.AddDatum(ShareDatum{
			Work:        targetToAverageAttempts(shareTargetBigInt),
			IsDead:      false,
			WorkerName:  c.WorkerName,
			ShareTarget: shareTargetBigInt,
		})
		shareAccepted = true
		newShare, err := work.CreateShare(job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, config.Active.PoolAddress, s.workManager.ShareChain, c.CurrentDifficulty)
		if err != nil {
			logging.Errorf("Stratum: Could not create share object: %v", err)
		} else {
			s.workManager.ShareChain.AddShares([]wire.Share{*newShare})
			s.peerManager.Broadcast(&wire.MsgShares{Shares: []wire.Share{*newShare}})
		}
	} else {
		logging.Warnf("Stratum: Share rejected. Hash does not meet target.")
	}
	
	// This section is refactored to be more explicit for the compiler.
	var response JSONRPCResponse
	if req.ID == nil || string(*req.ID) == "null" {
		// Per legacy behavior, respond with an ID of 0.
		// We must create a valid json.RawMessage containing "0".
		rawID := json.RawMessage("0")
		response = JSONRPCResponse{ID: &rawID, Result: shareAccepted, Error: nil}
	} else {
		response = JSONRPCResponse{ID: copyRaw(req.ID), Result: shareAccepted, Error: nil}
	}
	
	if err := c.send(response); err != nil {
		logging.Warnf("Stratum: failed to send submit response to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	}
}

func (s *StratumServer) handleSubscribe(c *Client, req *JSONRPCRequest) {
	logging.Infof("Stratum: Received mining.subscribe from %s", c.Conn.RemoteAddr())
	c.Mutex.Lock()
	c.SubscriptionID = c.ExtraNonce1
	extraNonce1 := c.ExtraNonce1
	nonce2Size := c.Nonce2Size
	c.Mutex.Unlock()

	subscriptionDetails := []interface{}{"mining.notify", c.SubscriptionID}
	result := []interface{}{subscriptionDetails, extraNonce1, nonce2Size}
	response := JSONRPCResponse{ID: copyRaw(req.ID), Result: result, Error: nil}
	if err := c.send(response); err != nil {
		logging.Warnf("Stratum: Failed to send subscribe response to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	}
}

func (s *StratumServer) handleAuthorize(c *Client, req *JSONRPCRequest) {
	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 1 {
		logging.Warnf("Stratum: Failed to parse authorize params from %s", c.Conn.RemoteAddr())
		return
	}

	c.Mutex.Lock()
	c.WorkerName = params[0]
	c.Authorized = true
	c.Mutex.Unlock()

	logging.Infof("Stratum: Miner %s successfully authorized for worker %s", c.Conn.RemoteAddr(), c.WorkerName)

	response := JSONRPCResponse{ID: copyRaw(req.ID), Result: true, Error: nil}
	if err := c.send(response); err != nil {
		logging.Warnf("Stratum: Failed to send authorize response to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	}

	s.sendDifficulty(c, config.Active.Vardiff.MinDiff)

	s.lastJobMutex.RLock()
	latestJob := s.lastJob
	s.lastJobMutex.RUnlock()

	if latestJob != nil {
		go s.sendMiningJob(c, latestJob.BlockTemplate, true)
	}
}

func (s *StratumServer) sendDifficulty(c *Client, diff float64) {
	if c.closed.Load() {
		return
	}
	c.Mutex.Lock()
	c.CurrentDifficulty = diff
	c.Mutex.Unlock()

	params := []interface{}{diff}
	diffResponse := JSONRPCResponse{Method: "mining.set_difficulty", Params: params}
	if err := c.send(diffResponse); err != nil {
		logging.Warnf("Stratum: Failed to send difficulty to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
		return
	}
	logging.Infof("Stratum: Difficulty set to %.6f for miner %s", diff, c.Conn.RemoteAddr())
}

func (s *StratumServer) vardiffLoop(c *Client) {
	ticker := time.NewTicker(time.Duration(config.Active.Vardiff.RetargetTime) * time.Second)
	defer ticker.Stop()
	for {
		<-ticker.C
		if c.closed.Load() {
			return
		}
		c.Mutex.Lock()
		if !c.Authorized {
			c.Mutex.Unlock()
			return
		}

		datums, actualDuration := c.LocalRateMonitor.GetDatumsInLast(c.LocalRateMonitor.maxLookbackTime)

		if len(datums) < 4 {
			c.Mutex.Unlock()
			continue
		}

		totalTimeSpan := actualDuration.Seconds()
		if totalTimeSpan <= 0 {
			c.Mutex.Unlock()
			continue
		}

		totalWork := 0.0
		for _, datum := range datums {
			totalWork += datum.Work
		}

		avgSharesPerSec := totalWork / totalTimeSpan
		targetSharesPerSec := 1.0 / config.Active.Vardiff.TargetTime
		newDiff := c.CurrentDifficulty * (avgSharesPerSec / targetSharesPerSec)
		if newDiff < config.Active.Vardiff.MinDiff {
			newDiff = config.Active.Vardiff.MinDiff
		}
		roundedDiff := roundDifficultyToPowerOfTwo(newDiff)
		currentDiff := c.CurrentDifficulty
		workerName := c.WorkerName
		c.Mutex.Unlock()

		if math.Abs(roundedDiff-currentDiff) > 0.0001 {
			logging.Infof("Stratum: Retargeting miner %s from diff %f to %f (avg shares/sec: %.2f, total time span: %.2fs)", workerName, currentDiff, roundedDiff, avgSharesPerSec, totalTimeSpan)
			s.sendDifficulty(c, roundedDiff)
		}
	}
}

func targetToAverageAttempts(target *big.Int) float64 {
	if target == nil || target.Sign() <= 0 {
		return 0
	}
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	targetFloat := new(big.Float).SetInt(target)
	if targetFloat.Cmp(big.NewFloat(0)) == 0 {
		return 0
	}
	f, _ := new(big.Float).Quo(maxTargetFloat, targetFloat).Float64()
	return f
}

func roundDifficultyToPowerOfTwo(difficulty float64) float64 {
	dumbScryptDiff := 16.0
	scaledDifficulty := difficulty / dumbScryptDiff
	roundedDifficulty := 1.0

	if scaledDifficulty >= 1.0 {
		for i := 0; i < 32; i++ {
			nextRoundedDifficulty := roundedDifficulty * 2
			if (1.5 * roundedDifficulty) < scaledDifficulty {
				roundedDifficulty = nextRoundedDifficulty
			} else {
				break
			}
		}
	} else {
		for i := 0; i < 32; i++ {
			nextRoundedDifficulty := roundedDifficulty / 2
			if (0.75 * roundedDifficulty) >= scaledDifficulty && nextRoundedDifficulty > 0 {
				roundedDifficulty = nextRoundedDifficulty
			} else {
				break
			}
		}
	}

	return roundedDifficulty * dumbScryptDiff
}

func (s *StratumServer) sendMiningJob(c *Client, tmpl *work.BlockTemplate, cleanJobs bool) {
	if c.closed.Load() {
		return
	}
	if tmpl == nil {
		logging.Warnf("Stratum: No block template available, cannot send job to miner %s", c.Conn.RemoteAddr())
		return
	}

	c.Mutex.Lock()
	jobID := fmt.Sprintf("job%d", rand.Intn(10000))
	newJob := &Job{
		ID:            jobID,
		BlockTemplate: tmpl,
		ExtraNonce1:   c.ExtraNonce1,
	}
	if cleanJobs {
		c.ActiveJobs = make(map[string]*Job)
	}
	c.ActiveJobs[jobID] = newJob
	workerName := c.WorkerName
	c.Mutex.Unlock()

	prevhash := tmpl.PreviousBlockHash
	coinb1 := "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0704"
	coinb2 := "000000000000000000000000000000"
	merkleBranch := []string{}
	blockVersion := "20000000"
	nbits := tmpl.Bits
	ntime := fmt.Sprintf("%x", tmpl.CurTime)
	params := []interface{}{jobID, prevhash, coinb1, coinb2, merkleBranch, blockVersion, nbits, ntime, cleanJobs}

	jobResponse := JSONRPCResponse{Method: "mining.notify", Params: params}

	if err := c.send(jobResponse); err != nil {
		logging.Debugf("Stratum: Failed to send job to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	} else {
		logging.Infof("Stratum: Sent new job %s to worker %s", jobID, workerName)
	}
}
