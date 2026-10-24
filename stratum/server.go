package stratum

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

const hashrateConstant = 16777216 // 2^24
var jobCounter uint64
var lastVerifiedHeight int64

func doubleSHA256(data []byte) []byte {
	h := sha256.Sum256(data)
	h2 := sha256.Sum256(h[:])
	return h2[:]
}

func isValidVtcAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
		hrp, ver, prog, err := work.DecodeSegwitAddress(addr)
		expectedHrp := "vtc"
		if config.Active.Testnet {
			expectedHrp = "tvtc"
		}
		if err != nil {
			logging.Errorf("Bech32 decode failed for address '%s': %v", addr, err)
			return false
		}
		// Taproot payouts only get a spendable script once every node builds it.
		if ver == 1 && time.Now().Unix() < work.TaprootPayoutActivationTime {
			return false
		}
		return hrp == expectedHrp && work.IsStandardPayout(ver, len(prog), true)
	}

	decoded := base58.Decode(addr)
	if len(decoded) < 5 {
		return false
	}
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	expected := doubleSHA256(payload)[:4]
	for i := 0; i < 4; i++ {
		if checksum[i] != expected[i] {
			return false
		}
	}

	version := payload[0]
	if len(payload) != 21 {
		return false
	}
	if config.Active.Testnet {
		return version == 0x6f || version == 0xc4
	}
	return version == 0x47 || version == 0x05
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

// TargetToDiff converts a 256-bit hash/target into VerthashMiner difficulty units (Diff 1 = 0x000000ffff..., 2^24).
func TargetToDiff(target *big.Int) float64 {
	maxTarget, _ := new(big.Int).SetString("000000FFFF000000000000000000000000000000000000000000000000000000", 16)
	if target == nil || target.Sign() <= 0 {
		return 0
	}
	resultFloat := new(big.Float).SetPrec(256).Quo(new(big.Float).SetPrec(256).SetInt(maxTarget), new(big.Float).SetPrec(256).SetInt(target))
	f64, _ := resultFloat.Float64()
	return f64
}

// formatStratumPrevHash correctly prepares prevhash for VerthashMiner / ccminer.
func formatStratumPrevHash(rpcPrevHashHex string) string {
	h, err := chainhash.NewHashFromStr(rpcPrevHashHex)
	if err != nil {
		return rpcPrevHashHex
	}
	leBytes := h.CloneBytes() // 32-byte internal little-endian
	stratumBytes := make([]byte, 32)
	for i := 0; i < 32; i += 4 {
		stratumBytes[i+0] = leBytes[i+3]
		stratumBytes[i+1] = leBytes[i+2]
		stratumBytes[i+2] = leBytes[i+1]
		stratumBytes[i+3] = leBytes[i+0]
	}
	return hex.EncodeToString(stratumBytes)
}

const reconnectDiffTTL = 30 * time.Minute

type cachedDiff struct {
	diff      float64
	updatedAt time.Time
}

type StratumServer struct {
	workManager         *work.WorkManager
	p2pNode             *p2p.Node
	clients             map[uint64]*Client
	clientsMutex        sync.RWMutex
	lastJob             *Job
	latestPrevBlockHash *chainhash.Hash
	lastJobMutex        sync.RWMutex
	diffCache           map[string]cachedDiff
	diffCacheMutex      sync.Mutex
}

func NewStratumServer(wm *work.WorkManager, node *p2p.Node) *StratumServer {
	s := &StratumServer{
		workManager: wm,
		p2pNode:     node,
		clients:     make(map[uint64]*Client),
	}
	go s.jobBroadcaster()
	return s
}

func (s *StratumServer) GetWorkManager() *work.WorkManager {
	return s.workManager
}

func (s *StratumServer) Serve(listener net.Listener) error {
	logging.Infof("Stratum: Listening for miners on %s", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go s.handleMinerConnection(conn)
	}
}

func (s *StratumServer) GetLocalEfficiency() float64 {
	clients := s.GetClients()
	var totalAccepted, totalRejected uint64
	for _, c := range clients {
		c.Mutex.Lock()
		totalAccepted += c.AcceptedShares
		totalRejected += c.RejectedShares
		c.Mutex.Unlock()
	}
	totalShares := totalAccepted + totalRejected
	if totalShares == 0 {
		return 100.0
	}
	return (float64(totalAccepted) / float64(totalShares)) * 100.0
}

func (s *StratumServer) GetLocalSharesPerSecond() float64 {
	clients := s.GetClients()
	total := 0.0
	for _, c := range clients {
		c.Mutex.Lock()
		datums, span := c.LocalRateMonitor.GetDatumsInLast(5 * time.Minute)
		if len(datums) > 0 && span.Seconds() > 1 {
			total += float64(len(datums)) / span.Seconds()
		}
		c.Mutex.Unlock()
	}
	return total
}

func (s *StratumServer) GetLocalHashrate() float64 {
	clients := s.GetClients()
	total := 0.0
	for _, c := range clients {
		total += s.GetHashrateForClient(c.ID)
	}
	return total
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
	if len(datums) < 2 || span < 5*time.Second {
		return 0.0
	}
	workTotal := 0.0
	for _, d := range datums {
		workTotal += d.Work
	}
	return (workTotal * hashrateConstant) / span.Seconds()
}

func (s *StratumServer) dropClient(c *Client) {
	if c.closed.Load() {
		return
	}
	c.closed.Store(true)

	c.Mutex.Lock()
	worker := c.WorkerName
	curDiff := c.CurrentDifficulty
	c.Mutex.Unlock()
	if worker != "" && curDiff > 0 {
		s.rememberClientDiff(c, worker, curDiff)
	}

	c.Conn.Close()

	s.clientsMutex.Lock()
	if _, ok := s.clients[c.ID]; ok {
		delete(s.clients, c.ID)
		logging.Infof("Stratum: Miner %s disconnected.", c.Conn.RemoteAddr())
	}
	s.clientsMutex.Unlock()
}

func (s *StratumServer) jobBroadcaster() {
	for {
		template := <-s.workManager.NewBlockChan
		newJobID := atomic.AddUint64(&jobCounter, 1)
		jobTemplate := *template

		wtxidMerkleRoot, witnessCommitment, _ := work.CalculateWitnessCommitment(&jobTemplate)

		dummyCoinbaseHash := make([]byte, 32)
		txHashesForLink := [][]byte{dummyCoinbaseHash}
		for _, tx := range jobTemplate.Transactions {
			txIDStr := tx.TxID
			if txIDStr == "" {
				txIDStr = tx.Hash
			}
			txHash, err := chainhash.NewHashFromStr(txIDStr)
			if err != nil {
				logging.Errorf("Stratum: Failed to parse TxID %s: %v", txIDStr, err)
				continue
			}
			txHashesForLink = append(txHashesForLink, txHash.CloneBytes())
		}
		merkleLinkBranches := work.CalculateMerkleLink(txHashesForLink, 0)

		newJob := &Job{
			ID:                 fmt.Sprintf("%d", newJobID),
			BlockTemplate:      &jobTemplate,
			WitnessCommitment:  witnessCommitment.CloneBytes(),
			WTXIDMerkleRoot:    wtxidMerkleRoot,
			TXIDMerkleLink:     merkleLinkBranches,
			CoinbaseMerkleLink: merkleLinkBranches,
			TipHash:            s.workManager.ShareChain.GetTipHash(),
		}
		s.lastJobMutex.Lock()
		s.lastJob = newJob
		s.latestPrevBlockHash, _ = chainhash.NewHashFromStr(template.PreviousBlockHash)
		s.lastJobMutex.Unlock()

		clients := s.GetClients()
		for _, c := range clients {
			if c.Authorized && !c.closed.Load() {
				go s.sendMiningJob(c, &jobTemplate, true)
			}
		}
	}
}

func (s *StratumServer) handleMinerConnection(conn net.Conn) {
	client := NewClient(conn)
	reg := false
	defer func() {
		if reg {
			s.dropClient(client)
		} else {
			_ = conn.Close()
		}
	}()
	decoder := json.NewDecoder(client.Reader)
	for {
		client.Conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		var req JSONRPCRequest
		if err := decoder.Decode(&req); err != nil {
			return
		}
		if !reg {
			reg = true
			logging.Infof("Stratum: New miner connection from %s", client.Conn.RemoteAddr())
			s.clientsMutex.Lock()
			s.clients[client.ID] = client
			s.clientsMutex.Unlock()
			go s.vardiffLoop(client)
		}

		client.LastActivity = time.Now()

		switch req.Method {
		case "mining.subscribe":
			s.handleSubscribe(client, &req)
		case "mining.authorize":
			s.handleAuthorize(client, &req)
		case "mining.submit":
			s.handleSubmit(client, &req)
		}
	}
}

func (s *StratumServer) handleSubmit(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	var params []string
	if req.Params == nil || json.Unmarshal(*req.Params, &params) != nil || len(params) < 5 {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{25, "Malformed submission", nil}})
		return
	}

	_, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]

	c.Mutex.Lock()
	job, jobExists := c.ActiveJobs[jobID]
	payoutAddr := c.WorkerName
	jobDifficulty := 0.0
	isDup := false
	if jobExists {
		jobDifficulty = job.Difficulty
		if job.Submitted == nil {
			job.Submitted = make(map[string]struct{})
		}
		subKey := extraNonce2 + ":" + nTime + ":" + nonceHex
		if _, seen := job.Submitted[subKey]; seen {
			isDup = true
		} else {
			job.Submitted[subKey] = struct{}{}
		}
	}
	c.Mutex.Unlock()

	if isDup {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{22, "Duplicate share", nil}})
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
		return
	}

	if !jobExists {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{21, "Stale share", nil}})
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
		return
	}

	if t, err := strconv.ParseUint(nTime, 16, 32); err != nil || int64(t) > time.Now().Add(work.MaxShareTimeFuture).Unix() || int64(t) < job.BlockTemplate.MinTime {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{20, "ntime out of range", nil}})
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
		return
	}

	if jobDifficulty <= 0 {
		jobDifficulty = config.Active.Vardiff.MinDiff
		if jobDifficulty <= 0 {
			jobDifficulty = 0.01
		}
	}

	// Reconstruct the exact coinbase bytes from the job's Coinb1 and Coinb2 so it never drifts
	// from what VerthashMiner hashed even if new shares arrived in the ShareChain in the meantime.
	var prebuiltCoinbase []byte
	if job.Coinb1 != "" && job.Coinb2 != "" {
		prebuiltCoinbase, _ = work.RebuildCoinbaseFromStratumParts(job.Coinb1, job.ExtraNonce1, extraNonce2, job.Coinb2)
	}

	jobTip := job.TipHash
	newShare, err := work.CreateShare(
		job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr,
		s.workManager.ShareChain, jobDifficulty, job.WitnessCommitment,
		job.WTXIDMerkleRoot, job.TXIDMerkleLink, job.CoinbaseMerkleLink,
		prebuiltCoinbase,
	)
	if err != nil {
		logging.Errorf("Stratum: CreateShare error: %v", err)
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{26, "Internal error", nil}})
		return
	}

	if jobTip != nil {
		newShare.ShareInfo.ShareData.PreviousShareHash = jobTip
	}
	if newShare.Hash != nil {
		shKey := newShare.Hash.String()
		c.Mutex.Lock()
		dupH := false
		for _, aj := range c.ActiveJobs {
			if aj.Submitted != nil {
				if _, ok := aj.Submitted[shKey]; ok {
					dupH = true
					break
				}
			}
		}
		if !dupH && job.Submitted != nil {
			job.Submitted[shKey] = struct{}{}
		}
		c.Mutex.Unlock()
		if dupH {
			_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{22, "Duplicate share", nil}})
			c.Mutex.Lock()
			c.RejectedShares++
			c.Mutex.Unlock()
			return
		}
	}
	committedTarget := job.ShareTarget
	if committedTarget == nil {
		committedTarget = work.DiffToTarget(s.workManager.ShareChain.GetExpectedShareDifficulty(jobTip))
	}
	newShare.Target = committedTarget
	s.lastJobMutex.RLock()
	latestPrevHash := s.latestPrevBlockHash
	s.lastJobMutex.RUnlock()
	jobPrevHash, _ := chainhash.NewHashFromStr(job.BlockTemplate.PreviousBlockHash)
	isStale := latestPrevHash != nil && !jobPrevHash.IsEqual(latestPrevHash)
	if err := newShare.CalculateHashes(); err != nil || newShare.POWHash == nil {
		_ = c.send(JSONRPCResponse{ID: id, Result: false})
		return
	}
	powInt := new(big.Int).SetBytes(newShare.POWHash.CloneBytes())
	shareDiff := TargetToDiff(powInt)
	accepted := powInt.Cmp(work.DiffToTarget(jobDifficulty)) <= 0
	reason := "Share PoW hash does not meet vardiff target"
	if accepted && !isStale {
		logging.Successf("SHARE ACCEPTED from %s (Share Diff: %.5f / Target Diff: %.5f)", c.WorkerName, shareDiff, jobDifficulty)
		if atomic.LoadInt64(&lastVerifiedHeight) != job.BlockTemplate.Height {
			if atomic.CompareAndSwapInt64(&lastVerifiedHeight, atomic.LoadInt64(&lastVerifiedHeight), job.BlockTemplate.Height) {
				go s.workManager.VerifyCandidateBlock(newShare, job.BlockTemplate)
			}
		}
		c.Mutex.Lock()
		c.AcceptedShares++
		c.Mutex.Unlock()
		c.LocalRateMonitor.AddDatum(ShareDatum{Work: jobDifficulty, WorkerName: c.WorkerName})
		nBits64, _ := strconv.ParseUint(job.BlockTemplate.Bits, 16, 32)
		netTarget := blockchain.CompactToBig(uint32(nBits64))
		poolShareDiff := job.PoolShareDiff
		if poolShareDiff <= 0 {
			poolShareDiff = s.workManager.ShareChain.GetExpectedShareDifficulty(jobTip)
		}
		if powInt.Cmp(committedTarget) <= 0 || powInt.Cmp(netTarget) <= 0 {
			if ok, r := newShare.IsValid(); ok {
				logging.Infof("P2Pool Share qualified for network ShareChain! (Share Diff: %.4f >= Pool Share Diff: %.4f)", shareDiff, poolShareDiff)
				go func(sh work.Share) {
					if s.workManager.ShareChain.AddShares([]work.Share{sh}) > 0 {
						s.NotifyNewShare()
					}
					if sb, err := json.Marshal(sh); err == nil && s.p2pNode != nil {
						s.p2pNode.BroadcastShare(sb)
					}
				}(*newShare)
			} else {
				logging.Warnf("Stratum: Share met pool target but failed IsValid: %s", r)
			}
		}
		if config.Active.ForceBlockFind {
			netTarget, _ = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
		}
		if powInt.Cmp(netTarget) <= 0 {
			logging.Successf("!!!! BLOCK FOUND !!!! Share meets network target! Submitting to vertcoind...")
			go func() {
				if err := s.workManager.SubmitBlock(newShare, job.BlockTemplate); err != nil {
					logging.Errorf("CRITICAL: Block submission failed: %v", err)
				}
			}()
		}
	} else {
		if isStale {
			reason = "Stale share (DOA)"
		}
		logging.Warnf("Stratum: Share rejected – %s (Actual Diff: %.6f vs Needed: %.6f)", reason, shareDiff, jobDifficulty)
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
	}
	_ = c.send(JSONRPCResponse{ID: id, Result: accepted && !isStale, Error: nil})
}

func (s *StratumServer) handleSubscribe(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	c.Mutex.Lock()
	c.SubscriptionID = c.ExtraNonce1
	result := []interface{}{[]interface{}{"mining.notify", c.SubscriptionID}, c.ExtraNonce1, c.Nonce2Size}
	c.Mutex.Unlock()
	_ = c.send(JSONRPCResponse{ID: id, Result: result, Error: nil})
}

func (s *StratumServer) handleAuthorize(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	if !s.workManager.IsSynced() {
		_ = c.send(JSONRPCResponse{ID: id, Result: false, Error: []interface{}{27, "Node not synced", nil}})
		return
	}

	var params []string
	if req.Params == nil || json.Unmarshal(*req.Params, &params) != nil || len(params) == 0 {
		_ = c.send(JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Malformed authorize params", nil}})
		go s.dropClient(c)
		return
	}

	rawUser := strings.TrimSpace(params[0])
	rawPass := ""
	if len(params) > 1 {
		rawPass = strings.TrimSpace(params[1])
	}
	addr, hintDiff := parseAuthorizeUserAndHint(rawUser, rawPass)
	logging.Infof("Stratum: Miner authorizing address: '%s'", addr)

	if !isValidVtcAddress(addr) {
		logging.Errorf("Stratum: Rejected invalid VTC address: '%s'", addr)
		_ = c.send(JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Invalid VTC address", nil}})
		go s.dropClient(c)
		return
	}

	normAddr := addr
	if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
		normAddr = strings.ToLower(addr)
	}

	c.Mutex.Lock()
	c.WorkerName = normAddr
	c.Authorized = true
	c.Mutex.Unlock()

	startDiff := s.initialDifficultyForClient(c, normAddr, hintDiff)

	_ = c.send(JSONRPCResponse{ID: id, Result: true, Error: nil})
	s.sendDifficulty(c, startDiff)

	s.lastJobMutex.RLock()
	job := s.lastJob
	s.lastJobMutex.RUnlock()
	if job != nil {
		jobTemplate := *job.BlockTemplate
		go s.sendMiningJob(c, &jobTemplate, true)
	}
}

func (s *StratumServer) sendDifficulty(c *Client, diff float64) {
	if c.closed.Load() {
		return
	}
	c.Mutex.Lock()
	c.CurrentDifficulty = diff
	worker := c.WorkerName
	c.Mutex.Unlock()
	if worker != "" && diff > 0 {
		s.rememberClientDiff(c, worker, diff)
	}
	_ = c.send(JSONRPCResponse{Method: "mining.set_difficulty", Params: []interface{}{diff}})
}

func parseAuthorizeUserAndHint(rawUser, rawPass string) (string, float64) {
	userPart := strings.TrimSpace(rawUser)
	var hint float64

	if plusIdx := strings.LastIndex(userPart, "+"); plusIdx > 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(userPart[plusIdx+1:]), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 {
			hint = v
		}
		userPart = userPart[:plusIdx]
	}

	addr := strings.TrimSpace(strings.Split(userPart, ".")[0])

	if hint <= 0 && rawPass != "" {
		for _, tok := range strings.FieldsFunc(rawPass, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '	'
		}) {
			tok = strings.TrimSpace(tok)
			lower := strings.ToLower(tok)
			var valStr string
			switch {
			case strings.HasPrefix(lower, "d="):
				valStr = tok[2:]
			case strings.HasPrefix(lower, "diff="):
				valStr = tok[5:]
			case strings.HasPrefix(lower, "sd="):
				valStr = tok[3:]
			}
			if valStr != "" {
				if v, err := strconv.ParseFloat(strings.TrimSpace(valStr), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 {
					hint = v
					break
				}
			}
		}
	}

	return addr, hint
}

func clientHostKey(c *Client) string {
	if c == nil || c.Conn == nil || c.Conn.RemoteAddr() == nil {
		return ""
	}
	addrStr := c.Conn.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(addrStr); err == nil {
		return host
	}
	return addrStr
}

func (s *StratumServer) rememberClientDiff(c *Client, worker string, diff float64) {
	if worker == "" || diff <= 0 || math.IsNaN(diff) || math.IsInf(diff, 0) {
		return
	}
	now := time.Now()
	host := clientHostKey(c)

	s.diffCacheMutex.Lock()
	defer s.diffCacheMutex.Unlock()
	if s.diffCache == nil {
		s.diffCache = make(map[string]cachedDiff)
	}
	for k, v := range s.diffCache {
		if now.Sub(v.updatedAt) > reconnectDiffTTL {
			delete(s.diffCache, k)
		}
	}
	if host != "" {
		s.diffCache[host+"|"+worker] = cachedDiff{diff: diff, updatedAt: now}
	}
	s.diffCache[worker] = cachedDiff{diff: diff, updatedAt: now}
}

func (s *StratumServer) lookupCachedDiff(c *Client, worker string) (float64, bool) {
	if worker == "" {
		return 0, false
	}
	now := time.Now()
	host := clientHostKey(c)

	s.diffCacheMutex.Lock()
	defer s.diffCacheMutex.Unlock()
	if s.diffCache == nil {
		return 0, false
	}
	if host != "" {
		if entry, ok := s.diffCache[host+"|"+worker]; ok && now.Sub(entry.updatedAt) <= reconnectDiffTTL && entry.diff > 0 {
			return entry.diff, true
		}
	}
	if entry, ok := s.diffCache[worker]; ok && now.Sub(entry.updatedAt) <= reconnectDiffTTL && entry.diff > 0 {
		return entry.diff, true
	}
	return 0, false
}

func (s *StratumServer) estimateDiffFromShareChain(worker string, targetTime float64) float64 {
	if s.workManager == nil || s.workManager.ShareChain == nil || worker == "" {
		return 0
	}
	shares, _ := s.workManager.ShareChain.GetRecentShares(120)
	if len(shares) == 0 {
		return 0
	}
	cutoff := time.Now().Add(-reconnectDiffTTL).Unix()
	const windowSec = 1800.0
	var totalWork float64
	var matched int
	var earliest, latest int64
	for i := range shares {
		sh := shares[i]
		if sh == nil {
			continue
		}
		ts := int64(sh.ShareInfo.Timestamp)
		if ts < cutoff || sh.ShareInfo.ShareData.StaleInfo != work.StaleInfoNone {
			continue
		}
		addr := work.DecodeShareAddress(sh)
		if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
			addr = strings.ToLower(addr)
		}
		if addr != worker || sh.Target == nil || sh.Target.Sign() <= 0 {
			continue
		}
		d := s.workManager.ShareChain.GetEffectiveShareDiff(sh)
		if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
			continue
		}
		totalWork += d
		matched++
		if earliest == 0 || ts < earliest {
			earliest = ts
		}
		if ts > latest {
			latest = ts
		}
	}
	if matched < 2 {
		return 0
	}
	span := float64(latest - earliest)
	if span < 60.0 {
		span = 60.0
	}
	if span > windowSec {
		span = windowSec
	}
	if targetTime <= 0 {
		targetTime = 5.0
	}
	return (totalWork / span) * targetTime
}

func (s *StratumServer) initialDifficultyForClient(c *Client, worker string, hintDiff float64) float64 {
	minDiff := config.Active.Vardiff.MinDiff
	if minDiff <= 0 {
		minDiff = 0.01
	}
	maxDiff := 10.0
	if s.workManager != nil && s.workManager.ShareChain != nil {
		if poolShareDiff := s.workManager.ShareChain.GetPoolShareDifficulty(); poolShareDiff > 0 && poolShareDiff < maxDiff {
			maxDiff = poolShareDiff
		}
	}
	if minDiff > maxDiff {
		minDiff = maxDiff
	}

	targetTime := config.Active.Vardiff.TargetTime
	if targetTime <= 0 {
		targetTime = 5.0
	}

	chosen := minDiff
	source := "default minDiff"
	if hintDiff > 0 {
		chosen = hintDiff
		source = "miner difficulty hint"
	} else if cached, ok := s.lookupCachedDiff(c, worker); ok {
		chosen = cached
		source = "reconnect vardiff cache"
	} else if est := s.estimateDiffFromShareChain(worker, targetTime); est > minDiff {
		chosen = est
		source = "sharechain history estimate"
	}

	if chosen < minDiff {
		chosen = minDiff
	}
	if chosen > maxDiff {
		chosen = maxDiff
	}
	if source != "default minDiff" {
		logging.Infof("Stratum: Initial difficulty for '%s' set to %.5f (%s)", worker, chosen, source)
	}
	return chosen
}

func (s *StratumServer) vardiffLoop(c *Client) {
	retargetSecs := time.Duration(config.Active.Vardiff.RetargetTime) * time.Second
	if retargetSecs <= 0 {
		retargetSecs = 30 * time.Second
	}
	ticker := time.NewTicker(retargetSecs)
	defer ticker.Stop()

	minDiff := config.Active.Vardiff.MinDiff
	if minDiff <= 0 {
		minDiff = 0.01
	}
	maxDiff := 10.0

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

		datums, span := c.LocalRateMonitor.GetDatumsInLast(retargetSecs)
		curDiff := c.CurrentDifficulty
		if curDiff <= 0 {
			curDiff = minDiff
		}

		var newDiff float64
		if len(datums) == 0 {
			newDiff = curDiff * 0.5
		} else {
			avgTime := span.Seconds() / float64(len(datums))
			targetTime := config.Active.Vardiff.TargetTime
			if targetTime <= 0 {
				targetTime = 5.0
			}
			ratio := targetTime / avgTime
			if ratio > 2.0 {
				ratio = 2.0
			} else if ratio < 0.5 {
				ratio = 0.5
			}
			newDiff = curDiff * ratio
		}

		poolMaxDiff := s.workManager.ShareChain.GetPoolShareDifficulty()
		if poolMaxDiff > 0 && poolMaxDiff < maxDiff {
			maxDiff = poolMaxDiff
		}
		if newDiff < minDiff {
			newDiff = minDiff
		}
		if p := s.workManager.ShareChain.GetPoolShareDifficulty(); p > 0 && p < maxDiff {
			maxDiff = p
		}
		if newDiff > maxDiff {
			newDiff = maxDiff
		}

		diffChangeRatio := newDiff / curDiff
		shouldUpdate := diffChangeRatio < 0.85 || diffChangeRatio > 1.15
		c.Mutex.Unlock()

		if shouldUpdate {
			s.sendDifficulty(c, newDiff)

			s.lastJobMutex.RLock()
			job := s.lastJob
			s.lastJobMutex.RUnlock()
			if job != nil {
				jobTemplate := *job.BlockTemplate
				nowUnix := time.Now().Unix()
				if nowUnix > jobTemplate.CurTime {
					jobTemplate.CurTime = nowUnix
				} else {
					jobTemplate.CurTime++
				}
				go s.sendMiningJob(c, &jobTemplate, true)
			}
		}
	}
}

func (s *StratumServer) sendMiningJob(c *Client, tmpl *work.BlockTemplate, clean bool) {
	if c.closed.Load() || tmpl == nil {
		return
	}

	c.Mutex.Lock()
	jobID := fmt.Sprintf("%d", atomic.AddUint64(&jobCounter, 1))
	jobTemplate := *tmpl
	payoutAddr := c.WorkerName

	s.lastJobMutex.RLock()
	masterJob := s.lastJob
	s.lastJobMutex.RUnlock()
	if masterJob == nil {
		c.Mutex.Unlock()
		return
	}

	tipHash := s.workManager.ShareChain.GetTipHash()
	pDiffJob := s.workManager.ShareChain.GetExpectedShareDifficulty(tipHash)
	sTarget := work.DiffToTarget(pDiffJob)
	coinb1, coinb2, err := work.SplitCoinbaseForStratumAtTip(&jobTemplate, payoutAddr, masterJob.WitnessCommitment, s.workManager.ShareChain, tipHash)
	if err != nil {
		logging.Errorf("Stratum: Failed to build coinbase for miner %s: %v", payoutAddr, err)
		c.Mutex.Unlock()
		return
	}
	merkleBranchHex := make([]string, len(masterJob.CoinbaseMerkleLink))
	for i, branchHash := range masterJob.CoinbaseMerkleLink {
		merkleBranchHex[i] = hex.EncodeToString(branchHash.CloneBytes())
	}
	diff := c.CurrentDifficulty
	if diff <= 0 {
		diff = config.Active.Vardiff.MinDiff
		if diff <= 0 {
			diff = 0.01
		}
	}
	pDiff := s.workManager.ShareChain.GetPoolShareDifficulty()
	if diff > pDiff {
		diff = pDiff
	}

	job := &Job{
		ID:                 jobID,
		BlockTemplate:      &jobTemplate,
		ExtraNonce1:        c.ExtraNonce1,
		Difficulty:         diff,
		Coinb1:             coinb1,
		Coinb2:             coinb2,
		WitnessCommitment:  masterJob.WitnessCommitment,
		WTXIDMerkleRoot:    masterJob.WTXIDMerkleRoot,
		TXIDMerkleLink:     masterJob.TXIDMerkleLink,
		CoinbaseMerkleLink: masterJob.CoinbaseMerkleLink,
		TipHash:            tipHash, ShareTarget: sTarget, PoolShareDiff: pDiffJob,
	}

	if clean && len(c.ActiveJobs) > 20 {
		keys := make([]string, 0, len(c.ActiveJobs))
		for k := range c.ActiveJobs {
			keys = append(keys, k)
		}
		for i := 0; i < len(keys)/2; i++ {
			delete(c.ActiveJobs, keys[i])
		}
	}
	if !clean && tipHash != nil {
		for _, aj := range c.ActiveJobs {
			if aj.TipHash != nil && aj.TipHash.IsEqual(tipHash) && aj.Difficulty == diff {
				c.Mutex.Unlock()
				return
			}
		}
	}
	c.ActiveJobs[jobID] = job
	c.Mutex.Unlock()

	params := []interface{}{
		jobID,
		formatStratumPrevHash(jobTemplate.PreviousBlockHash),
		coinb1,
		coinb2,
		merkleBranchHex,
		fmt.Sprintf("%08x", jobTemplate.Version),
		jobTemplate.Bits,
		fmt.Sprintf("%08x", jobTemplate.CurTime),
		clean,
	}

	if err := c.send(JSONRPCResponse{Method: "mining.notify", Params: params}); err != nil {
		go s.dropClient(c)
	}
}

func (s *StratumServer) NotifyNewShare() {
	s.lastJobMutex.RLock()
	job := s.lastJob
	s.lastJobMutex.RUnlock()
	if job == nil {
		return
	}
	tmpl := *job.BlockTemplate
	newTip := s.workManager.ShareChain.GetTipHash()
	for _, c := range s.GetClients() {
		if c.Authorized && !c.closed.Load() {
			c.Mutex.Lock()
			sameTip := false
			for _, aj := range c.ActiveJobs {
				if aj.TipHash != nil && newTip != nil && aj.TipHash.IsEqual(newTip) {
					sameTip = true
					break
				}
			}
			c.Mutex.Unlock()
			if sameTip {
				continue
			}
			go s.sendMiningJob(c, &tmpl, false)
		}
	}
}
