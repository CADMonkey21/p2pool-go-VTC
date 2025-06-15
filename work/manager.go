package work

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/rpc"
)

type WorkManager struct {
	rpcClient      *rpc.Client
	ShareChain     *ShareChain
	Templates      map[string]*BlockTemplate
	TemplateMutex  sync.RWMutex
	NewBlockChan   chan *BlockTemplate
}

func NewWorkManager(rpcClient *rpc.Client, sc *ShareChain) *WorkManager {
	return &WorkManager{
		rpcClient:      rpcClient,
		ShareChain:     sc,
		Templates:      make(map[string]*BlockTemplate),
		NewBlockChan:   make(chan *BlockTemplate, 10),
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
