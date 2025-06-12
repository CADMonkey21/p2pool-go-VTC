package stratum

import (
	"github.com/gertjaap/p2pool-go/work"
)

// Job represents a unit of work sent to a miner.
type Job struct {
	ID              string
	BlockTemplate   *work.BlockTemplate
	ExtraNonce1     string
	// In the future, this would also store the target difficulty for this job
}
