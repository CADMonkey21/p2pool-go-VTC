package stratum

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
)

func TestHandleSubmitWithoutParams(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := NewClient(a)
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(b).ReadString('\n')
		got <- line
	}()
	var req JSONRPCRequest
	if err := json.Unmarshal([]byte(`{"id":1,"method":"mining.submit"}`), &req); err != nil {
		t.Fatal(err)
	}
	(&StratumServer{}).handleSubmit(c, &req)
	var resp JSONRPCResponse
	if err := json.Unmarshal([]byte(<-got), &resp); err != nil || resp.Error == nil {
		t.Fatalf("want a malformed submission error, got %+v (%v)", resp, err)
	}
}

func TestIsValidVtcAddressRejectsMalformed(t *testing.T) {
	prog := func(n int) []byte { return make([]byte, n) }
	enc := func(m bool, ver byte, p []byte) string {
		d, _ := bech32.ConvertBits(p, 8, 5, true)
		f := bech32.Encode
		if m {
			f = bech32.EncodeM
		}
		s, _ := f("vtc", append([]byte{ver}, d...))
		return s
	}
	empty, _ := bech32.Encode("vtc", nil)
	for _, tc := range []struct {
		name string
		addr string
		ok   bool
	}{
		{"P2WPKH", enc(false, 0, prog(20)), true},
		{"P2WSH", enc(false, 0, prog(32)), true},
		{"no data part", empty, false},
		{"v0 with bech32m checksum", enc(true, 0, prog(20)), false},
		{"v0 bad program length", enc(false, 0, prog(10)), false},
		{"Taproot before activation", enc(true, 1, prog(32)), false},
		{"P2PKH", base58.CheckEncode(prog(20), 0x47), true},
		{"P2PKH bad length", base58.CheckEncode(prog(32), 0x47), false},
	} {
		if got := isValidVtcAddress(tc.addr); got != tc.ok {
			t.Errorf("%s (%s): valid=%v, want %v", tc.name, tc.addr, got, tc.ok)
		}
	}
}

func TestParseAuthorizeUserAndHint(t *testing.T) {
	tests := []struct {
		user     string
		pass     string
		wantAddr string
		wantHint float64
	}{
		{"vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct.rig1", "x", "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", 0},
		{"vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct+0.58", "x", "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", 0.58},
		{"vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct.rig1+0.42", "x", "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", 0.42},
		{"vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", "d=0.65", "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", 0.65},
		{"vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", "x,diff=0.33", "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", 0.33},
		{"vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", "d=-5", "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct", 0},
	}
	for _, tc := range tests {
		addr, hint := parseAuthorizeUserAndHint(tc.user, tc.pass)
		if addr != tc.wantAddr || hint != tc.wantHint {
			t.Errorf("parseAuthorizeUserAndHint(%q, %q) = (%q, %v), want (%q, %v)",
				tc.user, tc.pass, addr, hint, tc.wantAddr, tc.wantHint)
		}
	}
}

func TestReconnectVardiffCache(t *testing.T) {
	s := &StratumServer{}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := NewClient(a)
	worker := "vtc1q49etcvzld4ra2hatcqg9rwq5d8ddrplgkkzcct"

	s.rememberClientDiff(c, worker, 0.525)
	got, ok := s.lookupCachedDiff(c, worker)
	if !ok || got != 0.525 {
		t.Fatalf("lookupCachedDiff = (%v, %v), want (0.525, true)", got, ok)
	}
}
