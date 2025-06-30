package work

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/rpc"
	p2pwire "github.com/gertjaap/p2pool-go/wire"
)

type WorkManager struct {
	rpcClient    *rpc.Client
	ShareChain   *ShareChain
	Templates    map[string]*BlockTemplate
	TemplateMutex sync.RWMutex
	NewBlockChan chan *BlockTemplate
}

func NewWorkManager(rpcClient *rpc.Client, sc *ShareChain) *WorkManager {
	return &WorkManager{
		rpcClient:    rpcClient,
		ShareChain:   sc,
		Templates:    make(map[string]*BlockTemplate),
		NewBlockChan: make(chan *BlockTemplate, 10),
	}
}

// GetLatestTemplate returns the most recent block template available.
func (wm *WorkManager) GetLatestTemplate() *BlockTemplate {
	wm.TemplateMutex.RLock()
	defer wm.TemplateMutex.RUnlock()

	var latestTemplate *BlockTemplate
	for _, t := range wm.Templates {
		if latestTemplate == nil || t.Height > latestTemplate.Height {
			latestTemplate = t
		}
	}
	return latestTemplate
}

func (wm *WorkManager) WatchBlockTemplate() {
	for {
		rawTemplate, err := wm.rpcClient.GetBlockTemplate()
		if err != nil {
			logging.Errorf("Error getting block template: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		var tmpl BlockTemplate
		err = json.Unmarshal(rawTemplate, &tmpl)
		if err != nil {
			logging.Errorf("Error decoding block template: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		wm.TemplateMutex.Lock()
		if _, exists := wm.Templates[tmpl.PreviousBlockHash]; !exists {
			logging.Infof("New block template received for height %d", tmpl.Height)
			wm.Templates[tmpl.PreviousBlockHash] = &tmpl

			select {
			case wm.NewBlockChan <- &tmpl:
			default:
				logging.Warnf("NewBlockChan is full, dropping new template notification.")
			}
		}
		wm.TemplateMutex.Unlock()

		time.Sleep(1 * time.Second)
	}
}

// SubmitBlock constructs and submits a full block to the network based on a winning share.
func (wm *WorkManager) SubmitBlock(share *p2pwire.Share, template *BlockTemplate) error {
	// Reconstruct the full block header from the winning share.
	header, err := share.FullBlockHeader()
	if err != nil {
		logging.Errorf("Could not construct full block header from share %s: %v", share.Hash.String(), err)
		return err
	}

	// The first transaction is the coinbase from our winning share.
	var coinbaseTx wire.MsgTx
	err = coinbaseTx.Deserialize(bytes.NewReader(share.ShareInfo.ShareData.CoinBase))
	if err != nil {
		return err
	}

	// Create the full block object
	block := wire.NewMsgBlock(header)
	block.AddTransaction(&coinbaseTx)

	// Add all other transactions from the template.
	for _, txTmpl := range template.Transactions {
		txBytes, err := hex.DecodeString(txTmpl.Data)
		if err != nil {
			return err
		}
		var msgTx wire.MsgTx
		err = msgTx.Deserialize(bytes.NewReader(txBytes))
		if err != nil {
			return err
		}
		block.AddTransaction(&msgTx)
	}

	// Submit the block via RPC.
	logging.Infof("Submitting block %s to the network...", block.BlockHash().String())
	err = wm.rpcClient.SubmitBlock(block)
	if err != nil {
		logging.Errorf("Block submission failed: %v", err)
		return err
	}

	logging.Infof("SUCCESS! Block %s accepted by the network!", block.BlockHash().String())
	// In the future, payout logic would be triggered from here.

	return nil
}
