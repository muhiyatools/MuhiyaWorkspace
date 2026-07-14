package proxy

import (
	"math"
	"testing"

	"gateway/db"
)

// All MiniMax values in this test are simulated from the documented pricing
// table; this suite never contacts the live MiniMax API.
func TestMiniMaxTieredCostSimulated(t *testing.T) {
	m3 := &db.Model{
		Name: "minimax-m3", TargetModel: "MiniMax-M3",
		InputCostPerMillion: 0.30, OutputCostPerMillion: 1.20, CacheReadCostPerMillion: 0.06,
	}
	cases := []struct {
		name                  string
		input, output, cached int
		want                  float64
	}{
		{"lower tier", 500000, 100000, 0, 0.27},
		{"boundary stays lower", 512000, 100000, 0, 0.2736},
		{"upper tier", 600000, 100000, 0, 0.60},
		{"lower cached discount", 500000, 100000, 250000, 0.21},
		{"upper cached discount", 600000, 100000, 300000, 0.456},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateCost(m3, tc.input, tc.output, tc.cached, 0)
			if math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("cost = %.12f, want %.12f", got, tc.want)
			}
		})
	}
}

// TestMiniMaxM3VariantNamesStayTiered locks in the audit fix for the pricing
// detector: isMiniMaxM3 prefix-matches like classifyUpstream's family
// detection, so an M3 VARIANT row (e.g. a future minimax-m3-highspeed) still
// bills the >512k tier instead of silently keeping base rates, while M2.x
// rows never match. Simulated pricing only — no live MiniMax traffic.
func TestMiniMaxM3VariantNamesStayTiered(t *testing.T) {
	upperTierInput, output := 600000, 100000
	base := db.Model{InputCostPerMillion: 0.30, OutputCostPerMillion: 1.20, CacheReadCostPerMillion: 0.06}

	tiered := []db.Model{
		{Name: "minimax-m3-highspeed", TargetModel: "MiniMax-M3-HighSpeed"},
		{Name: "minimax-m3-preview", TargetModel: "MiniMax-M3-Preview"},
		{Name: "m3", TargetModel: "MiniMax-M3"},
	}
	for _, model := range tiered {
		model.InputCostPerMillion, model.OutputCostPerMillion, model.CacheReadCostPerMillion = base.InputCostPerMillion, base.OutputCostPerMillion, base.CacheReadCostPerMillion
		got := calculateCost(&model, upperTierInput, output, 0, 0)
		want := 0.60 // 0.6M in × $0.60/M + 0.1M out × $2.40/M
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("%s: cost = %.12f, want tiered %.12f", model.Name, got, want)
		}
	}

	flat := db.Model{Name: "minimax-m2.7-highspeed", TargetModel: "MiniMax-M2.7-HighSpeed",
		InputCostPerMillion: base.InputCostPerMillion, OutputCostPerMillion: base.OutputCostPerMillion, CacheReadCostPerMillion: base.CacheReadCostPerMillion}
	got := calculateCost(&flat, upperTierInput, output, 0, 0)
	want := 0.30 // flat: 0.6M × $0.30/M + 0.1M × $1.20/M
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("M2.x must never take the M3 tier: cost = %.12f, want %.12f", got, want)
	}
}

func TestMiniMaxM2FlatCostSimulated(t *testing.T) {
	m2 := &db.Model{
		Name: "minimax-m2.7", TargetModel: "MiniMax-M2.7",
		InputCostPerMillion: 0.30, OutputCostPerMillion: 1.20, CacheReadCostPerMillion: 0.06,
	}
	got := calculateCost(m2, 800000, 100000, 400000, 0)
	want := 0.264
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("flat M2.7 cost = %.12f, want %.12f", got, want)
	}
}
