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

	// "github.com/btcsuite/btcd/blockchain" // [REMOVED] This import is no longer used
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/CADMonkey21/p2pool-go-VTC/util"
	// "github.com/CADMonkey21/p2pool-go-VTC/verthash" // Import removed, no longer needed
	p2pwire "github.com/CADMonkey21/p2pool-go-VTC/wire"
)

// BlockState defines the status of a block in the payout processor.
type BlockState int

const (
	StatePending BlockState = iota // Newly found, awaiting maturity
	StateMature                    // 100+ confirmations, ready for payout
	StatePaid                      // Payout transaction sent
	StateOrphan                    // Confirmed to not be on the main chain
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
	// verthasher         verthash.Verthasher // [REMOVED] This field was redundant
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

	// [REMOVED] The verthasher initialization here was redundant
	// and is already handled in main.go.
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
		wm.Templates = make(map[string]*BlockTemplate) // Clear old templates
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

	wm.fetchBlockTemplate() // Initial fetch

	for {
		select {
		case <-ticker.C:
			wm.fetchBlockTemplate()
		case <-wm.ForceNewTemplate:
			logging.Debugf("Forcing immediate template refresh.")
			time.Sleep(250 * time.Millisecond) // Give daemon a moment to process the new block
			wm.fetchBlockTemplate()
		}
	}
}

func (wm *WorkManager) WatchMaturedBlocks() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		<-ticker.C
		var keptBlocks []*PayoutBlock

		wm.PendingMutex.Lock()
		if len(wm.PendingBlocks) > 0 {
			logging.Debugf("Checking for matured blocks... (%d pending)", len(wm.PendingBlocks))
		}

		for _, pb := range wm.PendingBlocks {
			keep := true
			switch pb.State {
			case StatePaid, StateOrphan:
				keep = false
			case StatePending:
				blockInfo, err := wm.rpcClient.GetBlock(pb.BlockHash)
				if err != nil {
					if rpcErr, ok := err.(*rpc.RPCError); ok && rpcErr.Code == -5 {
						logging.Warnf("Block %s appears orphaned (not found), will not check again.", pb.BlockHash.String())
						pb.State = StateOrphan
						keep = false
					} else {
						logging.Warnf("Could not get block info for %s: %v", pb.BlockHash.String(), err)
					}
				} else if blockInfo.Confirmations >= 100 {
					logging.Successf("✅ Block %s at height %d is now MATURE with %d confirmations!", pb.BlockHash.String(), pb.BlockHeight, blockInfo.Confirmations)
					pb.State = StateMature
				} else {
					logging.Debugf("Block %s at height %d is still pending maturity (%d confirmations)", pb.BlockHash.String(), pb.BlockHeight, blockInfo.Confirmations)
				}
			case StateMature:
				logging.Infof("Triggering PPLNS payout for block %s", pb.BlockHash.String())
				err := wm.ProcessPayout(pb)
				if err != nil {
					logging.Errorf("Payout for block %s FAILED: %v", pb.BlockHash.String(), err)
				} else {
					logging.Successf("Payout for block %s completed successfully.", pb.BlockHash.String())
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
		return fmt.Errorf("could not find the block-finding share %s in sharechain", pb.BlockFindShareHash.String())
	}
	totalPayout := blockFindShare.ShareInfo.ShareData.Subsidy

	feePercentage := config.Active.Fee / 100.0
	poolFee := uint64(float64(totalPayout) * feePercentage)
	amountToDistribute := totalPayout - poolFee

	windowSize := config.Active.PPLNSWindow
	if windowSize <= 0 {
		return fmt.Errorf("PPLNS window size is not configured or is invalid")
	}
	payoutShares, err := wm.ShareChain.GetSharesForPayout(pb.BlockFindShareHash, windowSize)
	if err != nil {
		return fmt.Errorf("could not get PPLNS window shares: %w", err)
	}

	payouts := make(map[string]uint64)
	totalWorkInWindow := new(big.Int)
	maxTarget, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", 16)

	for _, share := range payoutShares {
		// [PAYOUT BUG FIX] Use the share's Stratum target, not the network's target
		shareTarget := share.Target
		if shareTarget == nil || shareTarget.Sign() <= 0 {
			continue
		}
		// [PAYOUT BUG FIX] Calculate work based on the standard maxWork
		difficulty := new(big.Int).Div(new(big.Int).Set(maxTarget), shareTarget)
		totalWorkInWindow.Add(totalWorkInWindow, difficulty)
	}

	if totalWorkInWindow.Sign() <= 0 {
		return fmt.Errorf("total work in PPLNS window is zero, cannot distribute payment")
	}

	for _, share := range payoutShares {
		if share.ShareInfo.ShareData.PubKeyHash == nil || len(share.ShareInfo.ShareData.PubKeyHash) == 0 {
			continue
		}

		var address string
		var err error
		if share.ShareInfo.ShareData.PubKeyHashVersion == 0x00 { // Bech32 P2WPKH or P2WSH
			converted, err := bech32.ConvertBits(share.ShareInfo.ShareData.PubKeyHash, 8, 5, true)
			if err != nil {
				logging.Warnf("Could not convert bits for bech32 payout: %v", err)
				continue
			}
			// Use the correct HRP for the active network
			hrp := "vtc"
			if config.Active.Testnet {
				hrp = "tvtc"
			}
			address, err = bech32.Encode(hrp, append([]byte{share.ShareInfo.ShareData.PubKeyHashVersion}, converted...))
		} else { // Legacy Base58
			address = base58.CheckEncode(share.ShareInfo.ShareData.PubKeyHash, share.ShareInfo.ShareData.PubKeyHashVersion)
		}

		if err != nil {
			logging.Warnf("Could not re-encode address for pubkeyhash, skipping share for payout: %v", err)
			continue
		}

		// [PAYOUT BUG FIX] Use the share's Stratum target, not the network's target
		shareTarget := share.Target
		if shareTarget == nil || shareTarget.Sign() <= 0 {
			continue
		}
		// [PAYOUT BUG FIX] Calculate work based on the standard maxWork
		difficulty := new(big.Int).Div(new(big.Int).Set(maxTarget), shareTarget)

		payoutAmount := new(big.Int).Mul(big.NewInt(int64(amountToDistribute)), difficulty)
		payoutAmount.Div(payoutAmount, totalWorkInWindow)

		payouts[address] += payoutAmount.Uint64()
	}

	payouts[config.Active.PoolAddress] += poolFee

	sendManyMap := make(map[string]float64)
	for address, amountSatoshis := range payouts {
		if amountSatoshis > 0 {
			sendManyMap[address] = float64(amountSatoshis) / 100000000.0
		}
	}

	if len(sendManyMap) == 0 {
		logging.Warnf("Payout map is empty for block %s. This could happen if no valid shares were in the PPLNS window.", pb.BlockHash.String())
		return nil
	}

	txid, err := wm.rpcClient.SendMany(sendManyMap)
	if err != nil {
		return fmt.Errorf("sendmany RPC call failed: %w", err)
	}

	logging.Successf("Successfully sent payout transaction for block %s. TXID: %s", pb.BlockHash.String(), txid)
	return nil
}

func (wm *WorkManager) SubmitBlock(share *p2pwire.Share, template *BlockTemplate) error {
	coinbaseTxHashBytes := DblSha256(share.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)
	merkleRoot := util.ComputeMerkleRootFromLink(coinbaseTxHash, share.MerkleLink.Branch, share.MerkleLink.Index)

	nBits64, err := strconv.ParseUint(template.Bits, 16, 32)
	if err != nil {
		return fmt.Errorf("could not parse network bits from template: %v", err)
	}
	nBits := uint32(nBits64)

	header := &wire.BlockHeader{
		Version:    share.MinHeader.Version,
		PrevBlock:  *share.MinHeader.PreviousBlock,
		MerkleRoot: *merkleRoot,
		Timestamp:  time.Unix(int64(share.MinHeader.Timestamp), 0),
		Bits:       nBits,
		Nonce:      share.MinHeader.Nonce,
	}

	var coinbaseTx wire.MsgTx
	err = coinbaseTx.Deserialize(bytes.NewReader(share.ShareInfo.ShareData.CoinBase))
	if err != nil {
		return err
	}

	block := wire.NewMsgBlock(header)
	block.AddTransaction(&coinbaseTx)

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

	logging.Infof("Proposing block %s for validation...", block.BlockHash().String())
	reason, err := wm.rpcClient.ProposeBlock(block)
	if err != nil {
		logging.Errorf("Block proposal failed: %v", err)
		return err
	}
	if reason != "accepted" {
		logging.Errorf("Block proposal REJECTED, reason: %s", reason)
		return fmt.Errorf("block rejected by daemon: %s", reason)
	}
	logging.Successf("Block proposal accepted! Submitting to the network...")

	err = wm.rpcClient.SubmitBlock(block)
	if err != nil {
		logging.Errorf("Block submission failed: %v", err)
		return err
	}

	logging.Successf("SUCCESS! Block %s accepted by the network! Awaiting maturity.", block.BlockHash().String())

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

	select {
	case wm.ForceNewTemplate <- true:
	default:
	}

	return nil
}

func (wm *WorkManager) GetRecentBlocks(count int) ([]PayoutBlock, error) {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()

	sort.Slice(wm.PendingBlocks, func(i, j int) bool {
		return wm.PendingBlocks[i].BlockHeight > wm.PendingBlocks[j].BlockHeight
	})

	if count > len(wm.PendingBlocks) {
		count = len(wm.PendingBlocks)
	}

	result := make([]PayoutBlock, count)
	for i := 0; i < count; i++ {
		result[i] = *wm.PendingBlocks[i]
	}

	return result, nil
}

func (wm *WorkManager) GetLastBlockFoundTime() time.Time {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()
	var lastTime time.Time
	for _, b := range wm.PendingBlocks {
		if b.FoundTime.After(lastTime) {
			lastTime = b.FoundTime
		}
	}
	return lastTime
}

func (wm *WorkManager) GetBlocksFoundInLast(window time.Duration) int {
	cutoff := time.Now().Add(-window)

	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()

	n := 0
	for _, pb := range wm.PendingBlocks {
		if pb.FoundTime.After(cutoff) {
			n++
		}
	}
	return n
}
