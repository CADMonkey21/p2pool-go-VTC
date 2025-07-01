package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// BlockInfo defines the structure of the data returned by the getblock RPC.
// We only include the fields we care about for now.
type BlockInfo struct {
	Hash          string `json:"hash"`
	Confirmations int64  `json:"confirmations"`
	Height        int32  `json:"height"`
}

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

// GetBlock retrieves detailed information about a block from the RPC server.
func (c *Client) GetBlock(hash *chainhash.Hash) (*BlockInfo, error) {
	params := []interface{}{
		hash.String(),
		1, // Verbosity 1 is sufficient for confirmation count
	}
	resp, err := c.Call("getblock", params)
	if err != nil {
		return nil, err
	}

	var blockInfo BlockInfo
	if err := json.Unmarshal(resp.Result, &blockInfo); err != nil {
		return nil, err
	}
	return &blockInfo, nil
}


// SubmitBlock sends a serialized block to the RPC server.
func (c *Client) SubmitBlock(block *wire.MsgBlock) error {
	var blockBuf bytes.Buffer
	if err := block.Serialize(&blockBuf); err != nil {
		return err
	}
	blockHex := hex.EncodeToString(blockBuf.Bytes())

	params := []interface{}{
		blockHex,
	}

	_, err := c.Call("submitblock", params)
	return err
}
