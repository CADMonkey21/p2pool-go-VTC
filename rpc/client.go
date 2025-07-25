package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/CADMonkey21/p2pool-go-vtc/config"
)

// RPCRequest represents a JSON-RPC request.
type RPCRequest struct {
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
	Jsonrpc string        `json:"jsonrpc"`
}

// RPCResponse represents a generic JSON-RPC response.
type RPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
	ID     int             `json:"id"`
}

// RPCError represents an error returned by the RPC server.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC Error - Code: %d, Message: %s", e.Code, e.Message)
}

// Client is a JSON-RPC client for vertcoind.
type Client struct {
	httpClient *http.Client
	endpoint   string
	username   string
	password   string
}

// New BlockHeaderInfo struct for the getblockheader RPC response
type BlockHeaderInfo struct {
	Hash          string  `json:"hash"`
	Confirmations int     `json:"confirmations"`
	Height        int     `json:"height"`
	Version       int     `json:"version"`
	VersionHex    string  `json:"versionHex"`
	MerkleRoot    string  `json:"merkleroot"`
	Time          int64   `json:"time"`
	MedianTime    int64   `json:"mediantime"`
	Nonce         uint32  `json:"nonce"`
	Bits          string  `json:"bits"`
	Difficulty    float64 `json:"difficulty"`
	ChainWork     string  `json:"chainwork"`
	NTx           int     `json:"nTx"`
	PreviousHash  string  `json:"previousblockhash"`
	NextHash      string  `json:"nextblockhash"`
}

// MiningInfo defines the structure for the data returned by the getmininginfo RPC.
type MiningInfo struct {
	NetworkHashPS float64 `json:"networkhashps"`
	Difficulty    float64 `json:"difficulty"`
}

// NewClient creates a new RPC client from the loaded configuration.
func NewClient(cfg config.Config) *Client {
	endpoint := fmt.Sprintf("http://%s:%d", cfg.RPCHost, cfg.RPCPort)
	return &Client{
		httpClient: &http.Client{},
		endpoint:   endpoint,
		username:   cfg.RPCUser,
		password:   cfg.RPCPass,
	}
}

// Call performs a JSON-RPC call.
func (c *Client) Call(method string, params []interface{}) (*RPCResponse, error) {
	request := RPCRequest{
		Method:  method,
		Params:  params,
		ID:      1,
		Jsonrpc: "2.0",
	}

	jsonReq, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.endpoint, bytes.NewBuffer(jsonReq))
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp RPCResponse
	err = json.Unmarshal(body, &rpcResp)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal RPC response: %s", body)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return &rpcResp, nil
}

// GetMiningInfo retrieves network statistics from the daemon.
func (c *Client) GetMiningInfo() (*MiningInfo, error) {
	resp, err := c.Call("getmininginfo", nil)
	if err != nil {
		return nil, err
	}

	var info MiningInfo
	err = json.Unmarshal(resp.Result, &info)
	if err != nil {
		return nil, err
	}

	return &info, nil
}

// SendMany sends a transaction with multiple outputs to the RPC server.
// This is the core function for distributing PPLNS payouts.
func (c *Client) SendMany(amounts map[string]float64) (string, error) {
	// Using a minimal set of parameters for broader compatibility.
	// Parameters: fromaccount (dummy), amounts, minconf, comment
	params := []interface{}{
		"",
		amounts,
		1,
		"p2pool-go-vtc payout",
	}

	resp, err := c.Call("sendmany", params)
	if err != nil {
		return "", err
	}

	var txid string
	err = json.Unmarshal(resp.Result, &txid)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal txid from sendmany response: %w", err)
	}

	return txid, nil
}
