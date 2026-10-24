package work

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func segwitAddr(t *testing.T, m bool, ver byte, prog []byte) string {
	d, _ := bech32.ConvertBits(prog, 8, 5, true)
	f := bech32.Encode
	if m {
		f = bech32.EncodeM
	}
	s, err := f("vtc", append([]byte{ver}, d...))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDecodeSegwitAddress(t *testing.T) {
	p20, p32 := bytes.Repeat([]byte{1}, 20), bytes.Repeat([]byte{2}, 32)
	empty, _ := bech32.Encode("vtc", nil)
	for _, tc := range []struct {
		name string
		addr string
		ok   bool
	}{
		{"v0 20", segwitAddr(t, false, 0, p20), true},
		{"v0 32", segwitAddr(t, false, 0, p32), true},
		{"v1 32", segwitAddr(t, true, 1, p32), true},
		{"empty data", empty, false},
		{"v0 bech32m", segwitAddr(t, true, 0, p20), false},
		{"v1 bech32", segwitAddr(t, false, 1, p32), false},
		{"v0 10 bytes", segwitAddr(t, false, 0, p20[:10]), false},
	} {
		if _, _, _, err := DecodeSegwitAddress(tc.addr); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
		if _, err := AddressToScriptPubKey(tc.addr); (err == nil) != tc.ok {
			t.Errorf("%s: AddressToScriptPubKey err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestTaprootPayoutScript(t *testing.T) {
	p32 := bytes.Repeat([]byte{7}, 32)
	addr := segwitAddr(t, true, 1, p32)
	script, err := AddressToScriptPubKey(addr)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{txscript.OP_1, 32}, p32...)
	if !bytes.Equal(script, want) {
		t.Fatalf("script = %x, want %x", script, want)
	}
	v, p := DecodeAddressToPKH(addr)
	sh := &Share{}
	sh.ShareInfo.ShareData.PubKeyHashVersion, sh.ShareInfo.ShareData.PubKeyHash = v, p
	sh.ShareInfo.Timestamp = int32(TaprootPayoutActivationTime)
	if got := DecodeShareAddress(sh); got != addr {
		t.Fatalf("share address after activation = %s, want %s", got, addr)
	}
	// Before activation the share keeps the legacy encoding every deployed node uses.
	old := &Share{}
	old.ShareInfo.ShareData = sh.ShareInfo.ShareData
	old.ShareInfo.Timestamp = int32(TaprootPayoutActivationTime - 1)
	if got := DecodeShareAddress(old); got == addr || got == "" {
		t.Fatalf("share address before activation = %s, want the legacy base58 form", got)
	}
}

func TestIsValidRejectsMalformedShares(t *testing.T) {
	base := func() *Share {
		s := &Share{POWHash: &chainhash.Hash{}, Target: DiffToTarget(1)}
		s.ShareInfo.ShareData.PubKeyHash = make([]byte, 20)
		return s
	}
	wide := base()
	wide.Type = ShareVersionV2
	wide.Target = new(big.Int).Lsh(big.NewInt(1), 300)
	if ok, _ := wide.IsValid(); ok {
		t.Error("share with a target over 256 bits accepted")
	}
	if c := ComputeShareMetadataCommitment(ShareVersionV2, nil, wide.Target, 0, nil, 0, 0, 0, 0); len(c) == 0 {
		t.Error("no commitment for an oversized target")
	}
	for _, n := range []int{0, 10, 60} {
		s := base()
		s.ShareInfo.ShareData.PubKeyHash = make([]byte, n)
		if ok, _ := s.IsValid(); ok {
			t.Errorf("share with a %d-byte witness program accepted", n)
		}
	}
	if ok, r := base().IsValid(); !ok {
		t.Errorf("standard share rejected: %s", r)
	}
}

func TestFullBlockHeaderRejectsMissingHashes(t *testing.T) {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Index: 0xffffffff}, []byte{1, 1}, nil))
	tx.AddTxOut(wire.NewTxOut(1, []byte{txscript.OP_TRUE}))
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	s := &Share{}
	s.ShareInfo.ShareData.CoinBase = buf.Bytes()
	if _, err := s.FullBlockHeader(); err == nil {
		t.Error("share without a previous block hash built a header")
	}
	s.MinHeader.PreviousBlock = &chainhash.Hash{}
	s.MerkleLink.Branch = []*chainhash.Hash{nil}
	if _, err := s.FullBlockHeader(); err == nil {
		t.Error("share with a null merkle branch hash built a header")
	}
	s.MerkleLink.Branch = []*chainhash.Hash{{}}
	if _, err := s.FullBlockHeader(); err != nil {
		t.Errorf("valid share header: %v", err)
	}
}
