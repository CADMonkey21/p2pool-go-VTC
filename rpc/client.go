package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/gertjaap/p2pool-go/config"
	// "github.com/gertjaap/p2pool-go/logging" // This line has been removed
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
