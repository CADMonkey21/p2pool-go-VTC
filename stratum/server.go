package stratum

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math" // Added for math functions
	"math/big"
	"math/rand" // Kept for rand.Intn
	"net"
	"sync"
	"time"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/p2p"
	"github.com/gertjaap/p2pool-go/work" // Kept for BlockTemplate and CreateShare
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
						sendMiningJob(c, template, true) // Corrected: Call as standalone function
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

	shareTargetBigInt := work.DiffToTarget(c.CurrentDifficulty) // Get *big.Int target from current diff
	powHashInt := new(big.Int)
	powHashInt.SetBytes(work.ReverseBytes(powHash))

	shareAccepted := false
	if powHashInt.Cmp(shareTargetBigInt) <= 0 { // Compare PoW hash (BigInt) with share target (BigInt)
		logging.Infof("Stratum: SHARE ACCEPTED! Valid work from %s", c.WorkerName)
		
		// Add datum to RateMonitor for Vardiff calculation
		c.LocalRateMonitor.AddDatum(ShareDatum{
			Work: targetToAverageAttempts(shareTargetBigInt), // Corrected: Call local targetToAverageAttempts
			IsDead: false, // For now, assume not dead/orphan from stratum submission
			WorkerName: c.WorkerName,
			ShareTarget: shareTargetBigInt, // Store *big.Int target
			// PubKeyHash: Needs to be derived from PoolAddress or miner's provided address for LocalAddrRateMonitor
		})
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
	subscriptionDetails := []interface{}{"mining.notify", c.SubscriptionID}
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
	go sendMiningJob(c, s.workManager.GetLatestTemplate(), true) // Corrected: Call as standalone function
}

// targetToAverageAttempts calculates the average attempts per second for a given target.
// This is a direct copy from work/util.go to resolve compilation issues.
func targetToAverageAttempts(target *big.Int) float64 {
	// 2**256
	two_256 := new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)
	// target + 1
	targetPlusOne := new(big.Int).Add(target, big.NewInt(1))
	
	// Convert to big.Float for precise division
	// Python: 2**256 // (target + 1)
	quotient := new(big.Float).Quo(new(big.Float).SetInt(two_256), new(big.Float).SetInt(targetPlusOne))
	
	f, _ := quotient.Float64() // Convert to float64, _ discards exactness bool
	return f
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
	logging.Infof("Stratum: Difficulty set to %.6f for miner %s", diff, c.Conn.RemoteAddr()) // Corrected: Formatted diff for logging
    logging.Debugf("sendDifficulty received diff: %.10f", diff) // Added for high precision logging
}

// roundDifficultyToPowerOfTwo mirrors Python's power-of-2 rounding for difficulty.
// It finds the closest difficulty that is a power of 2 (or fraction thereof)
// and is less than or equal to the calculated difficulty.
func roundDifficultyToPowerOfTwo(difficulty float64) float64 { // Removed dumbScryptDiff param
	dumbScryptDiff := 16.0 // Corrected: Hardcode DumbScryptDiff to resolve undefined error
	logging.Debugf("Vardiff Rounding: Input difficulty = %f, DumbScryptDiff = %f", difficulty, dumbScryptDiff)

	// Adjust for DUMB_SCRYPT_DIFF as per Python's bitcoin_data.difficulty_to_target_alt
	scaledDifficulty := difficulty / dumbScryptDiff
	logging.Debugf("Vardiff Rounding: Scaled difficulty = %f", scaledDifficulty)

	roundedDifficulty := 1.0 // Start with 1.0 as the base power of 2
	
	if scaledDifficulty >= 1.0 {
		for i := 0; i < 32; i++ { // Limit iterations to prevent infinite loops and for sanity
			nextRoundedDifficulty := roundedDifficulty * 2
			// The Python condition is (rounded_difficulty + rounded_difficulty * 2) / 2 < difficulty
			// This means (1.5 * rounded_difficulty) < scaledDifficulty
			if (1.5 * roundedDifficulty) < scaledDifficulty { // Go for the next power of 2
				roundedDifficulty = nextRoundedDifficulty
				logging.Debugf("Vardiff Rounding: Increasing roundedDifficulty to %f", roundedDifficulty)
			} else {
				break
			}
		}
	} else { // scaledDifficulty < 1.0, round downwards
		for i := 0; i < 32; i++ { // Limit iterations
			nextRoundedDifficulty := roundedDifficulty / 2
			// Python: (0.75 * rounded_difficulty) >= scaledDifficulty
			// This means (0.75 * rounded_difficulty) >= scaledDifficulty
			// Add a lower bound to prevent going to exactly zero
			if (0.75 * roundedDifficulty) >= scaledDifficulty && nextRoundedDifficulty > 0 { // Ensure it's not zero or negative
				roundedDifficulty = nextRoundedDifficulty
				logging.Debugf("Vardiff Rounding: Decreasing roundedDifficulty to %f", roundedDifficulty)
			} else {
				break
			}
		}
	}
	
	finalRoundedDifficulty := roundedDifficulty * dumbScryptDiff // Scale back up by DUMB_SCRYPT_DIFF
	logging.Debugf("Vardiff Rounding: Final scaled roundedDifficulty = %f", finalRoundedDifficulty)
	return finalRoundedDifficulty
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
		
		// Use localRateMonitor to get current hashrate and avg share time
		// Python's get_local_rates uses 10*60 seconds (10 minutes) as default lookback
		// The vardiff loop itself uses the full history kept by RateMonitor
		// (len(c.ShareTimestamps) is just a basic check)
		
		// For accurate avgTime calculation, we use the data from RateMonitor
		datums, actualDuration := c.LocalRateMonitor.GetDatumsInLast(c.LocalRateMonitor.maxLookbackTime)

		if len(datums) < 4 { // Needs at least 4 shares in the window for a stable calculation
			c.Mutex.Unlock()
			continue
		}

		totalTimeSpan := actualDuration.Seconds()
		logging.Debugf("Vardiff Loop for miner %s: actualDuration (seconds) = %.2f", c.WorkerName, totalTimeSpan)

		if totalTimeSpan <= 0 { 
			logging.Warnf("Stratum: Time window for vardiff is zero or too small for miner %s. Possibly very high hashrate or clock issue. Skipping retargeting this cycle.", c.WorkerName)
			c.Mutex.Unlock()
			continue
		}

		totalWork := 0.0
		for _, datum := range datums {
			totalWork += datum.Work
		}
		logging.Debugf("Vardiff Loop for miner %s: Total Work = %.2f", c.WorkerName, totalWork)
		
		avgSharesPerSec := totalWork / totalTimeSpan
		logging.Debugf("Vardiff Loop for miner %s: Avg Shares Per Sec = %.4f", c.WorkerName, avgSharesPerSec)

		targetSharesPerSec := 1.0 / config.Active.Vardiff.TargetTime
		logging.Debugf("Vardiff Loop for miner %s: Target Shares Per Sec = %.4f", c.WorkerName, targetSharesPerSec)

		newDiff := c.CurrentDifficulty * (avgSharesPerSec / targetSharesPerSec)
		logging.Debugf("Vardiff Loop for miner %s: Calculated newDiff (before rounding) = %f", c.WorkerName, newDiff)

		if newDiff < config.Active.Vardiff.MinDiff {
			newDiff = config.Active.Vardiff.MinDiff
			logging.Debugf("Vardiff Loop for miner %s: newDiff clamped to minDiff = %f", c.WorkerName, newDiff)
		}
		
		// Apply the power-of-2 rounding mirroring legacy Python
		roundedDiff := roundDifficultyToPowerOfTwo(newDiff) // Corrected: Removed DumbScryptDiff param
		logging.Debugf("Vardiff Loop for miner %s: Rounded newDiff = %f", c.WorkerName, roundedDiff)


		currentDiff := c.CurrentDifficulty
		// Check if newDiff is significantly different from currentDiff based on rounded value
		if math.Abs(roundedDiff-currentDiff) > 0.0001 { 
			c.Mutex.Unlock() // Unlock before calling sendDifficulty
			logging.Infof("Stratum: Retargeting miner %s from diff %f to %f (avg shares/sec: %.2f, total time span: %.2fs)", c.WorkerName, currentDiff, roundedDiff, avgSharesPerSec, totalTimeSpan)
			s.sendDifficulty(c, roundedDiff)
			c.Mutex.Lock() // Re-lock after sendDifficulty
		}
		
		c.Mutex.Unlock()
	}
}

// sendMiningJob is moved out of StratumServer struct to be a package-level function.
// This matches the pattern in the provided Go codebase and resolves compilation error.
func sendMiningJob(c *Client, tmpl *work.BlockTemplate, cleanJobs bool) { // Made standalone function
	if tmpl == nil {
		logging.Warnf("Stratum: No block template available, cannot send job to miner %s", c.Conn.RemoteAddr())
		return
	}
	jobID := fmt.Sprintf("job%d", rand.Intn(10000)) // Uses math/rand

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
