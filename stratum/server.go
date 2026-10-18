package stratum

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/p2p"
	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

const hashrateConstant = 16777216 // 2^24
var jobCounter uint64

func doubleSHA256(data []byte) []byte {
	h := sha256.Sum256(data)
	h2 := sha256.Sum256(h[:])
	return h2[:]
}

func isValidVtcAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
		hrp, _, err := bech32.Decode(strings.ToLower(addr))
		expectedHrp := "vtc"
		if config.Active.Testnet {
			expectedHrp = "tvtc"
		}
		if err != nil {
			logging.Errorf("Bech32 decode failed for address '%s': %v", addr, err)
		}
		return err == nil && hrp == expectedHrp
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

type StratumServer struct {
	workManager         *work.WorkManager
	p2pNode             *p2p.Node
	clients             map[uint64]*Client
	clientsMutex        sync.RWMutex
	lastJob             *Job
	latestPrevBlockHash *chainhash.Hash
	lastJobMutex        sync.RWMutex
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
	logging.Infof("Stratum: New miner connection from %s", client.Conn.RemoteAddr())

	s.clientsMutex.Lock()
	s.clients[client.ID] = client
	s.clientsMutex.Unlock()
	defer s.dropClient(client)

	go s.vardiffLoop(client)

	decoder := json.NewDecoder(client.Reader)
	for {
		client.Conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		var req JSONRPCRequest
		if err := decoder.Decode(&req); err != nil {
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
		}
	}
}

func (s *StratumServer) handleSubmit(c *Client, req *JSONRPCRequest) {
	id := extractID(req.ID)
	var params []string
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) < 5 {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{25, "Malformed submission", nil}})
		return
	}

	_, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]

	c.Mutex.Lock()
	job, jobExists := c.ActiveJobs[jobID]
	payoutAddr := c.WorkerName
	jobDifficulty := 0.0
	if jobExists {
		jobDifficulty = job.Difficulty
	}
	c.Mutex.Unlock()

	if !jobExists {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{21, "Stale share", nil}})
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

	newShare, err := work.CreateShare(
		job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr,
		s.workManager.ShareChain, jobDifficulty, job.WitnessCommitment,
		job.WTXIDMerkleRoot, job.TXIDMerkleLink, job.CoinbaseMerkleLink,
	)
	if err != nil {
		logging.Errorf("Stratum: CreateShare error: %v", err)
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{26, "Internal error", nil}})
		return
	}

	shareTarget := work.DiffToTarget(jobDifficulty)
	newShare.Target = shareTarget

	s.lastJobMutex.RLock()
	latestPrevHash := s.latestPrevBlockHash
	s.lastJobMutex.RUnlock()

	isStale := false
	jobPrevHash, _ := chainhash.NewHashFromStr(job.BlockTemplate.PreviousBlockHash)
	if latestPrevHash != nil && !jobPrevHash.IsEqual(latestPrevHash) {
		isStale = true
		newShare.ShareInfo.ShareData.StaleInfo = 254 // DOA
	}

	accepted, reason := newShare.IsValid()
	if accepted && !isStale {
		powInt := new(big.Int).SetBytes(newShare.POWHash.CloneBytes())
		shareDiff := TargetToDiff(powInt)
		logging.Successf("SHARE ACCEPTED from %s (Share Diff: %.5f / Target Diff: %.5f)", c.WorkerName, shareDiff, jobDifficulty)

		c.Mutex.Lock()
		c.AcceptedShares++
		c.Mutex.Unlock()

		c.LocalRateMonitor.AddDatum(ShareDatum{Work: jobDifficulty, WorkerName: c.WorkerName})

		go func(share work.Share) {
			s.workManager.ShareChain.AddShares([]work.Share{share})
			shareBytes, err := json.Marshal(share)
			if err == nil && s.p2pNode != nil {
				s.p2pNode.BroadcastShare(shareBytes)
			}
		}(*newShare)

		nBits64, _ := strconv.ParseUint(job.BlockTemplate.Bits, 16, 32)
		netTarget := blockchain.CompactToBig(uint32(nBits64))

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
		powInt := new(big.Int).SetBytes(newShare.POWHash.CloneBytes())
		actualDiff := TargetToDiff(powInt)
		logging.Warnf("Stratum: Share rejected – %s (Actual Diff: %.6f vs Needed: %.6f)", reason, actualDiff, jobDifficulty)
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
	}

	if isStale && accepted {
		go s.workManager.ShareChain.AddShares([]work.Share{*newShare})
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
	if err := json.Unmarshal(*req.Params, &params); err != nil || len(params) == 0 {
		_ = c.send(JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Malformed authorize params", nil}})
		go s.dropClient(c)
		return
	}

	addr := strings.TrimSpace(strings.Split(params[0], ".")[0])
	logging.Infof("Stratum: Miner authorizing address: '%s'", addr)

	if !isValidVtcAddress(addr) {
		logging.Errorf("Stratum: Rejected invalid VTC address: '%s'", addr)
		_ = c.send(JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Invalid VTC address", nil}})
		go s.dropClient(c)
		return
	}

	c.Mutex.Lock()
	if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
		c.WorkerName = strings.ToLower(addr)
	} else {
		c.WorkerName = addr
	}
	c.Authorized = true
	c.Mutex.Unlock()

	minDiff := config.Active.Vardiff.MinDiff
	if minDiff <= 0 {
		minDiff = 0.01
	}

	_ = c.send(JSONRPCResponse{ID: id, Result: true, Error: nil})
	s.sendDifficulty(c, minDiff)

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
	c.Mutex.Unlock()
	_ = c.send(JSONRPCResponse{Method: "mining.set_difficulty", Params: []interface{}{diff}})
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
			ratio := avgTime / targetTime
			// Wait: if avgTime < targetTime (shares coming in too fast), we must INCREASE difficulty:
			// newDiff = curDiff * (targetTime / avgTime)
			ratio = targetTime / avgTime
			if ratio > 2.0 {
				ratio = 2.0
			} else if ratio < 0.5 {
				ratio = 0.5
			}
			newDiff = curDiff * ratio
		}

		if newDiff < minDiff {
			newDiff = minDiff
		}
		if newDiff > maxDiff {
			newDiff = maxDiff
		}

		diffChangeRatio := newDiff / curDiff
		shouldUpdate := diffChangeRatio < 0.85 || diffChangeRatio > 1.15
		c.Mutex.Unlock()

		if shouldUpdate {
			s.sendDifficulty(c, newDiff)

			// VerthashMiner only applies sctx->next_diff when it receives a mining.notify packet.
			// By advancing CurTime to the current second, the new job has a unique header
			// so VerthashMiner applies the new difficulty immediately AND never hashes duplicate nonces.
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
				go s.sendMiningJob(c, &jobTemplate, false)
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

	coinb1, coinb2, err := work.SplitCoinbaseForStratum(&jobTemplate, payoutAddr, masterJob.WitnessCommitment)
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

	job := &Job{
		ID:                 jobID,
		BlockTemplate:      &jobTemplate,
		ExtraNonce1:        c.ExtraNonce1,
		Difficulty:         diff,
		WitnessCommitment:  masterJob.WitnessCommitment,
		WTXIDMerkleRoot:    masterJob.WTXIDMerkleRoot,
		TXIDMerkleLink:     masterJob.TXIDMerkleLink,
		CoinbaseMerkleLink: masterJob.CoinbaseMerkleLink,
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
