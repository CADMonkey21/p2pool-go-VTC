package stratum

import "encoding/json"

// JSONRPCRequest represents a request from the miner.
type JSONRPCRequest struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params *json.RawMessage `json:"params"`
}

// JSONRPCResponse can represent both a response and a notification to the miner.
// The `omitempty` tag is used so we can use one struct for multiple message types.
type JSONRPCResponse struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Result interface{}      `json:"result,omitempty"`
	Error  interface{}      `json:"error,omitempty"`
	Method string           `json:"method,omitempty"`
	Params interface{}      `json:"params,omitempty"`
}
