package stratum

import "encoding/json"

// JSONRPCRequest represents a request from the miner.
type JSONRPCRequest struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params *json.RawMessage `json:"params"`
}

// JSONRPCResponse can represent both a response and a notification to the miner.
type JSONRPCResponse struct {
	ID     interface{} `json:"id"`
	Result interface{} `json:"result,omitempty"`
	// CORRECTED: Removed omitempty to ensure the "error" key is always present.
	Error  interface{} `json:"error"`
	Method string      `json:"method,omitempty"`
	Params interface{} `json:"params,omitempty"`
}
