package work

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/CADMonkey21/p2pool-go-VTC/util"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

type BlockState int

const (
	StatePending BlockState = iota
	StateMature
	StatePaid
	StateOrphan
)

const blocksFile = "blocks.json"

type PayoutBlock struct {
	BlockHash          *chainhash.Hash
	BlockFindShareHash *chainhash.Hash
	BlockHeight        int32
	FoundTime          time.Time
	State              BlockState
}

type persistedBlock struct {
	BlockHash   string     `json:"block_hash"`
	ShareHash   string     `json:"share_hash"`
	BlockHeight int32      `json:"block_height"`
	FoundTime   time.Time  `json:"found_time"`
	State       BlockState `json:"state"`
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
	wm.LoadBlocks()
	return wm
}

// LoadBlocks restores found block history from blocks.json on startup.
func (wm *WorkManager) LoadBlocks() {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()

	data, err := os.ReadFile(blocksFile)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Warnf("Could not read %s: %v", blocksFile, err)
		}
		return
	}

	var diskBlocks []persistedBlock
	if err := json.Unmarshal(data, &diskBlocks); err != nil {
		logging.Warnf("Failed to unmarshal %s: %v", blocksFile, err)
		return
	}

	loaded := make([]*PayoutBlock, 0, len(diskBlocks))
	for _, db := range diskBlocks {
		bh, _ := chainhash.NewHashFromStr(db.BlockHash)
		sh, _ := chainhash.NewHashFromStr(db.ShareHash)
		if bh == nil {
			bh = &chainhash.Hash{}
		}
		if sh == nil {
			sh = &chainhash.Hash{}
		}
		loaded = append(loaded, &PayoutBlock{
			BlockHash:          bh,
			BlockFindShareHash: sh,
			BlockHeight:        db.BlockHeight,
			FoundTime:          db.FoundTime,
			State:              db.State,
		})
	}
	wm.PendingBlocks = loaded
	logging.Infof("Loaded %d found block(s) from %s", len(wm.PendingBlocks), blocksFile)
}

// SaveBlocks writes the current found blocks list to blocks.json.
func (wm *WorkManager) SaveBlocks() {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()
	wm.saveBlocksLocked()
}

func (wm *WorkManager) saveBlocksLocked() {
	diskBlocks := make([]persistedBlock, 0, len(wm.PendingBlocks))
	for _, pb := range wm.PendingBlocks {
		bhStr := ""
		if pb.BlockHash != nil {
			bhStr = pb.BlockHash.String()
		}
		shStr := ""
		if pb.BlockFindShareHash != nil {
			shStr = pb.BlockFindShareHash.String()
		}
		diskBlocks = append(diskBlocks, persistedBlock{
			BlockHash:   bhStr,
			ShareHash:   shStr,
			BlockHeight: pb.BlockHeight,
			FoundTime:   pb.FoundTime,
			State:       pb.State,
		})
	}

	data, err := json.MarshalIndent(diskBlocks, "", "  ")
	if err != nil {
		logging.Errorf("Failed to marshal found blocks: %v", err)
		return
	}
	if err := os.WriteFile(blocksFile, data, 0644); err != nil {
		logging.Errorf("Failed to write %s: %v", blocksFile, err)
	}
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
			if pb.BlockFindShareHash != nil && share.Hash != nil && pb.BlockFindShareHash.IsEqual(share.Hash) {
				isKnown = true
				break
			}
			if pb.BlockHeight == share.ShareInfo.AbsHeight {
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
			wm.saveBlocksLocked()
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

	for {
		<-ticker.C
		var keptBlocks []*PayoutBlock
		changed := false

		wm.PendingMutex.Lock()
		for _, pb := range wm.PendingBlocks {
			keep := true
			switch pb.State {
			case StatePaid, StateOrphan:
				// Keep found blocks for 7 days in history so recent_blocks_table stays populated
				if time.Since(pb.FoundTime) > 7*24*time.Hour {
					keep = false
					changed = true
				}
			case StatePending:
				if pb.BlockHash == nil || pb.BlockHash.IsEqual(&chainhash.Hash{}) {
					pb.State = StatePaid
					changed = true
					break
				}
				blockInfo, err := wm.rpcClient.GetBlock(pb.BlockHash)
				if err != nil {
					if rpcErr, ok := err.(*rpc.RPCError); ok && rpcErr.Code == -5 {
						logging.Warnf("Block %s appears orphaned", pb.BlockHash.String())
						pb.State = StateOrphan
						keep = false
						changed = true
					}
				} else if blockInfo.Confirmations < 0 {
					logging.Warnf("Block %s was orphaned (confirmations: %d)", pb.BlockHash.String(), blockInfo.Confirmations)
					pb.State = StateOrphan
					keep = false
					changed = true
				} else if blockInfo.Confirmations >= requiredConfirmations {
					logging.Successf("Block %s is now MATURE (%d confirmations)! Direct coinbase PPLNS outputs are now spendable.", pb.BlockHash.String(), blockInfo.Confirmations)
					pb.State = StatePaid
					changed = true
				}
			}

			if keep {
				keptBlocks = append(keptBlocks, pb)
			}
		}

		wm.PendingBlocks = keptBlocks
		if changed {
			wm.saveBlocksLocked()
		}
		wm.PendingMutex.Unlock()
	}
}

// buildMsgBlockFromShare constructs the full wire.MsgBlock from a Share and BlockTemplate.
func (wm *WorkManager) buildMsgBlockFromShare(share *Share, template *BlockTemplate) (*wire.MsgBlock, error) {
	var coinbaseTx wire.MsgTx
	if err := coinbaseTx.Deserialize(bytes.NewReader(share.ShareInfo.ShareData.CoinBase)); err != nil {
		return nil, fmt.Errorf("failed to deserialize coinbase tx: %v", err)
	}

	coinbaseTxHash := coinbaseTx.TxHash()
	merkleRoot := util.ComputeMerkleRootFromLink(&coinbaseTxHash, share.MerkleLink.Branch, share.MerkleLink.Index)

	nBits64, err := strconv.ParseUint(template.Bits, 16, 32)
	if err != nil {
		return nil, fmt.Errorf("could not parse network bits from template: %v", err)
	}

	header := &wire.BlockHeader{
		Version:    share.MinHeader.Version,
		PrevBlock:  *share.MinHeader.PreviousBlock,
		MerkleRoot: *merkleRoot,
		Timestamp:  time.Unix(int64(share.MinHeader.Timestamp), 0),
		Bits:       uint32(nBits64),
		Nonce:      share.MinHeader.Nonce,
	}

	block := wire.NewMsgBlock(header)
	block.AddTransaction(&coinbaseTx)

	for _, txTmpl := range template.Transactions {
		txBytes, _ := hex.DecodeString(txTmpl.Data)
		var msgTx wire.MsgTx
		if err := msgTx.Deserialize(bytes.NewReader(txBytes)); err != nil {
			return nil, fmt.Errorf("failed to deserialize template tx: %v", err)
		}
		block.AddTransaction(&msgTx)
	}

	return block, nil
}

// VerifyCandidateBlock asks vertcoind (via getblocktemplate mode="proposal") to verify that the
// assembled block structure, coinbase PPLNS outputs, SegWit commitment, and Merkle root pass 100% of consensus rules.
func (wm *WorkManager) VerifyCandidateBlock(share *Share, template *BlockTemplate) {
	block, err := wm.buildMsgBlockFromShare(share, template)
	if err != nil {
		logging.Errorf("Consensus Check: Failed to build candidate block: %v", err)
		return
	}

	reason, err := wm.rpcClient.ProposeBlock(block)
	if err != nil || reason != "accepted" {
		logging.Errorf("CONSENSUS CHECK FAILED for height %d! vertcoind reason: '%s', err: %v", template.Height, reason, err)
		return
	}

	logging.Successf("CONSENSUS VERIFIED: vertcoind validated full candidate block for height %d (Coinbase, PPLNS Outputs, SegWit Commitment & Merkle Root 100%% valid!)", template.Height)
}

func (wm *WorkManager) SubmitBlock(share *Share, template *BlockTemplate) error {
	block, err := wm.buildMsgBlockFromShare(share, template)
	if err != nil {
		logging.Errorf("SubmitBlock: %v", err)
		return err
	}

	blockHash := block.BlockHash()
	logging.Infof("Submitting solved block %s (Height %d) directly to vertcoind...", blockHash.String(), template.Height)

	if err := wm.rpcClient.SubmitBlock(block); err != nil {
		logging.Errorf("SubmitBlock RPC rejected or failed: %v", err)
		return err
	}

	logging.Successf("SUCCESS! Block %s (Height %d) ACCEPTED by the Vertcoin network!", blockHash.String(), template.Height)

	wm.PendingMutex.Lock()
	wm.PendingBlocks = append(wm.PendingBlocks, &PayoutBlock{
		BlockHash:          &blockHash,
		BlockFindShareHash: share.Hash,
		BlockHeight:        int32(template.Height),
		FoundTime:          time.Now(),
		State:              StatePending,
	})
	wm.saveBlocksLocked()
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
		if pb.FoundTime.After(cutoff) && pb.State != StateOrphan {
			n++
		}
	}
	return n
}

func (wm *WorkManager) GetCurrentBlockReward() float64 {
	latestTemplate := wm.GetLatestTemplate()
	if latestTemplate == nil {
		return 6.25
	}
	return float64(latestTemplate.CoinbaseValue) / 1e8
}

func (wm *WorkManager) GetPersistedBlocksJSON() []byte {
	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()

	diskBlocks := make([]persistedBlock, 0, len(wm.PendingBlocks))
	for _, pb := range wm.PendingBlocks {
		bhStr := ""
		if pb.BlockHash != nil {
			bhStr = pb.BlockHash.String()
		}
		shStr := ""
		if pb.BlockFindShareHash != nil {
			shStr = pb.BlockFindShareHash.String()
		}
		diskBlocks = append(diskBlocks, persistedBlock{
			BlockHash:   bhStr,
			ShareHash:   shStr,
			BlockHeight: pb.BlockHeight,
			FoundTime:   pb.FoundTime,
			State:       pb.State,
		})
	}
	b, _ := json.Marshal(diskBlocks)
	return b
}

func (wm *WorkManager) MergeRemoteBlocksJSON(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	var remoteBlocks []persistedBlock
	if err := json.Unmarshal(data, &remoteBlocks); err != nil {
		return 0
	}

	wm.PendingMutex.Lock()
	defer wm.PendingMutex.Unlock()

	added := 0
	for _, rb := range remoteBlocks {
		exists := false
		for _, pb := range wm.PendingBlocks {
			if pb.BlockHeight == rb.BlockHeight {
				exists = true
				break
			}
		}
		if !exists {
			bh, _ := chainhash.NewHashFromStr(rb.BlockHash)
			sh, _ := chainhash.NewHashFromStr(rb.ShareHash)
			if bh == nil {
				bh = &chainhash.Hash{}
			}
			if sh == nil {
				sh = &chainhash.Hash{}
			}
			wm.PendingBlocks = append(wm.PendingBlocks, &PayoutBlock{
				BlockHash:          bh,
				BlockFindShareHash: sh,
				BlockHeight:        rb.BlockHeight,
				FoundTime:          rb.FoundTime,
				State:              rb.State,
			})
			added++
		}
	}
	if added > 0 {
		wm.saveBlocksLocked()
	}
	return added
}
