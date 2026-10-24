package config

import (
	"reflect"
	"testing"
)

func TestParsePeerList(t *testing.T) {
	const a = "/ip4/138.68.53.157/tcp/9348/p2p/12D3KooWNiQ2TSXEhFgZrsJuuk9xxNheph8b23pK4RHaPSQFNDwU"
	const b = "/dns4/p2poolvtc.com/tcp/9348/p2p/12D3KooWL65LCCkfLnN2PaEjdXUm4BBHj24nzpABPhDatjNzPKwi"

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"single", a, []string{a}},
		{"multiple with spaces", " " + a + " , " + b + " ", []string{a, b}},
		{"empty entries dropped", "," + a + ",,", []string{a}},
		{"blank", "  ,  ", []string{}},
		{"invalid multiaddr skipped", "not-a-multiaddr," + b, []string{b}},
		{"missing peer id skipped", "/ip4/1.2.3.4/tcp/9348," + a, []string{a}},
		{"bad peer id skipped", "/ip4/1.2.3.4/tcp/9348/p2p/notapeerid," + a, []string{a}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePeerList(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePeerList(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
