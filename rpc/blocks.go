package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
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
