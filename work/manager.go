package work

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gertjaap/p2pool-go/logging"
	"github.com/gertjaap/p2pool-go/rpc"
)

// WorkManager holds block templates and manages fetching new ones.
type WorkManager struct {
	rpcClient *rpc.Client
	// Capitalized TemplateMutex to make it public (accessible from other packages)
	Templates      map[string]*BlockTemplate
	TemplateMutex  sync.RWMutex
}

// NewWorkManager creates a new manager for handling work templates.
func NewWorkManager(rpcClient *rpc.Client) *WorkManager {
	return &WorkManager{
		rpcClient: rpcClient,
		Templates: make(map[string]*BlockTemplate),
	}
}

// GetTemplate returns a block template by its PreviousBlockHash
func (wm *WorkManager) GetTemplate(prevHash string) (*BlockTemplate, bool) {
	wm.TemplateMutex.RLock()
	defer wm.TemplateMutex.RUnlock()
	template, ok := wm.Templates[prevHash]
	return template, ok
}

// WatchBlockTemplate runs in a loop to continuously fetch the latest block template.
func (wm *WorkManager) WatchBlockTemplate() {
	for {
		params := []interface{}{
			map[string][]string{"rules": {"segwit", "taproot"}},
		}

		resp, err := wm.rpcClient.Call("getblocktemplate", params)
		if err != nil {
			logging.Errorf("Error fetching block template: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		var tmpl BlockTemplate
		err = json.Unmarshal(resp.Result, &tmpl)
		if err != nil {
			logging.Errorf("Error decoding block template: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		wm.TemplateMutex.Lock()
		if _, exists := wm.Templates[tmpl.PreviousBlockHash]; !exists {
			logging.Infof("New block template received for height %d", tmpl.Height)
			wm.Templates[tmpl.PreviousBlockHash] = &tmpl
		}
		wm.TemplateMutex.Unlock()

		time.Sleep(1 * time.Second)
	}
}
