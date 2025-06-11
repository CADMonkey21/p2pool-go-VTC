package work

// import "encoding/json" // This line has been removed

// BlockTemplate represents the data returned by the getblocktemplate RPC call.
// We only define the fields we care about for now.
type BlockTemplate struct {
	Capabilities      []string          `json:"capabilities"`
	Version           uint32            `json:"version"`
	PreviousBlockHash string            `json:"previousblockhash"`
	Transactions      []BlockTemplateTx `json:"transactions"`
	CoinbaseAux       struct {
		Flags string `json:"flags"`
	} `json:"coinbaseaux"`
	CoinbaseValue int64    `json:"coinbasevalue"`
	LongPollID    string   `json:"longpollid"`
	Target        string   `json:"target"`
	MinTime       int64    `json:"mintime"`
	Mutable       []string `json:"mutable"`
	NonceRange    string   `json:"noncerange"`
	SigOpLimit    int      `json:"sigoplimit"`
	SizeLimit     int      `json:"sizelimit"`
	WeightLimit   int      `json:"weightlimit"`
	CurTime       int64    `json:"curtime"`
	Bits          string   `json:"bits"`
	Height        int64    `json:"height"`
}

// BlockTemplateTx represents a single transaction in the block template.
type BlockTemplateTx struct {
	Data    string `json:"data"`
	TxID    string `json:"txid"`
	Hash    string `json:"hash"`
	Depends []int  `json:"depends"`
	Fee     int    `json:"fee"`
	SigOps  int    `json:"sigops"`
	Weight  int    `json:"weight"`
}
