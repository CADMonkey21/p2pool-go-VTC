package stratum

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/CADMonkey21/p2pool-go-vtc/config"
	"github.com/CADMonkey21/p2pool-go-vtc/logging"
	p2pnet "github.com/CADMonkey21/p2pool-go-vtc/net"
	"github.com/CADMonkey21/p2pool-go-vtc/p2p"
	"github.com/CADMonkey21/p2pool-go-vtc/work"
	"github.com/CADMonkey21/p2pool-go-vtc/wire"
)

// Each unit of VertHash difficulty represents 2^24 hashes.
const hashrateConstant = 16777216 // 2^24

/* -------------------------------------------------------------------- */
/* Helpers                                                              */
/* -------------------------------------------------------------------- */

func isValidVtcAddress(addr string) bool {
	hrp, _, err := bech32.Decode(addr)
	if err != nil {
		return false
	}
	return hrp == "vtc" || hrp == "tvtc"
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

// TargetToDiff converts a target big.Int to a difficulty float64.
func TargetToDiff(target *big.Int) float64 {
	// The formula is: difficulty = 1 / (target / 2^256)
	// Simplified: difficulty = 2^256 / target
	maxTarget := new(big.Int).Lsh(big.NewInt(1), 256)
	diff := new(big.Float).SetInt(maxTarget)
	targetFloat := new(big.Float).SetInt(target)
	resultFloat := new(big.Float).Quo(diff, targetFloat)
	f64, _ := resultFloat.Float64()
	return f64
}

/* -------------------------------------------------------------------- */
/* StratumServer struct                                                 */
/* -------------------------------------------------------------------- */

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

/* -------------------------------------------------------------------- */
/* Public server entry point                                            */
/* -------------------------------------------------------------------- */

func (s *StratumServer) Serve(listener net.Listener) error {
	logging.Infof("Stratum: Listening for miners on %s", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				logging.Warnf("Stratum: temporary accept error: %v", err)
				continue
			}
			return err
		}
		go s.handleMinerConnection(conn)
	}
}

/* -------------------------------------------------------------------- */
/* Share‑rate helpers                                                   */
/* -------------------------------------------------------------------- */

func (s *StratumServer) GetLocalSharesPerSecond() float64 {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	total := 0.0
	for _, c := range s.clients {
		c.Mutex.Lock()
		datums, span := c.LocalRateMonitor.GetDatumsInLast(5 * time.Minute)
		if len(datums) < 5 || span < 30*time.Second {
			c.Mutex.Unlock()
			continue
		}
		if len(datums) > 0 && span.Seconds() > 1 {
			total += float64(len(datums)) / span.Seconds()
		}
		c.Mutex.Unlock()
	}
	return total
}

func (s *StratumServer) GetLocalHashrate() float64 {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	total := 0.0
	for _, c := range s.clients {
		total += s.GetHashrateForClient(c.ID)
	}
	return total
}

/* -------------------------------------------------------------------- */
/* Client management                                                    */
/* -------------------------------------------------------------------- */

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

/* -------------------------------------------------------------------- */
/* Keep‑alive & job broadcast loops                                     */
/* -------------------------------------------------------------------- */

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
		clients := make([]*Client, 0, len(s.clients))
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		s.clientsMutex.RUnlock()
		for _, c := range clients {
			if c.Authorized && s.isClientActive(c.ID) {
				s.sendMiningJob(c, job.BlockTemplate, false)
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
		clients := make([]*Client, 0, len(s.clients))
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		s.clientsMutex.RUnlock()

		for _, c := range clients {
			if c.Authorized && s.isClientActive(c.ID) {
				s.sendMiningJob(c, template, true)
			}
		}
	}
}

/* -------------------------------------------------------------------- */
/* Connection handler                                                   */
/* -------------------------------------------------------------------- */

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
		if err := decoder.Decode(&req); err != nil {
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
			logging.Warnf("Stratum: Unhandled method '%s' from %s", req.Method, conn.RemoteAddr())
		}
	}
}

/* -------------------------------------------------------------------- */
/* Submit / subscribe / authorize handlers                              */
/* -------------------------------------------------------------------- */

func (s *StratumServer) handleSubmit(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)

	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 5 {
		logging.Warnf("Stratum: Malformed submit params from %s", c.Conn.RemoteAddr())
		return
	}

	workerName, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]
	logging.Debugf("Stratum: Received mining.submit from %s for job %s", workerName, jobID)

	c.Mutex.Lock()
	job, jobExists := c.ActiveJobs[jobID]
	payoutAddr := c.WorkerName
	currentDiff := c.CurrentDifficulty
	c.Mutex.Unlock()

	if !jobExists {
		// This is normal after a rapid template refresh. The miner is submitting a share
		// for a job that has already been cleared. Silently drop it.
		logging.Debugf("Stratum: Dropping stale share for unknown jobID %s", jobID)
		return
	}

	headerBytes, _, err := work.CreateHeader(job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr)
	if err != nil {
		logging.Errorf("Stratum: Failed to create header: %v", err)
		return
	}

	powHash := p2pnet.ActiveNetwork.POWHash(headerBytes)
	shareTarget := work.DiffToTarget(currentDiff)

	powInt := new(big.Int).SetBytes(work.ReverseBytes(powHash))
	accepted := powInt.Cmp(shareTarget) <= 0

	if accepted {
		newShare, err := work.CreateShare(job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr, s.workManager.ShareChain, currentDiff)
		if err != nil {
			logging.Errorf("Stratum: Could not create share object: %v", err)
			return // Return early as we can't process this share
		}

		shareDiff := TargetToDiff(powInt)
		logging.Successf("SHARE ACCEPTED from %s (Height: %d, Diff: %.2f, Hash: %s)",
			c.WorkerName,
			job.BlockTemplate.Height,
			shareDiff,
			newShare.Hash.String()[:12],
		)

		c.Mutex.Lock()
		c.AcceptedShares++
		c.Mutex.Unlock()

		c.LocalRateMonitor.AddDatum(ShareDatum{
			Work:        currentDiff,
			IsDead:      false,
			WorkerName:  c.WorkerName,
			ShareTarget: shareTarget,
		})

		s.workManager.ShareChain.AddShares([]wire.Share{*newShare}, true)
		s.peerManager.Broadcast(&wire.MsgShares{Shares: []wire.Share{*newShare}})

		// CORRECTED: Use the network target from the job's original block template.
		bits, err := hex.DecodeString(job.BlockTemplate.Bits)
		if err == nil {
			nBits := binary.LittleEndian.Uint32(bits)
			netTarget := blockchain.CompactToBig(nBits)

			if powInt.Cmp(netTarget) <= 0 {
				logging.Successf("!!!! BLOCK FOUND !!!! Share %s is a valid block!", newShare.Hash.String()[:12])
				go s.workManager.SubmitBlock(newShare, job.BlockTemplate)
			}
		}
	} else {
		logging.Warnf("Stratum: Share rejected – hash above target")
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
	}

	resp := JSONRPCResponse{ID: id, Result: accepted, Error: nil}
	if err := c.send(resp); err != nil {
		logging.Warnf("Stratum: failed to send submit response: %v", err)
		s.dropClient(c)
	}
}

func (s *StratumServer) handleSubscribe(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	logging.Infof("Stratum: Received mining.subscribe from %s", c.Conn.RemoteAddr())

	c.Mutex.Lock()
	c.SubscriptionID = c.ExtraNonce1
	subDetails := []interface{}{"mining.notify", c.SubscriptionID}
	result := []interface{}{subDetails, c.ExtraNonce1, c.Nonce2Size}
	c.Mutex.Unlock()

	resp := JSONRPCResponse{ID: id, Result: result, Error: nil}
	if err := c.send(resp); err != nil {
		logging.Warnf("Stratum: Failed to send subscribe response: %v", err)
		s.dropClient(c)
	}
}

func (s *StratumServer) handleAuthorize(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)

	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 1 {
		logging.Warnf("Stratum: Bad authorize params from %s", c.Conn.RemoteAddr())
		return
	}

	addr := params[0]
	if !isValidVtcAddress(addr) {
		resp := JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Invalid VTC address", nil}}
		_ = c.send(resp)
		s.dropClient(c)
		return
	}

	c.Mutex.Lock()
	c.WorkerName = addr
	c.Authorized = true
	c.Mutex.Unlock()

	resp := JSONRPCResponse{ID: id, Result: true, Error: nil}
	if err := c.send(resp); err != nil {
		s.dropClient(c)
		return
	}

	s.sendDifficulty(c, config.Active.Vardiff.MinDiff)

	s.lastJobMutex.RLock()
	job := s.lastJob
	s.lastJobMutex.RUnlock()
	if job != nil {
		go s.sendMiningJob(c, job.BlockTemplate, true)
	}
}

/* -------------------------------------------------------------------- */
/* Difficulty & vardiff                                                 */
/* -------------------------------------------------------------------- */

func (s *StratumServer) sendDifficulty(c *Client, diff float64) {
	if c.closed.Load() {
		return
	}
	c.Mutex.Lock()
	c.CurrentDifficulty = diff
	c.Mutex.Unlock()

	params := []interface{}{diff}
	msg := JSONRPCResponse{Method: "mining.set_difficulty", Params: params}
	if err := c.send(msg); err != nil {
		logging.Warnf("Stratum: Failed to send diff: %v", err)
		s.dropClient(c)
	}
	logging.Infof("Stratum: Difficulty set to %.6f for %s", diff, c.Conn.RemoteAddr())
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
			continue
		}
		datums, span := c.LocalRateMonitor.GetDatumsInLast(c.LocalRateMonitor.maxLookbackTime)
		if len(datums) < 5 || span.Seconds() <= 0 {
			c.Mutex.Unlock()
			continue
		}

		avgTime := span.Seconds() / float64(len(datums))
		sumDiff := 0.0
		for _, d := range datums {
			sumDiff += d.Work
		}
		avgDiff := sumDiff / float64(len(datums))
		newDiff := avgDiff * (config.Active.Vardiff.TargetTime / avgTime)
		if newDiff < config.Active.Vardiff.MinDiff {
			newDiff = config.Active.Vardiff.MinDiff
		}
		curDiff := c.CurrentDifficulty
		worker := c.WorkerName
		c.Mutex.Unlock()

		if newDiff/curDiff > 1.0+config.Active.Vardiff.Variance ||
			newDiff/curDiff < 1.0-config.Active.Vardiff.Variance {
			logging.Infof("Stratum: Retargeting %s from %.4f to %.4f (avg share %.2fs)",
				worker, curDiff, newDiff, avgTime)
			s.sendDifficulty(c, newDiff)
		}
	}
}

/* -------------------------------------------------------------------- */
/* Job sender                                                           */
/* -------------------------------------------------------------------- */

func (s *StratumServer) sendMiningJob(c *Client, tmpl *work.BlockTemplate, clean bool) {
	if c.closed.Load() || tmpl == nil {
		return
	}

	c.Mutex.Lock()
	jobID := fmt.Sprintf("job%d", rand.Intn(10000))
	job := &Job{
		ID:              jobID,
		BlockTemplate:   tmpl,
		ExtraNonce1:     c.ExtraNonce1,
		Difficulty:      c.CurrentDifficulty,
	}
	if clean {
		c.ActiveJobs = make(map[string]*Job)
	}
	c.ActiveJobs[jobID] = job
	worker := c.WorkerName
	c.Mutex.Unlock()

	params := []interface{}{
		jobID,
		tmpl.PreviousBlockHash,
		"01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0704",
		"000000000000000000000000000000",
		[]string{},
		"20000000",
		tmpl.Bits,
		fmt.Sprintf("%x", tmpl.CurTime),
		clean,
	}
	msg := JSONRPCResponse{Method: "mining.notify", Params: params}
	if err := c.send(msg); err != nil {
		logging.Debugf("Stratum: Failed to send job to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	} else {
		txCount := len(tmpl.Transactions)
		txSize := 0
		for _, tx := range tmpl.Transactions {
			txSize += len(tx.Data)
		}
		blockValue := float64(tmpl.CoinbaseValue) / 100000000.0 // Assuming value is in satoshis

		logging.Noticef("New job %s sent to %s (Height: %d, Block Value: %.2f VTC, Share Diff: %.2f, %d tx, %.1f kB)",
			jobID,
			worker,
			tmpl.Height,
			blockValue,
			job.Difficulty,
			txCount,
			float64(txSize)/1024.0,
		)
	}
}

func (s *StratumServer) GetClients() []*Client {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()
	clients := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	return clients
}

func (s *StratumServer) GetHashrateForClient(id uint64) float64 {
	s.clientsMutex.RLock()
	client, ok := s.clients[id]
	s.clientsMutex.RUnlock()

	if !ok {
		return 0.0
	}

	client.Mutex.Lock()
	defer client.Mutex.Unlock()

	datums, span := client.LocalRateMonitor.GetDatumsInLast(10 * time.Minute)

	if len(datums) < 5 || span < 30*time.Second {
		return 0.0
	}

	workTotal := 0.0
	for _, d := range datums {
		workTotal += d.Work
	}

	return (workTotal * hashrateConstant) / span.Seconds()
}
