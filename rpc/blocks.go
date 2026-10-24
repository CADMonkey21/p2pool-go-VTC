package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// BlockInfo defines the structure of the data returned by the getblock RPC.
type BlockInfo struct {
	Hash          string `json:"hash"`
	Confirmations int64  `json:"confirmations"`
	Height        int32  `json:"height"`
}

// GetBlockTemplate requests the latest block template from Vertcoin Core.
// It explicitly passes the required "segwit" rule.
func (c *Client) GetBlockTemplate() (json.RawMessage, error) {
	type TemplateRequest struct {
		Rules []string `json:"rules"`
	}

	reqData := TemplateRequest{
		Rules: []string{"segwit"},
	}

	params := []interface{}{reqData}

	resp, err := c.Call("getblocktemplate", params)
	if err != nil {
		return nil, err
	}

	return resp.Result, nil
}

// GetBlockHeader retrieves header details for a block hash.
func (c *Client) GetBlockHeader(hash *chainhash.Hash) (*BlockHeaderInfo, error) {
	params := []interface{}{
		hash.String(),
		true,
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
		1,
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

// ProposeBlock performs a BIP22 "proposal" dry-run via getblocktemplate.
// Vertcoin Core runs TestBlockValidity() on the full block (checking coinbase, BIP34 height,
// SegWit witness commitment, merkle root, and all transactions) while ignoring PoW target.
func (c *Client) ProposeBlock(block *wire.MsgBlock) (string, error) {
	var blockBuf bytes.Buffer
	if err := block.Serialize(&blockBuf); err != nil {
		return "", err
	}
	blockHex := hex.EncodeToString(blockBuf.Bytes())

	type ProposalRequest struct {
		Mode  string   `json:"mode"`
		Data  string   `json:"data"`
		Rules []string `json:"rules"`
	}

	reqData := ProposalRequest{
		Mode:  "proposal",
		Data:  blockHex,
		Rules: []string{"segwit"},
	}

	resp, err := c.Call("getblocktemplate", []interface{}{reqData})
	if err != nil {
		return "", err
	}
	if resp.Result == nil || string(resp.Result) == "null" {
		return "accepted", nil
	}
	var reason string
	if err := json.Unmarshal(resp.Result, &reason); err != nil {
		return string(resp.Result), nil
	}
	if reason == "" {
		return "accepted", nil
	}
	return reason, nil
}

// BlockRejectedError is returned by SubmitBlock when vertcoind answers with a rejection reason.
type BlockRejectedError struct {
	Reason string
}

func (e *BlockRejectedError) Error() string {
	return "submitblock rejected by vertcoind: " + e.Reason
}

// SubmitBlock sends a serialized solved block to Vertcoin Core and verifies it was accepted.
func (c *Client) SubmitBlock(block *wire.MsgBlock) error {
	var blockBuf bytes.Buffer
	if err := block.Serialize(&blockBuf); err != nil {
		return err
	}
	blockHex := hex.EncodeToString(blockBuf.Bytes())

	params := []interface{}{
		blockHex,
	}

	resp, err := c.Call("submitblock", params)
	if err != nil {
		return err
	}

	// In Vertcoin Core, submitblock returns JSON null on success, or a string reason if rejected.
	if resp.Result != nil && string(resp.Result) != "null" {
		var reason string
		if err := json.Unmarshal(resp.Result, &reason); err == nil && reason != "" {
			return &BlockRejectedError{Reason: reason}
		}
		return &BlockRejectedError{Reason: string(resp.Result)}
	}

	return nil
}
