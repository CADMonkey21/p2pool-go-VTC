package web

import (
	"fmt"
	"math"
	"testing"

	"github.com/CADMonkey21/p2pool-go-VTC/work"
)

// The /api/status difficulty fallback (share chain bits, used before the first
// template) must report the same units as the primary path (template bits).
func TestStatusNetworkDifficultyFallbackMatchesPrimary(t *testing.T) {
	for _, bits := range []uint32{0x1d00ffff, 0x1b0404cb, 0x1c0ffff0, 0x1e0fffff} {
		sc := work.NewShareChain(nil)
		sc.SetNetworkBitsFromTemplate(fmt.Sprintf("%08x", bits))
		primary := statusNetworkDifficulty(sc.GetStats().NetworkDifficulty, 0)
		fallback := statusNetworkDifficulty(0, bits)
		if primary <= 0 {
			t.Fatalf("bits %08x: primary difficulty not set", bits)
		}
		if math.Abs(primary-fallback) > primary*1e-12 {
			t.Errorf("bits %08x: fallback %v != primary %v", bits, fallback, primary)
		}
	}
}

func TestStatusNetworkDifficultyBitcoinUnits(t *testing.T) {
	// 0x1d00ffff is Bitcoin difficulty 1.
	if got := statusNetworkDifficulty(0, 0x1d00ffff); math.Abs(got-1) > 1e-9 {
		t.Errorf("difficulty for 0x1d00ffff = %v, want 1", got)
	}
	if got := statusNetworkDifficulty(0, 0); got != 0 {
		t.Errorf("difficulty with no bits = %v, want 0", got)
	}
	if got := statusNetworkDifficulty(24.8, 0x1d00ffff); got != 24.8 {
		t.Errorf("stats difficulty not preferred: got %v", got)
	}
}
