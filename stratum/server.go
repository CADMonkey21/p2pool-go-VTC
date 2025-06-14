package stratum

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

type StratumServer struct {
	workManager  *work.WorkManager
	peerManager  *p2p.PeerManager
	clients      map[uint64]*Client
	clientsMutex sync.RWMutex
}

func NewStratumServer(wm *work.WorkManager, pm *p2p.PeerManager) *StratumServer {
	s := &StratumServer{
		workManager: wm,
		peerManager: pm,
		clients:     make(map[uint64]*Client),
	}
	go s.jobBroadcaster()
	return s
}

func (s *StratumServer) jobBroadcaster() {
	for {
		template := <-s.workManager.NewBlockChan
		
		s.clientsMutex.RLock()
		if len(s.clients) > 0 {
			logging.Infof("Stratum: Broadcasting new job for height %d to %d miners", template.Height, len(s.clients))
			for _, client := range s.clients {
				go func(c *Client) {
					if c.Authorized {
						s.sendMiningJob(c, template, true)
					}
				}(client)
			}
		}
		s.clientsMutex.RUnlock()
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
	defer func() {
		s.clientsMutex.Lock()
		delete(s.clients, client.ID)
		s.clientsMutex.Unlock()
		client.Conn.Close()
		logging.Infof("Stratum: Miner %s disconnected.", conn.RemoteAddr())
	}()

	go s.vardiffLoop(client)
	decoder := json.NewDecoder(client.Reader)
	for {
		client.Conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
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
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	var params []string
	err := json.Unmarshal(*req.Params, &params)
	if err != nil || len(params) < 5 {
		logging.Warnf("Stratum: Malformed submit params from %s", c.Conn.RemoteAddr())
		return
	}

	workerName, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]
	logging.Infof("Stratum: Received mining.submit from %s for job %s", workerName, jobID)
	
	job, jobExists := c.ActiveJobs[jobID]
	if !jobExists {
		logging.Warnf("Stratum: Received submission for unknown jobID %s", jobID)
		return
	}

	header, _, err := work.CreateHeader(job.BlockTemplate, c.ExtraNonce1, extraNonce2, nTime, nonceHex, config.Active.PoolAddress)
	if err != nil {
		logging.Errorf("Stratum: Failed to create block header for validation: %v", err)
		return
	}

	powHash := p2pnet.ActiveNetwork.POWHash(header)
	logging.Debugf("PoW hash for submission: %s", hex.EncodeToString(work.ReverseBytes(powHash)))

	shareTarget := work.DiffToTarget(c.CurrentDifficulty)
	powHashInt := new(big.Int)
	powHashInt.SetBytes(work.ReverseBytes(powHash))

	shareAccepted := false
	if powHashInt.Cmp(shareTarget) <= 0 {
		logging.Infof("Stratum: SHARE ACCEPTED! Valid work from %s", c.WorkerName)
		c.ShareTimestamps = append(c.ShareTimestamps, time.Now().Unix())
		shareAccepted = true
		
		newShare, err := work.CreateShare(job.BlockTemplate, c.ExtraNonce1, extraNonce2, nTime, nonceHex, config.Active.PoolAddress, s.workManager.ShareChain)
		if err != nil {
			logging.Errorf("Stratum: Could not create share object: %v", err)
		} else {
			s.workManager.ShareChain.AddShares([]wire.Share{*newShare})
			s.peerManager.Broadcast(&wire.MsgShares{Shares: []wire.Share{*newShare}})
		}
	} else {
		logging.Warnf("Stratum: Share rejected. Hash does not meet target.")
	}
	
	response := JSONRPCResponse{ID: req.ID, Result: shareAccepted}
	err = c.Encoder.Encode(response)
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
	result := []interface{}{subscriptionDetails, c.ExtraNonce1, c.Nonce2Size}
	response := JSONRPCResponse{ID: req.ID, Result: result}
	err := c.Encoder.Encode(response)
	if err != nil {
		logging.Warnf("Stratum: Failed to send subscribe response to %s: %v", c.Conn.RemoteAddr(), err)
	}
}

func (s *StratumServer) handleAuthorize(c *Client, req *JSONRPCRequest) {
	c.Mutex.Lock()
	
	logging.Infof("Stratum: Received mining.authorize from %s", c.Conn.RemoteAddr())
	var params []string
	err := json.Unmarshal(*req.Params, &params)
	if err != nil || len(params) < 1 {
		c.Mutex.Unlock()
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
	
	c.Mutex.Unlock() 
	
	s.sendDifficulty(c, config.Active.Vardiff.MinDiff)
	go s.sendMiningJob(c, s.workManager.GetLatestTemplate(), true)
}

func (s *StratumServer) sendDifficulty(c *Client, diff float64) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	c.CurrentDifficulty = diff
	params := []interface{}{c.CurrentDifficulty}
	diffResponse := JSONRPCResponse{Method: "mining.set_difficulty", Params: params}
	err := c.Encoder.Encode(diffResponse)
	if err != nil {
		logging.Warnf("Stratum: Failed to send difficulty to %s: %v", c.Conn.RemoteAddr(), err)
	}
	logging.Infof("Stratum: Difficulty set to %f for miner %s", diff, c.Conn.RemoteAddr())
}

func (s *StratumServer) sendMiningJob(c *Client, tmpl *work.BlockTemplate, cleanJobs bool) {
	if tmpl == nil {
		logging.Warnf("Stratum: No block template available, cannot send job to miner %s", c.Conn.RemoteAddr())
		return
	}
	jobID := fmt.Sprintf("job%d", rand.Intn(10000))

	c.Mutex.Lock()
	newJob := &Job{
		ID:            jobID,
		BlockTemplate: tmpl,
		ExtraNonce1:   c.ExtraNonce1,
	}
	if cleanJobs {
		c.ActiveJobs = make(map[string]*Job)
	}
	c.ActiveJobs[jobID] = newJob
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
	
	c.Mutex.Lock()
	err := c.Encoder.Encode(jobResponse)
	c.Mutex.Unlock()
	
	if err != nil {
		logging.Warnf("Stratum: Failed to send job to %s: %v", c.Conn.RemoteAddr(), err)
	}
	logging.Infof("Stratum: Sent new job %s to worker %s", jobID, c.WorkerName)
}

func (s *StratumServer) vardiffLoop(c *Client) {
	ticker := time.NewTicker(time.Duration(config.Active.Vardiff.RetargetTime) * time.Second)
	defer ticker.Stop()
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
			currentDiff := c.CurrentDifficulty
			if (newDiff-currentDiff) > 0.0001 || (currentDiff-newDiff) > 0.0001 {
				c.Mutex.Unlock()
				logging.Infof("Stratum: Retargeting miner %s from diff %f to %f (avg share time: %.2fs)", c.Conn.RemoteAddr(), currentDiff, newDiff, avgTime)
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

