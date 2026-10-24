package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
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

// HTTPError is returned when vertcoind answers with a non-2xx status.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP Error %d: %s (Check your rpcuser/rpcpass and rpcallowip)", e.StatusCode, e.Body)
}

const rpcInWarmup = -28

// IsTransient reports whether err is a transport failure, an HTTP 5xx or vertcoind warming up,
// i.e. whether the same call may succeed if repeated.
func IsTransient(err error) bool {
	var rej *BlockRejectedError
	var rpcErr *RPCError
	var httpErr *HTTPError
	switch {
	case err == nil, errors.As(err, &rej):
		return false
	case errors.As(err, &rpcErr):
		return rpcErr.Code == rpcInWarmup
	case errors.As(err, &httpErr):
		var resp RPCResponse
		if json.Unmarshal([]byte(httpErr.Body), &resp) == nil && resp.Error != nil {
			return resp.Error.Code == rpcInWarmup
		}
		return httpErr.StatusCode >= 500
	}
	return true
}

// Client is a JSON-RPC client for vertcoind.
type Client struct {
	httpClient *http.Client
	endpoint   string
	username   string
	password   string
	cookieFile string
	mu         sync.Mutex
}

func (c *Client) loadCookie() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.cookieFile
	if p == "" {
		h, _ := os.UserHomeDir()
		dirs := []string{filepath.Join(h, ".vertcoin"), filepath.Join(os.Getenv("APPDATA"), "Vertcoin")}
		for _, base := range dirs {
			if b, err := os.ReadFile(filepath.Join(base, "vertcoin.conf")); err == nil {
				for _, ln := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(strings.TrimSpace(ln), "datadir=") {
						dirs = append([]string{strings.TrimSpace(strings.SplitN(ln, "=", 2)[1])}, dirs...)
					}
				}
			}
		}
		for _, d := range dirs {
			sd := d
			if config.Active.Testnet {
				sd = filepath.Join(d, "testnet3")
			}
			if _, e := os.Stat(filepath.Join(sd, ".cookie")); e == nil {
				p = filepath.Join(sd, ".cookie")
				break
			}
		}
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return false
	}
	kv := strings.SplitN(strings.TrimSpace(string(b)), ":", 2)
	if len(kv) == 2 {
		c.username, c.password, c.cookieFile = kv[0], kv[1], p
		return true
	}
	return false
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
		httpClient: &http.Client{Timeout: 60 * time.Second},
		endpoint:   endpoint,
		username:   cfg.RPCUser,
		password:   cfg.RPCPass,
		cookieFile: cfg.RPCCookieFile,
	}
}

// Call performs a JSON-RPC call.
func (c *Client) Call(method string, params []interface{}) (*RPCResponse, error) {
	return c.callInternal(method, params, false)
}
func (c *Client) callInternal(method string, params []interface{}, retry401 bool) (*RPCResponse, error) {
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

	if c.password == "" || c.cookieFile != "" {
		c.loadCookie()
	}
	c.mu.Lock()
	req.SetBasicAuth(c.username, c.password)
	c.mu.Unlock()
	req.Header.Set("Content-Type", "application/json")

	// [NEW] Tells Go to close the connection after the request, preventing Keep-Alive EOF errors
	req.Close = true

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Catch HTTP errors before trying to parse JSON
	if resp.StatusCode == 401 && !retry401 && c.loadCookie() {
		return c.callInternal(method, params, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var rpcResp RPCResponse
	err = json.Unmarshal(body, &rpcResp)
	if err != nil {
		return nil, fmt.Errorf("JSON parse failed. HTTP %d. Error: %v | Body: %s", resp.StatusCode, err, string(body))
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

// GetRawBlockHeaderHex retrieves the raw 80-byte hex-encoded block header for a block hash.
func (c *Client) GetRawBlockHeaderHex(blockHash string) (string, error) {
	resp, err := c.Call("getblockheader", []interface{}{blockHash, false})
	if err != nil {
		return "", err
	}
	var rawHex string
	if err := json.Unmarshal(resp.Result, &rawHex); err != nil {
		return "", err
	}
	return rawHex, nil
}
