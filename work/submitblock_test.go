package work

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/CADMonkey21/p2pool-go-VTC/rpc"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

type fakeReply func(w http.ResponseWriter)

func replyResult(result string) fakeReply {
	return func(w http.ResponseWriter) {
		w.Write([]byte(`{"result":` + result + `,"error":null,"id":1}`))
	}
}

func replyStatus(code int, body string) fakeReply {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		w.Write([]byte(body))
	}
}

func replyDrop(w http.ResponseWriter) {
	conn, _, _ := w.(http.Hijacker).Hijack()
	conn.Close()
}

// fakeVertcoind serves the given replies to submitblock calls in order, repeating the last one.
func fakeVertcoind(t *testing.T, replies ...fakeReply) (*rpc.Client, func() int) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reply := replies[min(calls, len(replies)-1)]
		calls++
		mu.Unlock()
		reply(w)
	}))
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	client := rpc.NewClient(config.Config{RPCHost: host, RPCPort: p, RPCUser: "u", RPCPass: "p"})
	return client, func() int { mu.Lock(); defer mu.Unlock(); return calls }
}

func testSolvedShare(t *testing.T, tmplTxs int) (*Share, *BlockTemplate) {
	t.Chdir(t.TempDir())
	old := submitBlockBackoff
	submitBlockBackoff = []time.Duration{0, 0, 0}
	t.Cleanup(func() { submitBlockBackoff = old })

	cb := wire.NewMsgTx(1)
	cb.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{}, 0xffffffff), []byte{0x01, 0x02}, nil))
	cb.AddTxOut(wire.NewTxOut(5000, []byte{0x51}))
	var buf bytes.Buffer
	cb.Serialize(&buf)

	tmpl := &BlockTemplate{Bits: "1d00ffff", Height: 100, PreviousBlockHash: testHash(9, 0).String()}
	for i := 0; i < tmplTxs; i++ {
		tx := wire.NewMsgTx(1)
		tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(testHash(7, i), 0), nil, nil))
		tx.AddTxOut(wire.NewTxOut(1000, []byte{0x51}))
		var txBuf bytes.Buffer
		tx.Serialize(&txBuf)
		tmpl.Transactions = append(tmpl.Transactions, BlockTemplateTx{Data: hex.EncodeToString(txBuf.Bytes())})
	}

	share := &Share{Hash: testHash(8, 0)}
	share.MinHeader = SmallBlockHeader{Version: 0x20000000, PreviousBlock: testHash(9, 0), Timestamp: uint32(time.Now().Unix())}
	share.ShareInfo.ShareData.CoinBase = buf.Bytes()
	return share, tmpl
}

func newTestWorkManager(client *rpc.Client) *WorkManager {
	return &WorkManager{rpcClient: client, Templates: make(map[string]*BlockTemplate), ForceNewTemplate: make(chan bool, 1)}
}

func TestSubmitBlockRetriesTransientErrors(t *testing.T) {
	share, tmpl := testSolvedShare(t, 0)
	client, calls := fakeVertcoind(t, replyDrop, replyStatus(503, "Work queue depth exceeded"),
		replyStatus(500, `{"result":null,"error":{"code":-28,"message":"Loading block index..."},"id":1}`), replyResult("null"))
	wm := newTestWorkManager(client)
	if err := wm.SubmitBlock(share, tmpl); err != nil {
		t.Fatalf("SubmitBlock: %v", err)
	}
	if calls() != 4 {
		t.Fatalf("calls = %d, want 4", calls())
	}
	if len(wm.PendingBlocks) != 1 {
		t.Fatalf("PendingBlocks = %d, want 1", len(wm.PendingBlocks))
	}
}

func TestSubmitBlockGivesUpAfterRetries(t *testing.T) {
	share, tmpl := testSolvedShare(t, 0)
	client, calls := fakeVertcoind(t, replyStatus(503, "Work queue depth exceeded"))
	wm := newTestWorkManager(client)
	var httpErr *rpc.HTTPError
	if err := wm.SubmitBlock(share, tmpl); !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want HTTPError", err)
	}
	if calls() != 4 {
		t.Fatalf("calls = %d, want 4", calls())
	}
	if len(wm.PendingBlocks) != 0 {
		t.Fatalf("PendingBlocks = %d, want 0", len(wm.PendingBlocks))
	}
}

func TestSubmitBlockRejectionNotRetried(t *testing.T) {
	for _, reply := range []fakeReply{
		replyResult(`"bad-txnmrklroot"`),
		replyResult(`"inconclusive"`),
		replyStatus(500, `{"result":null,"error":{"code":-22,"message":"Block decode failed"},"id":1}`),
	} {
		share, tmpl := testSolvedShare(t, 0)
		client, calls := fakeVertcoind(t, reply)
		wm := newTestWorkManager(client)
		if err := wm.SubmitBlock(share, tmpl); err == nil {
			t.Fatal("SubmitBlock succeeded, want rejection")
		}
		if calls() != 1 {
			t.Fatalf("calls = %d, want 1", calls())
		}
		if len(wm.PendingBlocks) != 0 {
			t.Fatalf("PendingBlocks = %d, want 0", len(wm.PendingBlocks))
		}
	}
}

func TestSubmitBlockDuplicateIsAccepted(t *testing.T) {
	for _, replies := range [][]fakeReply{
		{replyResult(`"duplicate"`)},
		{replyDrop, replyResult(`"duplicate"`)},
	} {
		share, tmpl := testSolvedShare(t, 0)
		client, calls := fakeVertcoind(t, replies...)
		wm := newTestWorkManager(client)
		wm.Templates[tmpl.PreviousBlockHash] = tmpl
		if err := wm.SubmitBlock(share, tmpl); err != nil {
			t.Fatalf("SubmitBlock: %v", err)
		}
		if calls() != len(replies) {
			t.Fatalf("calls = %d, want %d", calls(), len(replies))
		}
		if len(wm.PendingBlocks) != 1 {
			t.Fatalf("PendingBlocks = %d, want 1", len(wm.PendingBlocks))
		}
		if _, ok := wm.Templates[tmpl.PreviousBlockHash]; ok {
			t.Fatal("template not deleted")
		}
		if len(wm.ForceNewTemplate) != 1 {
			t.Fatal("ForceNewTemplate not signalled")
		}
	}
}

func TestSubmitBlockRefusesMerkleMismatch(t *testing.T) {
	share, tmpl := testSolvedShare(t, 1)
	client, calls := fakeVertcoind(t, replyResult("null"))
	wm := newTestWorkManager(client)
	relayed := false
	wm.RelayBlockHexFunc = func(string) int { relayed = true; return 0 }
	if err := wm.SubmitBlock(share, tmpl); err == nil {
		t.Fatal("SubmitBlock succeeded with a mismatched merkle root")
	}
	if calls() != 0 || relayed {
		t.Fatalf("calls = %d, relayed = %v; want neither", calls(), relayed)
	}
	if len(wm.PendingBlocks) != 0 {
		t.Fatalf("PendingBlocks = %d, want 0", len(wm.PendingBlocks))
	}
}
