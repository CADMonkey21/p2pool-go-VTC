package rpc

import (
	"encoding/json"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// GetBlockTemplate now returns the raw JSON result.
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

// New GetBlockHeader function
func (c *Client) GetBlockHeader(hash *chainhash.Hash) (*BlockHeaderInfo, error) {
	params := []interface{}{
		hash.String(),
		true, // verbose = true
	}

	resp, err := c.Call("getblockheader", params)
	if err != nil {
		return nil, err
	}

	var headerInfo BlockHeaderInfo
	err = json.Unmarshal(resp.Result, &headerInfo)
	if err != nil {
		return nil, err
	}

	return &headerInfo, nil
}
