package work

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/CADMonkey21/p2pool-go-VTC/util"
)

type BlockState int

const (
	StatePending BlockState = iota 
	StateMature                    
	StatePaid                      
	StateOrphan                    
)

type PayoutBlock struct {
	BlockHash          *chainhash.Hash
	BlockFindShareHash *chainhash.Hash
	BlockHeight        int32
	FoundTime          time.Time
	State              BlockState
}

type WorkManager struct {
	rpcClient          *rpc.Client
	ShareChain         *ShareChain
	Templates          map[string]*BlockTemplate
	PendingBlocks      []*PayoutBlock
	TemplateMutex      sync.RWMutex
	PendingMutex       sync.Mutex
	NewBlockChan       chan *BlockTemplate
	ForceNewTemplate   chan bool
	lastBlockUpdate    time.Time
	lastBlockUpdateMux sync.RWMutex
}

func NewWorkManager(rpcClient *rpc.Client, sc *ShareChain) *WorkManager {
	wm := &WorkManager{
		rpcClient:        rpcClient,
		ShareChain:       sc,
		Templates:        make(map[string]*BlockTemplate),
		PendingBlocks:    make([]*PayoutBlock, 0),
		NewBlockChan:     make(chan *BlockTemplate, 10),
		ForceNewTemplate: make(chan bool, 1),
	}
	return wm
}

func (wm *WorkManager) IsSynced() bool {
	wm.lastBlockUpdateMux.RLock()
	defer wm.lastBlockUpdateMux.RUnlock()
	return time.Since(wm.lastBlockUpdate) < 5*time.Minute
}

func (wm *WorkManager) WatchFoundBlocks() {
	for share := range wm.ShareChain.FoundBlockChan {
		wm.PendingMutex.Lock()
		isKnown := false
		for _, pb := range wm.PendingBlocks {
			if pb.BlockFindShareHash.IsEqual(share.Hash) {
				isKnown = true
				break
			}
		}

		if !isKnown {
			header, err := share.FullBlockHeader()
			if err != nil {
				logging.Warnf("Could not get header from block-finding share %s", share.Hash)
				wm.PendingMutex.Unlock()
				continue
			}
			blockHash := header.BlockHash()

			logging.Infof("Adding block found by peer to pending list. Share: %s, Block: %s", share.Hash, blockHash)
			wm.PendingBlocks = append(wm.PendingBlocks, &PayoutBlock{
				BlockHash:          &blockHash,
				BlockFindShareHash: share.Hash,
				BlockHeight:        share.ShareInfo.AbsHeight,
				FoundTime:          time.Unix(int64(share.ShareInfo.Timestamp), 0),
				State:              StatePending,
			})
		}
		wm.PendingMutex.Unlock()
	}
}

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

func (wm *WorkManager) fetchBlockTemplate() {
	rawTemplate, err := wm.rpcClient.GetBlockTemplate()
	if err != nil {
		logging.Errorf("Error getting block template: %v", err)
		return
	}

	var tmpl BlockTemplate
	err = json.Unmarshal(rawTemplate, &tmpl)
	if err != nil {
		logging.Errorf("Error decoding block template: %v", err)
		return
	}

	wm.TemplateMutex.Lock()
	if _, exists := wm.Templates[tmpl.PreviousBlockHash]; !exists {
		logging.Infof("New block template received for height %d", tmpl.Height)
		wm.Templates = make(map[string]*BlockTemplate) 
		wm.Templates[tmpl.PreviousBlockHash] = &tmpl

		wm.lastBlockUpdateMux.Lock()
		wm.lastBlockUpdate = time.Now()
		wm.lastBlockUpdateMux.Unlock()

		select {
		case wm.NewBlockChan <- &tmpl:
		default:
			logging.Warnf("NewBlockChan is full, dropping new template notification.")
		}
	}
	wm.TemplateMutex.Unlock()
}

func (wm *WorkManager) WatchBlockTemplate() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	wm.fetchBlockTemplate() 

	for {
		select {
		case <-ticker.C:
			wm.fetchBlockTemplate()
		case <-wm.ForceNewTemplate:
			logging.Debugf("Forcing immediate template refresh.")
			time.Sleep(250 * time.Millisecond) 
			wm.fetchBlockTemplate()
		}
	}
}

func (wm *WorkManager) WatchMaturedBlocks() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	var requiredConfirmations int64 = 100
	if config.Active.Testnet {
		requiredConfirmations = 2
	}

	for {
		<-ticker.C
		var keptBlocks []*PayoutBlock

		wm.PendingMutex.Lock()
		for _, pb := range wm.PendingBlocks {
			keep := true
			switch pb.State {
			case StatePaid, StateOrphan:
				keep = false
			case StatePending:
				blockInfo, err := wm.rpcClient.GetBlock(pb.BlockHash)
				if err != nil {
					if rpcErr, ok := err.(*rpc.RPCError); ok && rpcErr.Code == -5 {
						logging.Warnf("Block %s appears orphaned", pb.BlockHash.String())
						pb.State = StateOrphan
						keep = false
					}
				} else if blockInfo.Confirmations >= requiredConfirmations {
					logging.Successf("✅ Block %s is now MATURE!", pb.BlockHash.String())
					pb.State = StateMature
				}
			case StateMature:
				err := wm.ProcessPayout(pb)
				if err != nil {
					logging.Errorf("Payout for block %s FAILED: %v", pb.BlockHash.String(), err)
				} else {
					logging.Successf("Payout for block %s completed.", pb.BlockHash.String())
					pb.State = StatePaid
					keep = false
				}
			}

			if keep {
				keptBlocks = append(keptBlocks, pb)
			}
		}

		wm.PendingBlocks = keptBlocks
		wm.PendingMutex.Unlock()
	}
}

func (wm *WorkManager) ProcessPayout(pb *PayoutBlock) error {
	blockFindShare := wm.ShareChain.GetShare(pb.BlockFindShareHash.String())
	if blockFindShare == nil {
		return fmt.Errorf("could not find share %s in sharechain", pb.BlockFindShareHash.String())
	}
	totalPayout := blockFindShare.ShareInfo.ShareData.Subsidy
	feePercentage := config.Active.Fee / 100.0
	poolFee := uint64(float64(totalPayout) * feePercentage)
	amountToDistribute := totalPayout - poolFee

	windowSize := config.Active.PPLNSWindow
	payoutShares, err := wm.ShareChain.GetSharesForPayout(pb.BlockFindShareHash, windowSize)
	if err != nil {
		return fmt.Errorf("could not get PPLNS window shares: %w", err)
	}

	payouts := make(map[string]uint64)
	totalWorkInWindow := new(big.Int)
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	for _, share := range payoutShares {
		if share.Target == nil || share.Target.Sign() <= 0 {
			continue
		}
		difficulty := new(big.Int).Div(new(big.Int).Set(maxTarget), share.Target)
		totalWorkInWindow.Add(totalWorkInWindow, difficulty)
	}

	if totalWorkInWindow.Sign() <= 0 {
		return fmt.Errorf("total work is zero")
	}

	for _, share := range payoutShares {
		if len(share.ShareInfo.ShareData.PubKeyHash) == 0 {
			continue
		}

		var address string
		if share.ShareInfo.ShareData.PubKeyHashVersion == 0x00 { 
			converted, err := bech32.ConvertBits(share.ShareInfo.ShareData.PubKeyHash, 8, 5, true)
			if err != nil { continue }
			hrp := "vtc"
			if config.Active.Testnet { hrp = "tvtc" }
			address, _ = bech32.Encode(hrp, append([]byte{share.ShareInfo.ShareData.PubKeyHashVersion}, converted...))
		} else {
			address = base58.CheckEncode(share.ShareInfo.ShareData.PubKeyHash, share.ShareInfo.ShareData.PubKeyHashVersion)
		}

		if share.Target == nil || share.Target.Sign() <= 0 { continue }
		difficulty := new(big.Int).Div(new(big.Int).Set(maxTarget), share.Target)
		payoutAmount := new(big.Int).Mul(big.NewInt(int64(amountToDistribute)), difficulty)
		payoutAmount.Div(payoutAmount, totalWorkInWindow)
		payouts[address] += payoutAmount.Uint64()
	}

	payouts[config.Active.PoolAddress] += poolFee
	sendManyMap := make(map[string]float64)
	for addr, amt := range payouts {
		if amt > 0 { sendManyMap[addr] = float64(amt) / 100000000.0 }
	}

	if len(sendManyMap) == 0 { return nil }
	_, err = wm.rpcClient.SendMany(sendManyMap)
	return err
}

// Submitting a Block now references the local 'Share' type instead of wire protocol
func (wm *WorkManager) SubmitBlock(share *Share, template *BlockTemplate) error {
	coinbaseTxHashBytes := DblSha256(share.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)
	merkleRoot := util.ComputeMerkleRootFromLink(coinbaseTxHash, share.MerkleLink.Branch, share.MerkleLink.Index)

	nBits64, err := strconv.ParseUint(template.Bits, 16, 32)
	if err != nil {
		return fmt.Errorf("could not parse network bits from template: %v", err)
	}

	header := &wire.BlockHeader{
		Version:    share.MinHeader.Version,
		PrevBlock:  *share.MinHeader.PreviousBlock,
		MerkleRoot: *merkleRoot,
		Timestamp:  time.Unix(int64(share.MinHeader.Timestamp), 0),
		Bits:       uint32(nBits64),
		Nonce:      share.MinHeader.Nonce,
	}

	var coinbaseTx wire.MsgTx
	err = coinbaseTx.Deserialize(bytes.NewReader(share.ShareInfo.ShareData.CoinBase))
	if err != nil { return err }

	block := wire.NewMsgBlock(header)
	block.AddTransaction(&coinbaseTx)

	for _, txTmpl := range template.Transactions {
		txBytes, _ := hex.DecodeString(txTmpl.Data)
		var msgTx wire.MsgTx
		_ = msgTx.Deserialize(bytes.NewReader(txBytes))
		block.AddTransaction(&msgTx)
	}

	logging.Infof("Proposing block %s for validation...", block.BlockHash().String())
	reason, err := wm.rpcClient.ProposeBlock(block)
	if err != nil || reason != "accepted" {
		return fmt.Errorf("block rejected by daemon: %s", reason)
	}

	err = wm.rpcClient.SubmitBlock(block)
	if err != nil { return err }

	logging.Successf("SUCCESS! Block %s accepted by the network!", block.BlockHash().String())

	wm.PendingMutex.Lock()
	blockHash := block.BlockHash()
	wm.PendingBlocks = append(wm.PendingBlocks, &PayoutBlock{
		BlockHash:          &blockHash,
		BlockFindShareHash: share.Hash,
		BlockHeight:        int32(template.Height),
		FoundTime:          time.Now(),
		State:              StatePending,
	})
	wm.PendingMutex.Unlock()

	wm.TemplateMutex.Lock()
	delete(wm.Templates, template.PreviousBlockHash)
	wm.TemplateMutex.Unlock()

	select { case wm.ForceNewTemplate <- true: default: }
	return nil
}

func (wm *WorkManager) GetRecentBlocks(count int) ([]PayoutBlock, error) {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()

	sort.Slice(wm.PendingBlocks, func(i, j int) bool {
		return wm.PendingBlocks[i].BlockHeight > wm.PendingBlocks[j].BlockHeight
	})

	if count > len(wm.PendingBlocks) { count = len(wm.PendingBlocks) }
	result := make([]PayoutBlock, count)
	for i := 0; i < count; i++ { result[i] = *wm.PendingBlocks[i] }
	return result, nil
}

func (wm *WorkManager) GetLastBlockFoundTime() time.Time {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()
	var lastTime time.Time
	for _, b := range wm.PendingBlocks {
		if b.FoundTime.After(lastTime) { lastTime = b.FoundTime }
	}
	return lastTime
}

func (wm *WorkManager) GetBlocksFoundInLast(window time.Duration) int {
	cutoff := time.Now().Add(-window)
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()
	n := 0
	for _, pb := range wm.PendingBlocks {
		if pb.FoundTime.After(cutoff) { n++ }
	}
	return n
}
