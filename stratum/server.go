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
	if strings.HasPrefix(strings.ToLower(addr), "vtc1") || strings.HasPrefix(strings.ToLower(addr), "tvtc1") {
		hrp, _, err := bech32.Decode(addr)
		expectedHrp := "vtc"
		if config.Active.Testnet { expectedHrp = "tvtc" }
		return err == nil && hrp == expectedHrp
	}

	decoded := base58.Decode(addr)
	if len(decoded) < 5 { return false }
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	expected := doubleSHA256(payload)[:4]
	for i := 0; i < 4; i++ {
		if checksum[i] != expected[i] { return false }
	}

	version := payload[0]
	if config.Active.Testnet {
		return version == 0x6f || version == 0xc4
	}
	return version == 0x47 || version == 0x05
}

func extractID(raw *json.RawMessage) interface{} {
	if raw == nil { return nil }
	var v interface{}
	if err := json.Unmarshal(*raw, &v); err != nil { return nil }
	return v
}

func TargetToDiff(target *big.Int) float64 {
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)
	if target == nil || target.Sign() <= 0 { return 0 }
	resultFloat := new(big.Float).Quo(new(big.Float).SetInt(maxTarget), new(big.Float).SetInt(target))
	f64, _ := resultFloat.Float64()
	return f64
}

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
		if err != nil { continue }
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
	if totalShares == 0 { return 100.0 }
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
	for _, c := range s.clients { clients = append(clients, c) }
	return clients
}

func (s *StratumServer) GetHashrateForClient(id uint64) float64 {
	s.clientsMutex.RLock()
	client, ok := s.clients[id]
	s.clientsMutex.RUnlock()

	if !ok { return 0.0 }
	client.Mutex.Lock()
	defer client.Mutex.Unlock()

	datums, span := client.LocalRateMonitor.GetDatumsInLast(10 * time.Minute)
	if len(datums) < 5 || span < 30*time.Second { return 0.0 }
	workTotal := 0.0
	for _, d := range datums { workTotal += d.Work }
	return (workTotal * hashrateConstant) / span.Seconds()
}

func (s *StratumServer) dropClient(c *Client) {
	if c.closed.Load() { return }
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

		dummyCoinbaseHash := work.DblSha256([]byte{})
		txHashesForLink := [][]byte{work.ReverseBytes(dummyCoinbaseHash)}
		for _, tx := range jobTemplate.Transactions {
			txHashBytes, _ := hex.DecodeString(tx.Hash)
			txHashesForLink = append(txHashesForLink, work.ReverseBytes(txHashBytes))
		}
		merkleLinkBranches := work.CalculateMerkleLink(txHashesForLink, 0)

		masterParams := buildMasterNotifyParams(&jobTemplate)

		newJob := &Job{
			ID:                   fmt.Sprintf("%d", newJobID),
			BlockTemplate:        &jobTemplate, 
			WitnessCommitment:    witnessCommitment.CloneBytes(),
			WTXIDMerkleRoot:      wtxidMerkleRoot,
			TXIDMerkleLink:       merkleLinkBranches,
			CoinbaseMerkleLink:   merkleLinkBranches,
		}
		s.lastJobMutex.Lock()
		s.lastJob = newJob
		s.latestPrevBlockHash, _ = chainhash.NewHashFromStr(template.PreviousBlockHash)
		s.lastJobMutex.Unlock()

		clients := s.GetClients()
		for _, c := range clients {
			if c.Authorized && !c.closed.Load() {
				go s.sendMiningJob(c, &jobTemplate, true, masterParams)
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
		if err := decoder.Decode(&req); err != nil { return }
		client.LastActivity = time.Now()

		switch req.Method {
		case "mining.subscribe": s.handleSubscribe(client, &req)
		case "mining.authorize": s.handleAuthorize(client, &req)
		case "mining.submit": s.handleSubmit(client, &req)
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
	if jobExists { jobDifficulty = job.Difficulty }
	c.Mutex.Unlock()

	if !jobExists {
		_ = c.send(JSONRPCResponse{ID: id, Error: []interface{}{21, "Stale share", nil}})
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
		return
	}

	newShare, err := work.CreateShare(
		job.BlockTemplate, job.ExtraNonce1, extraNonce2, nTime, nonceHex, payoutAddr,
		s.workManager.ShareChain, jobDifficulty, job.WitnessCommitment,
		job.WTXIDMerkleRoot, job.TXIDMerkleLink, job.CoinbaseMerkleLink,
	)
	if err != nil {
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
		newShare.ShareInfo.ShareData.StaleInfo = 255 
	}

	accepted, reason := newShare.IsValid()
	if accepted && !isStale {
		powInt := new(big.Int).SetBytes(newShare.POWHash.CloneBytes())
		shareDiff := TargetToDiff(powInt)
		logging.Successf("SHARE ACCEPTED from %s (Diff: %.2f)", c.WorkerName, shareDiff)

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
			logging.Successf("!!!! BLOCK FOUND !!!! Share meets network target!")
			go s.workManager.SubmitBlock(newShare, job.BlockTemplate)
		}
	} else {
		if isStale { reason = "Stale share (DOA)" }
		logging.Warnf("Stratum: Share rejected – %s", reason)
		c.Mutex.Lock()
		c.RejectedShares++
		c.Mutex.Unlock()
	}

	if isStale { go s.workManager.ShareChain.AddShares([]work.Share{*newShare}) }
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
	json.Unmarshal(*req.Params, &params)
	addr := strings.Split(params[0], ".")[0]

	if !isValidVtcAddress(addr) {
		_ = c.send(JSONRPCResponse{ID: id, Result: false, Error: []interface{}{24, "Invalid VTC address", nil}})
		go s.dropClient(c)
		return
	}

	c.Mutex.Lock()
	c.WorkerName = strings.ToLower(addr)
	c.Authorized = true
	c.Mutex.Unlock()

	_ = c.send(JSONRPCResponse{ID: id, Result: true, Error: nil})
	s.sendDifficulty(c, config.Active.Vardiff.MinDiff)

	s.lastJobMutex.RLock()
	job := s.lastJob
	s.lastJobMutex.RUnlock()
	if job != nil {
		jobTemplate := *job.BlockTemplate
		go s.sendMiningJob(c, &jobTemplate, true, buildMasterNotifyParams(&jobTemplate))
	}
}

func (s *StratumServer) sendDifficulty(c *Client, diff float64) {
	if c.closed.Load() { return }
	c.Mutex.Lock()
	c.CurrentDifficulty = diff
	c.Mutex.Unlock()
	_ = c.send(JSONRPCResponse{Method: "mining.set_difficulty", Params: []interface{}{diff}})
}

// [CRITICAL FIX] Mathematically constrained Vardiff loop to prevent Death Spirals
func (s *StratumServer) vardiffLoop(c *Client) {
	ticker := time.NewTicker(time.Duration(config.Active.Vardiff.RetargetTime) * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C
		if c.closed.Load() { return }

		c.Mutex.Lock()
		if !c.Authorized { 
			c.Mutex.Unlock()
			continue 
		}

		datums, span := c.LocalRateMonitor.GetDatumsInLast(c.LocalRateMonitor.maxLookbackTime)
		
		// If miner goes completely silent, smoothly halve the difficulty to bring them back to life
		if len(datums) < 2 {
			newDiff := c.CurrentDifficulty * 0.5
			if newDiff < config.Active.Vardiff.MinDiff { 
				newDiff = config.Active.Vardiff.MinDiff 
			}
			curDiff := c.CurrentDifficulty
			c.Mutex.Unlock()

			if newDiff < curDiff {
				s.sendDifficulty(c, newDiff)
			}
			continue
		}

		avgTime := span.Seconds() / float64(len(datums))
		if avgTime <= 0 { avgTime = 1 }

		ratio := float64(config.Active.Vardiff.TargetTime) / avgTime

		// Clamp the jump multiplier. It can NEVER increase more than 1.5x or decrease more than 0.5x.
		// This mathematically prevents the massive 300x spikes that were silencing the miner.
		if ratio > 1.5 { 
			ratio = 1.5 
		} else if ratio < 0.5 { 
			ratio = 0.5 
		}

		newDiff := c.CurrentDifficulty * ratio

		if newDiff < config.Active.Vardiff.MinDiff { 
			newDiff = config.Active.Vardiff.MinDiff 
		}
		
		curDiff := c.CurrentDifficulty
		c.Mutex.Unlock()

		// Apply the variance buffer to avoid spamming the miner with micro-adjustments
		if newDiff/curDiff > 1.0+config.Active.Vardiff.Variance || newDiff/curDiff < 1.0-config.Active.Vardiff.Variance {
			s.sendDifficulty(c, newDiff)
		}
	}
}

func (s *StratumServer) sendMiningJob(c *Client, tmpl *work.BlockTemplate, clean bool, masterParams []interface{}) {
	if c.closed.Load() || tmpl == nil { return }

	c.Mutex.Lock()
	jobID := fmt.Sprintf("%d", atomic.AddUint64(&jobCounter, 1))
	jobTemplate := *tmpl

	s.lastJobMutex.RLock()
	masterJob := s.lastJob
	s.lastJobMutex.RUnlock()
	if masterJob == nil { c.Mutex.Unlock(); return }

	job := &Job{
		ID: jobID, BlockTemplate: &jobTemplate, ExtraNonce1: c.ExtraNonce1, Difficulty: c.CurrentDifficulty,
		WitnessCommitment: masterJob.WitnessCommitment, WTXIDMerkleRoot: masterJob.WTXIDMerkleRoot,
		TXIDMerkleLink: masterJob.TXIDMerkleLink, CoinbaseMerkleLink: masterJob.CoinbaseMerkleLink,
	}

	if clean && len(c.ActiveJobs) > 20 {
		keys := make([]string, 0, len(c.ActiveJobs))
		for k := range c.ActiveJobs { keys = append(keys, k) }
		for i := 0; i < len(keys)/2; i++ { delete(c.ActiveJobs, keys[i]) }
	}
	c.ActiveJobs[jobID] = job
	c.Mutex.Unlock()

	params := make([]interface{}, 9)
	params[0] = jobID
	copy(params[1:8], masterParams) 
	params[8] = clean

	if err := c.send(JSONRPCResponse{Method: "mining.notify", Params: params}); err != nil {
		go s.dropClient(c)
	}
}
