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
	ID     interface{}      `json:"id"`           // Corrected: No omitempty
	Result interface{}      `json:"result,omitempty"`
	Error  interface{}      `json:"error"`          // Corrected: No omitempty
	Method string           `json:"method,omitempty"`
	Params interface{}      `json:"params,omitempty"`
}
