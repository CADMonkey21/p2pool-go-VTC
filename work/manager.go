package work

import (
	"bytes"
	"encoding/binary" // ADDED
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time" // ADDED

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/gertjaap/p2pool-go/config"
	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/rpc"
	"github.com/gertjaap/p2pool-go/util" // ADDED
	p2pwire "github.com/gertjaap/p2pool-go/wire"
)

// PayoutBlock represents a block found by the pool that is awaiting maturity for payout.
type PayoutBlock struct {
	BlockHash          *chainhash.Hash
	BlockFindShareHash *chainhash.Hash // Hash of the share that found the block
	BlockHeight        int32
	IsMature           bool
	IsPaid             bool // Flag to prevent double payouts
}

type WorkManager struct {
	rpcClient     *rpc.Client
	ShareChain    *ShareChain
	Templates     map[string]*BlockTemplate
	PendingBlocks []*PayoutBlock
	TemplateMutex sync.RWMutex
	PendingMutex  sync.Mutex
	NewBlockChan  chan *BlockTemplate
}

func NewWorkManager(rpcClient *rpc.Client, sc *ShareChain) *WorkManager {
	return &WorkManager{
		rpcClient:     rpcClient,
		ShareChain:    sc,
		Templates:     make(map[string]*BlockTemplate),
		PendingBlocks: make([]*PayoutBlock, 0),
		NewBlockChan:  make(chan *BlockTemplate, 10),
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

// WatchMaturedBlocks periodically checks for submitted blocks that have reached
// the required number of confirmations to be considered mature and triggers payouts.
func (wm *WorkManager) WatchMaturedBlocks() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		<-ticker.C
		wm.PendingMutex.Lock()
		if len(wm.PendingBlocks) > 0 {
			logging.Debugf("Checking for matured blocks... (%d pending)", len(wm.PendingBlocks))
		}

		for _, pb := range wm.PendingBlocks {
			if pb.IsMature && pb.IsPaid {
				continue // Already processed and paid
			}

			if !pb.IsMature {
				blockInfo, err := wm.rpcClient.GetBlock(pb.BlockHash)
				if err != nil {
					logging.Warnf("Could not get block info for %s: %v. It may have been orphaned.", pb.BlockHash.String(), err)
					continue
				}

				if blockInfo.Confirmations >= 100 {
					logging.Infof("✅ Block %s at height %d is now MATURE with %d confirmations!", pb.BlockHash.String(), pb.BlockHeight, blockInfo.Confirmations)
					pb.IsMature = true
				} else {
					logging.Debugf("Block %s at height %d is still pending maturity (%d confirmations)", pb.BlockHash.String(), pb.BlockHeight, blockInfo.Confirmations)
				}
			}

			if pb.IsMature && !pb.IsPaid {
				logging.Infof("Triggering PPLNS payout for block %s", pb.BlockHash.String())
				err := wm.ProcessPayout(pb)
				if err != nil {
					logging.Errorf("Payout for block %s FAILED: %v", pb.BlockHash.String(), err)
				} else {
					logging.Infof("Payout for block %s completed successfully.", pb.BlockHash.String())
					pb.IsPaid = true
				}
			}
		}

		wm.PendingMutex.Unlock()
	}
}

// ProcessPayout calculates and distributes rewards for a mature block.
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
		shareTarget := blockchain.CompactToBig(share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			continue
		}
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

		address, err := bech32.Encode("vtc", append([]byte{share.ShareInfo.ShareData.PubKeyHashVersion}, share.ShareInfo.ShareData.PubKeyHash...))
		if err != nil {
			logging.Warnf("Could not re-encode address for pubkeyhash, skipping share for payout: %v", err)
			continue
		}

		shareTarget := blockchain.CompactToBig(share.ShareInfo.Bits)
		if shareTarget.Sign() <= 0 {
			continue
		}
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

	logging.Infof("Successfully sent payout transaction for block %s. TXID: %s", pb.BlockHash.String(), txid)
	return nil
}

// SubmitBlock constructs and submits a full block to the network based on a winning share.
func (wm *WorkManager) SubmitBlock(share *p2pwire.Share, template *BlockTemplate) error {
	// --- START OF FIX ---
	// Manually reconstruct the full block header from the winning share, but crucially,
	// use the high-difficulty NETWORK BITS from the template, not the low-difficulty share bits.
	coinbaseTxHashBytes := DblSha256(share.ShareInfo.ShareData.CoinBase)
	coinbaseTxHash, _ := chainhash.NewHash(coinbaseTxHashBytes)
	merkleRoot := util.ComputeMerkleRootFromLink(coinbaseTxHash, share.MerkleLink.Branch, share.MerkleLink.Index)

	nBitsBytes, err := hex.DecodeString(template.Bits)
	if err != nil {
		return fmt.Errorf("could not decode network bits from template: %v", err)
	}
	nBits := binary.LittleEndian.Uint32(nBitsBytes)

	// Assemble the header with the correct network bits.
	header := &wire.BlockHeader{
		Version:    share.MinHeader.Version,
		PrevBlock:  *share.MinHeader.PreviousBlock,
		MerkleRoot: *merkleRoot,
		Timestamp:  time.Unix(int64(share.MinHeader.Timestamp), 0),
		Bits:       nBits, // Use network bits, not share.MinHeader.Bits
		Nonce:      share.MinHeader.Nonce,
	}
	// --- END OF FIX ---

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

	logging.Infof("Submitting block %s to the network...", block.BlockHash().String())
	err = wm.rpcClient.SubmitBlock(block)
	if err != nil {
		logging.Errorf("Block submission failed: %v", err)
		return err
	}

	logging.Infof("SUCCESS! Block %s accepted by the network! Awaiting maturity.", block.BlockHash().String())

	wm.PendingMutex.Lock()
	blockHash := block.BlockHash()
	wm.PendingBlocks = append(wm.PendingBlocks, &PayoutBlock{
		BlockHash:          &blockHash,
		BlockFindShareHash: share.Hash,
		BlockHeight:        int32(template.Height),
	})
	wm.PendingMutex.Unlock()

	return nil
}
