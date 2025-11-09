package stratum

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
	p2pwire "github.com/CADMonkey21/p2pool-go-VTC/wire"
)

// Each unit of VertHash difficulty represents 2^24 hashes.
const hashrateConstant = 16777216 // 2^24

// jobCounter is a thread-safe counter for generating unique job IDs.
var jobCounter uint64

/* -------------------------------------------------------------------- */
/* Helpers                                                              */
/* -------------------------------------------------------------------- */

// doubleSHA256 computes sha256(sha256(data))
func doubleSHA256(data []byte) []byte {
	h := sha256.Sum256(data)
	h2 := sha256.Sum256(h[:])
	return h2[:]
}

// HashLEToBig converts a 32-byte little-endian hash to a big.Int (big-endian) value.
func HashLEToBig(h []byte) *big.Int {
	if len(h) != 32 {
		return big.NewInt(0)
	}
	be := make([]byte, 32)
	for i := 0; i < 32; i++ {
		be[i] = h[31-i]
	}
	return new(big.Int).SetBytes(be)
}

// isValidVtcAddress now validates both Bech32 and legacy Base58 addresses with checksums.
func isValidVtcAddress(addr string) bool {
	// --- Try bech32 first ---
	if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
		hrp, _, err := bech32.Decode(addr)
		// Use the correct HRP for the active network
		expectedHrp := "vtc"
		if config.Active.Testnet {
			expectedHrp = "tvtc"
		}

		if err == nil {
			if hrp == expectedHrp {
				return true
			}
		}
		return false
	}

	// --- Try legacy Base58 (with checksum) ---
	decoded := base58.Decode(addr)
	if len(decoded) < 5 {
		return false
	}

	// split into payload + checksum
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	expected := doubleSHA256(payload)[:4]
	for i := 0; i < 4; i++ {
		if checksum[i] != expected[i] {
			return false
		}
	}

	// version byte
	version := payload[0]
	if config.Active.Testnet {
		switch version {
		case 0x6f, 0xc4: // testnet P2PKH, P2SH
			return true
		default:
			return false
		}
	} else {
		switch version {
		case 0x47, 0x05: // mainnet P2PKH, P2SH
			return true
		default:
			return false
		}
	}
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
	// [FIX] Use the correct max target (2^256 - 1)
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
	if target == nil || target.Sign() <= 0 {
		return 0
	}

	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	targetFloat := new(big.Float).SetInt(target)
	resultFloat := new(big.Float).Quo(maxTargetFloat, targetFloat)
	f64, _ := resultFloat.Float64()
	return f64
}

// [NEW] buildMasterNotifyParams creates the 7 job parameters that are
// common to all miners for a given template.
func buildMasterNotifyParams(tmpl *work.BlockTemplate) []interface{} {
	return []interface{}{
		tmpl.PreviousBlockHash,
		"01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0704",
		"000000000000000000000000000000",
		[]string{},
		"20000000",
		tmpl.Bits,
		fmt.Sprintf("%x", tmpl.CurTime),
	}
}

/* -------------------------------------------------------------------- */
/* StratumServer struct                                                 */
/* -------------------------------------------------------------------- */

type StratumServer struct {
	workManager         *work.WorkManager
	peerManager         *p2p.PeerManager
	clients             map[uint64]*Client
	clientsMutex        sync.RWMutex
	lastJob             *Job
	latestPrevBlockHash *chainhash.Hash // [FIX] Added to track the latest block hash
	lastJobMutex        sync.RWMutex
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
/* Share-rate helpers                                                   */
/* -------------------------------------------------------------------- */

// [NEW] GetLocalEfficiency calculates the total efficiency of all connected miners.
func (s *StratumServer) GetLocalEfficiency() float64 {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	var totalAccepted uint64 = 0
	var totalRejected uint64 = 0

	for _, c := range s.clients {
		c.Mutex.Lock()
		totalAccepted += c.AcceptedShares
		totalRejected += c.RejectedShares
		c.Mutex.Unlock()
	}

	totalShares := totalAccepted + totalRejected
	if totalShares == 0 {
		return 100.0 // No shares yet, so 100% efficient
	}

	return (float64(totalAccepted) / float64(totalShares)) * 100.0
}

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
/* Keep-alive & job broadcast loops                                     */
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

		// [OPTIMIZATION] Build master params once for all clients
		jobTemplate := *job.BlockTemplate
		masterParams := buildMasterNotifyParams(&jobTemplate)

		s.clientsMutex.RLock()
		clients := make([]*Client, 0, len(s.clients))
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		s.clientsMutex.RUnlock()
		for _, c := range clients {
			if c.Authorized && s.isClientActive(c.ID) {
				// [OPTIMIZATION] Pass master params to sendMiningJob
				s.sendMiningJob(c, &jobTemplate, false, masterParams)
			}
		}
	}
}

func (s *StratumServer) jobBroadcaster() {
	for {
		template := <-s.workManager.NewBlockChan

		newJobID := atomic.AddUint64(&jobCounter, 1)

		// [RACE CONDITION FIX] Deep copy the block template.
		// template is a pointer, and its contents WILL be mutated
		// by the workManager. We must create a new struct and copy the values
		// so this job's data is immutable.
		jobTemplate := *template

		// [OPTIMIZATION] Pre-calculate all expensive Merkle data ONCE.
		wtxidMerkleRoot, witnessCommitment, err := work.CalculateWitnessCommitment(&jobTemplate)
		if err != nil {
			logging.Errorf("Failed to calculate witness commitment for new job: %v", err)
			continue
		}

		coinbaseWtxid, _ := chainhash.NewHashFromStr("0000000000000000000000000000000000000000000000000000000000000000")
		wtxids := []*chainhash.Hash{coinbaseWtxid}
		for _, txTmpl := range jobTemplate.Transactions {
			txBytes, _ := hex.DecodeString(txTmpl.Data)
			var msgTx wire.MsgTx
			_ = msgTx.Deserialize(bytes.NewReader(txBytes))
			wtxid := msgTx.WitnessHash()
			wtxids = append(wtxids, &wtxid)
		}
		txidMerkleLinkBranches := work.CalculateMerkleLinkFromHashes(wtxids, 0)

		// This dummy hash will be replaced by the real one in CreateShare
		dummyCoinbaseHash := work.DblSha256([]byte{})
		txHashesForLink := [][]byte{work.ReverseBytes(dummyCoinbaseHash)}
		for _, tx := range jobTemplate.Transactions {
			txHashBytes, _ := hex.DecodeString(tx.Hash)
			txHashesForLink = append(txHashesForLink, work.ReverseBytes(txHashBytes))
		}
		coinbaseMerkleLinkBranches := work.CalculateMerkleLink(txHashesForLink, 0)
		// [END OPTIMIZATION]

		// [OPTIMIZATION] Build master params once for all clients
		masterParams := buildMasterNotifyParams(&jobTemplate)

		newJob := &Job{
			ID:                   fmt.Sprintf("%d", newJobID),
			BlockTemplate:        &jobTemplate, // <-- Pass a pointer to our new, safe copy
			WitnessCommitment:    witnessCommitment.CloneBytes(),
			WTXIDMerkleRoot:      wtxidMerkleRoot,
			TXIDMerkleLink:       txidMerkleLinkBranches,
			CoinbaseMerkleLink: coinbaseMerkleLinkBranches,
		}
		s.lastJobMutex.Lock()
		s.lastJob = newJob
		// [STALE SHARE FIX] Store the latest previous block hash
		prevHash, _ := chainhash.NewHashFromStr(template.PreviousBlockHash)
		s.latestPrevBlockHash = prevHash
		s.lastJobMutex.Unlock()

		s.clientsMutex.RLock()
		clients := make([]*Client, 0, len(s.clients))
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		s.clientsMutex.RUnlock()

		for _, c := range clients {
			if c.Authorized && s.isClientActive(c.ID) {
				// [OPTIMIZATION] Pass master params to sendMiningJob
				s.sendMiningJob(c, &jobTemplate, true, masterParams)
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
		resp := JSONRPCResponse{ID: id, Result: nil, Error: []interface{}{25, "Malformed submission", nil}}
		_ = c.send(resp)
		return
	}

	workerName, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]
	logging.Debugf("Stratum: Received mining.submit from %s for job %s", workerName, jobID)

	c.Mutex.Lock()
	job, jobExists := c.ActiveJobs[jobID]
	payoutAddr := c.WorkerName
	var jobDifficulty float64
	if jobExists {
		jobDifficulty = job.Difficulty
	}
	c.Mutex.Unlock()

	if !jobExists {
		logging.Warnf("Stratum: Stale share for unknown jobID %s from %s", jobID, c.WorkerName)
		resp := JSONRPCResponse{ID: id, Result: nil, Error: []interface{}{21, "Stale share", nil}}
		_ = c.send(resp)
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
		return
	}

	// [OPTIMIZATION] Pass the pre-calculated job data to CreateShare
	newShare, err := work.CreateShare(
		job.BlockTemplate,
		job.ExtraNonce1,
		extraNonce2,
		nTime,
		nonceHex,
		payoutAddr,
		s.workManager.ShareChain,
		jobDifficulty,
		job.WitnessCommitment,
		job.WTXIDMerkleRoot,
		job.TXIDMerkleLink,
		job.CoinbaseMerkleLink,
	)
	if err != nil {
		logging.Errorf("Stratum: Could not create share object: %v", err)
		resp := JSONRPCResponse{ID: id, Result: nil, Error: []interface{}{26, "Internal error creating share", nil}}
		_ = c.send(resp)
		return
	}

	shareTarget := work.DiffToTarget(jobDifficulty)
	newShare.Target = shareTarget

	s.lastJobMutex.RLock()
	latestPrevHash := s.latestPrevBlockHash
	s.lastJobMutex.RUnlock()

	isStale := false
	// [STALE SHARE FIX] We check if the share's prevBlock is the *current* latest block.
	// [COMPILER FIX] Convert the string from the template to a hash object before comparing.
	jobPrevHash, _ := chainhash.NewHashFromStr(job.BlockTemplate.PreviousBlockHash)
	if latestPrevHash != nil && !jobPrevHash.IsEqual(latestPrevHash) {
		isStale = true
		newShare.ShareInfo.ShareData.StaleInfo = p2pwire.StaleInfoDOA // Mark as Dead on Arrival
		// [COMPILER FIX] Log the string field `PreviousBlockHash`
		logging.Warnf("Stratum: Share %s is stale (DOA), based on old block %s", newShare.Hash.String()[:12], job.BlockTemplate.PreviousBlockHash[:12])
	}

	accepted, reason := newShare.IsValid()
	if accepted && !isStale {
		powInt := new(big.Int).SetBytes(newShare.POWHash.CloneBytes())
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
			Work:        jobDifficulty,
			IsDead:      false,
			WorkerName:  c.WorkerName,
			ShareTarget: shareTarget,
		})

		s.workManager.ShareChain.AddShares([]p2pwire.Share{*newShare})
		s.peerManager.Broadcast(&p2pwire.MsgShares{Shares: []p2pwire.Share{*newShare}})

		logging.Debugf("[DIAG] shareHash.print=%s", newShare.Hash.String())
		logging.Debugf("[DIAG] powInt(H^LE->big)=%s", powInt.Text(16))

		// [CRITICAL FIX] Use the nBits from the JOB the share was for, not the global template.
		nBits64, err := strconv.ParseUint(job.BlockTemplate.Bits, 16, 32)
		if err != nil {
			logging.Errorf("Could not parse bits from block template: %v", err)
		} else {
			nBits := uint32(nBits64)
			netTarget := blockchain.CompactToBig(nBits)

			logging.Debugf("[DIAG] nBits=0x%08x netTarget=%s", nBits, netTarget.Text(16))
			logging.Debugf("[DIAG] powInt=%s", powInt.Text(16))
			logging.Debugf("[DIAG] netTarget=%s", netTarget.Text(16))
			logging.Debugf("[DIAG] Checking if share is a valid block...")
			if powInt.Cmp(netTarget) <= 0 {
				netDiff := TargetToDiff(netTarget)
				logging.Successf("!!!! BLOCK FOUND !!!! Share %s (Diff %.2f) meets network target (Diff %.2f)!", newShare.Hash.String()[:12], shareDiff, netDiff)
				go s.workManager.SubmitBlock(newShare, job.BlockTemplate)
			} else {
				logging.Debugf("[DIAG] Share is not a valid block.")
			}
		}
	} else {
		if isStale {
			reason = "Stale share (DOA)"
			accepted = false // Ensure it's marked as rejected
		} else if !accepted {
			reason = fmt.Sprintf("Share hash > target (%s)", reason)
		}
		logging.Warnf("Stratum: Share rejected – %s for %s", reason, c.WorkerName)
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
	}

	if isStale {
		s.workManager.ShareChain.AddShares([]p2pwire.Share{*newShare})
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

	if !s.workManager.IsSynced() || !s.peerManager.IsSynced() {
		logging.Warnf("Stratum: Rejecting miner authorization from %s - P2Pool is not synced.", c.Conn.RemoteAddr())
		resp := JSONRPCResponse{ID: id, Result: false, Error: []interface{}{27, "P2Pool node is not synced yet", nil}}
		_ = c.send(resp)
		return
	}

	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 1 {
		logging.Warnf("Stratum: Bad authorize params from %s", c.Conn.RemoteAddr())
		return
	}

	addr := params[0]
	// Handle full address with worker name (e.g., vtc1...worker1)
	if strings.Contains(addr, ".") {
		addr = strings.Split(addr, ".")[0]
	}

	logging.Debugf("Stratum: Miner %s trying to authorize with address: %s", c.Conn.RemoteAddr(), addr)
	if !isValidVtcAddress(addr) {
		logging.Warnf("Stratum: Miner %s failed authorization with INVALID address: %s", c.Conn.RemoteAddr(), addr)
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
		// [RACE CONDITION FIX] We must pass a *copy* of the template, not the pointer
		jobTemplate := *job.BlockTemplate

		// [OPTIMIZATION] Build master params for this job send
		masterParams := buildMasterNotifyParams(&jobTemplate)
		go s.sendMiningJob(c, &jobTemplate, true, masterParams)
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

// [MODIFIED] Added masterParams argument
func (s *StratumServer) sendMiningJob(c *Client, tmpl *work.BlockTemplate, clean bool, masterParams []interface{}) {
	if c.closed.Load() || tmpl == nil {
		return
	}

	c.Mutex.Lock()
	newJobID := atomic.AddUint64(&jobCounter, 1)
	jobID := fmt.Sprintf("%d", newJobID)

	// [RACE CONDITION FIX] Deep copy the block template.
	// tmpl is a pointer, and its contents WILL be mutated
	// by the jobBroadcaster. We must create a new struct and copy the values
	// so this job's data is immutable.
	jobTemplate := *tmpl

	// [OPTIMIZATION] Get the pre-calculated data from the master job
	s.lastJobMutex.RLock()
	masterJob := s.lastJob
	s.lastJobMutex.RUnlock()
	if masterJob == nil {
		c.Mutex.Unlock()
		return // No job to send
	}

	job := &Job{
		ID:                   jobID,
		BlockTemplate:        &jobTemplate, // <-- Pass a pointer to our new, safe copy
		ExtraNonce1:          c.ExtraNonce1,
		Difficulty:           c.CurrentDifficulty,
		WitnessCommitment:    masterJob.WitnessCommitment,
		WTXIDMerkleRoot:      masterJob.WTXIDMerkleRoot,
		TXIDMerkleLink:       masterJob.TXIDMerkleLink,
		CoinbaseMerkleLink: masterJob.CoinbaseMerkleLink,
	}
	if clean {
		if len(c.ActiveJobs) > 20 { // Keep a buffer of old jobs
			keys := make([]string, 0, len(c.ActiveJobs))
			for k := range c.ActiveJobs {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				iNum, _ := strconv.Atoi(keys[i])
				jNum, _ := strconv.Atoi(keys[j])
				return iNum < jNum
			})
			for i := 0; i < len(keys)/2; i++ {
				delete(c.ActiveJobs, keys[i])
			}
		}
	}
	c.ActiveJobs[jobID] = job
	worker := c.WorkerName
	c.Mutex.Unlock()

	// [OPTIMIZATION] Create the final params slice efficiently
	// 0: jobID (client-specific)
	// 1-7: masterParams (common)
	// 8: clean (client-specific)
	params := make([]interface{}, 9)
	params[0] = jobID
	copy(params[1:8], masterParams) // Copy the 7 common params
	params[8] = clean

	msg := JSONRPCResponse{Method: "mining.notify", Params: params}
	if err := c.send(msg); err != nil {
		logging.Debugf("Stratum: Failed to send job to %s: %v", c.Conn.RemoteAddr(), err)
		s.dropClient(c)
	} else {
		txCount := len(jobTemplate.Transactions)
		txSize := 0
		for _, tx := range jobTemplate.Transactions {
			txSize += len(tx.Data)
		}
		blockValue := float64(jobTemplate.CoinbaseValue) / 100000000.0

		logging.Noticef("New job %s sent to %s (Height: %d, Block Value: %.2f VTC, Share Diff: %.2f, %d tx, %.1f kB)",
			jobID,
			worker,
			jobTemplate.Height,
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

func (s *StratumServer) GetWorkManager() *work.WorkManager {
	return s.workManager
}
