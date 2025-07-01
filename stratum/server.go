package stratum

import (
	"encoding/binary"
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

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/work"
	"github.com/gertjaap/p2pool-go/wire"
)

func isValidVtcAddress(addr string) bool {
	hrp, _, err := bech32.Decode(addr)
	if err != nil {
		return false
	}
	if hrp != "vtc" && hrp != "tvtc" {
		return false
	}
	return true
}

func extractID(raw *json.RawMessage) interface{} {
	if raw == nil {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(*raw, &v); err != nil {
		return nil
	}
	return v
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

func (s *StratumServer) GetLocalSharesPerSecond() float64 {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	totalSharesPerSec := 0.0
	for _, client := range s.clients {
		client.Mutex.Lock()
		datums, actualDuration := client.LocalRateMonitor.GetDatumsInLast(5 * time.Minute)
		if len(datums) > 0 && actualDuration.Seconds() > 1 {
			totalSharesPerSec += float64(len(datums)) / actualDuration.Seconds()
		}
		client.Mutex.Unlock()
	}
	return totalSharesPerSec
}

func (s *StratumServer) GetLocalHashrate() float64 {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()
	totalHashrate := 0.0
	for _, client := range s.clients {
		client.Mutex.Lock()
		datums, actualDuration := client.LocalRateMonitor.GetDatumsInLast(10 * time.Minute)
		if len(datums) < 1 || actualDuration.Seconds() < 1 {
			client.Mutex.Unlock()
			continue
		}
		totalWork := 0.0
		for _, datum := range datums {
			totalWork += datum.Work
		}
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
	id := extractID(req.ID)
	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 5 {
		logging.Warnf("Stratum: Malformed submit params from %s", c.Conn.RemoteAddr())
		return
	}
	workerName, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]
	logging.Infof("Stratum: Received mining.submit from %s for job %s", workerName, jobID)
	c.Mutex.Lock()
	job, jobExists := c.ActiveJobs[jobID]
	payoutAddr := c.WorkerName
	currentDiff := c.CurrentDifficulty
	c.Mutex.Unlock()

	if !jobExists {
		logging.Warnf("Stratum: Received submission for unknown jobID %s", jobID)
		return
	}

	headerBytes, _, err := work.CreateHeader(job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr)
	if err != nil {
		logging.Errorf("Stratum: Failed to create block header for validation: %v", err)
		return
	}
	powHash := p2pnet.ActiveNetwork.POWHash(headerBytes)
	logging.Debugf("PoW hash for submission: %s", hex.EncodeToString(work.ReverseBytes(powHash)))
	shareTargetBigInt := work.DiffToTarget(currentDiff)
	powHashInt := new(big.Int)
	powHashInt.SetBytes(work.ReverseBytes(powHash))
	shareAccepted := false
	if powHashInt.Cmp(shareTargetBigInt) <= 0 {
		logging.Infof("Stratum: SHARE ACCEPTED! Valid work from %s", c.WorkerName)
		shareAccepted = true
		c.LocalRateMonitor.AddDatum(ShareDatum{
			Work:        currentDiff,
			IsDead:      false,
			WorkerName:  c.WorkerName,
			ShareTarget: shareTargetBigInt,
		})

		newShare, err := work.CreateShare(job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr, s.workManager.ShareChain, currentDiff)
		if err != nil {
			logging.Errorf("Stratum: Could not create share object: %v", err)
		} else {
			s.workManager.ShareChain.AddShares([]wire.Share{*newShare}, true)
			s.peerManager.Broadcast(&wire.MsgShares{Shares: []wire.Share{*newShare}})
			bits, err := hex.DecodeString(job.BlockTemplate.Bits)
			if err != nil {
				logging.Errorf("Could not decode bits from template: %v", err)
			} else {
				nBits := binary.LittleEndian.Uint32(bits)
				netTarget := blockchain.CompactToBig(nBits)
				if powHashInt.Cmp(netTarget) <= 0 {
					logging.Infof("!!!! BLOCK FOUND !!!! Share %s is a valid block!", newShare.Hash.String()[:12])
					go s.workManager.SubmitBlock(newShare, job.BlockTemplate)
				}
			}
		}
	} else {
		logging.Warnf("Stratum: Share rejected. Hash does not meet target.")
	}
	response := JSONRPCResponse{ID: id, Result: shareAccepted, Error: nil}
	if err := c.send(response); err != nil {
		logging.Warnf("Stratum: failed to send submit response to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	}
}

func (s *StratumServer) handleSubscribe(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	logging.Infof("Stratum: Received mining.subscribe from %s", c.Conn.RemoteAddr())
	c.Mutex.Lock()
	c.SubscriptionID = c.ExtraNonce1
	extraNonce1 := c.ExtraNonce1
	nonce2Size := c.Nonce2Size
	c.Mutex.Unlock()
	subscriptionDetails := []interface{}{"mining.notify", c.SubscriptionID}
	result := []interface{}{subscriptionDetails, extraNonce1, nonce2Size}
	response := JSONRPCResponse{ID: id, Result: result, Error: nil}
	if err := c.send(response); err != nil {
		logging.Warnf("Stratum: Failed to send subscribe response to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	}
}

func (s *StratumServer) handleAuthorize(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 1 {
		logging.Warnf("Stratum: Failed to parse authorize params from %s", c.Conn.RemoteAddr())
		return
	}

	workerAddr := params[0]
	if !isValidVtcAddress(workerAddr) {
		logging.Warnf("Stratum: Miner %s attempted to authorize with invalid address: %s", c.Conn.RemoteAddr(), workerAddr)
		response := JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Invalid VTC address", nil}}
		if err := c.send(response); err != nil {
			logging.Warnf("Stratum: Failed to send auth error response to %s: %v", c.Conn.RemoteAddr(), err)
		}
		s.dropClient(c)
		return
	}

	c.Mutex.Lock()
	c.WorkerName = workerAddr
	c.Authorized = true
	c.Mutex.Unlock()
	logging.Infof("Stratum: Miner %s successfully authorized for worker %s", c.Conn.RemoteAddr(), c.WorkerName)
	response := JSONRPCResponse{ID: id, Result: true, Error: nil}
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

// MODIFIED: This loop now uses a standard, time-based vardiff algorithm.
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
			continue
		}

		// Use the whole lookback window for a stable average
		datums, totalTimeSpan := c.LocalRateMonitor.GetDatumsInLast(c.LocalRateMonitor.maxLookbackTime)

		// Need a minimum number of shares to get a reliable average
		if len(datums) < 5 {
			c.Mutex.Unlock()
			continue
		}

		actualTimePerShare := totalTimeSpan.Seconds() / float64(len(datums))
		if actualTimePerShare <= 0 {
			c.Mutex.Unlock()
			continue
		}

		var summedDifficulty float64
		for _, d := range datums {
			summedDifficulty += d.Work
		}
		avgDifficulty := summedDifficulty / float64(len(datums))

		// Proportional adjustment: new_diff = current_diff * (target_time / actual_time)
		targetTime := config.Active.Vardiff.TargetTime
		newDiff := avgDifficulty * (targetTime / actualTimePerShare)

		if newDiff < config.Active.Vardiff.MinDiff {
			newDiff = config.Active.Vardiff.MinDiff
		}

		currentDiff := c.CurrentDifficulty
		workerName := c.WorkerName
		c.Mutex.Unlock()

		// MODIFIED: Use the config variance to decide whether to send an update.
		// This is the standard p2pool method and allows for smoother adjustments.
		if (newDiff / currentDiff) > (1.0 + config.Active.Vardiff.Variance) || (newDiff / currentDiff) < (1.0 - config.Active.Vardiff.Variance) {
			logging.Infof("Stratum: Retargeting miner %s from diff %.4f to %.4f (avg share time: %.2fs, target: %.2fs)", workerName, currentDiff, newDiff, actualTimePerShare, targetTime)
			s.sendDifficulty(c, newDiff)
		}
	}
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
