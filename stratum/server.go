package stratum

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net"
	"time"

	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
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

	var params []string
	err := json.Unmarshal(*req.Params, &params)
	if err != nil || len(params) < 5 {
		logging.Warnf("Stratum: Malformed submit params from %s", c.Conn.RemoteAddr())
		return
	}

	workerName, jobID, extraNonce2, nTime, nonceHex := params[0], params[1], params[2], params[3], params[4]
	logging.Infof("Stratum: Received mining.submit from %s for job %s (Nonce: %s, NTime: %s, ExtraNonce2: %s)", workerName, jobID, nonceHex, nTime, extraNonce2)

	job, jobExists := c.ActiveJobs[jobID]
	if !jobExists {
		logging.Warnf("Stratum: Received submission for unknown jobID %s", jobID)
		return
	}
	logging.Debugf("Stratum: Found job %s for submission, based on block template for height %d", jobID, job.BlockTemplate.Height)

	coinbaseTxBytes, err := work.CreateCoinbaseTx(job.BlockTemplate, config.Active.PoolAddress, c.ExtraNonce1, extraNonce2)
	if err != nil {
		logging.Errorf("Stratum: Failed to create coinbase tx for validation: %v", err)
		return
	}
	coinbaseTxHash := work.DblSha256(coinbaseTxBytes)

	txHashes := [][]byte{coinbaseTxHash}
	for _, tx := range job.BlockTemplate.Transactions {
		txHashBytes, _ := hex.DecodeString(tx.Hash)
		txHashes = append(txHashes, work.ReverseBytes(txHashBytes))
	}
	merkleRoot := calculateMerkleRoot(txHashes)
	logging.Debugf("Calculated Merkle Root: %s", hex.EncodeToString(work.ReverseBytes(merkleRoot)))

	header, err := createBlockHeader(job.BlockTemplate, merkleRoot, nTime, nonceHex)
	if err != nil {
		logging.Errorf("Stratum: Failed to create block header for validation: %v", err)
		return
	}

	powHash := p2pnet.ActiveNetwork.POWHash(header)
	logging.Debugf("PoW hash for submission: %s", hex.EncodeToString(work.ReverseBytes(powHash)))

	shareTarget := new(big.Int)
	// --- THIS IS THE ONLY CHANGE ---
	// The target is now much easier to hit for testing purposes.
	shareTarget.SetString("0333333333333333333333333333333333333333333333333333333333333333", 16)
	// --- END CHANGE ---

	powHashInt := new(big.Int)
	powHashInt.SetBytes(work.ReverseBytes(powHash))

	if powHashInt.Cmp(shareTarget) <= 0 {
		logging.Infof("Stratum: SHARE ACCEPTED! Valid work from %s", c.WorkerName)
		// TODO: Add share to sharechain and broadcast to P2P network
	} else {
		logging.Warnf("Stratum: Share rejected. Hash does not meet target.")
		return
	}

	response := JSONRPCResponse{ID: req.ID, Result: true}
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
	s.sendDifficulty(c)
	s.sendMiningJob(c)
}

func (s *StratumServer) sendDifficulty(c *Client) {
	difficulty := 0.1
	params := []interface{}{difficulty}
	diffResponse := JSONRPCResponse{Method: "mining.set_difficulty", Params: params}
	err := c.Encoder.Encode(diffResponse)
	if err != nil {
		logging.Warnf("Stratum: Failed to send difficulty to %s: %v", c.Conn.RemoteAddr(), err)
	}
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

func calculateMerkleRoot(hashes [][]byte) []byte {
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	for len(hashes) > 1 {
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}

		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			combined := append(hashes[i], hashes[i+1]...)
			newHash := work.DblSha256(combined)
			nextLevel = append(nextLevel, newHash)
		}
		hashes = nextLevel
	}
	return hashes[0]
}

func createBlockHeader(tmpl *work.BlockTemplate, merkleRoot []byte, nTime, nonceHex string) ([]byte, error) {
	versionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(versionBytes, tmpl.Version)

	prevHashBytes, err := hex.DecodeString(tmpl.PreviousBlockHash)
	if err != nil {
		return nil, err
	}

	nTimeBytes, err := hex.DecodeString(nTime)
	if err != nil {
		return nil, err
	}

	nBitsBytes, err := hex.DecodeString(tmpl.Bits)
	if err != nil {
		return nil, err
	}

	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, err
	}

	var header bytes.Buffer
	header.Write(versionBytes)
	header.Write(work.ReverseBytes(prevHashBytes))
	header.Write(work.ReverseBytes(merkleRoot))
	header.Write(nTimeBytes)
	header.Write(nBitsBytes)
	header.Write(nonceBytes)

	return header.Bytes(), nil
}
