package rpc

import (
	"encoding/json"
)

// GetBlockTemplate now returns the raw JSON result to avoid an import cycle.
func (c *Client) GetBlockTemplate() (json.RawMessage, error) {
	params := []interface{}{
		map[string][]string{"rules": {"segwit", "taproot"}},
	}
	
	resp, err := c.Call("getblocktemplate", params)
	if err != nil {
		return nil, err
	}

	return resp.Result, nil
}
