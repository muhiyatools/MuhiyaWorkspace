package db

import (
	"testing"

	"gateway/money"
)

func TestNormalizeModelMoneyPrefersExact(t *testing.T) {
	model := Model{InputCostPerMillion: 99, InputCostNanoPerMillion: 300_000_000}
	if err := normalizeModelMoney(&model); err != nil {
		t.Fatal(err)
	}
	if model.InputCostPerMillion != 0.3 {
		t.Fatalf("legacy price=%v", model.InputCostPerMillion)
	}
}

func TestNormalizeTopupMoneyConvertsLegacyCredits(t *testing.T) {
	topup := UserTopup{Credits: 5, UsedCredits: 1.5}
	if err := normalizeTopupMoney(&topup); err != nil {
		t.Fatal(err)
	}
	if topup.AmountNanoUSD != 5*money.PerCredit || topup.UsedNanoUSD != money.NanoUSD(15_000_000) {
		t.Fatalf("exact topup=%d used=%d", topup.AmountNanoUSD, topup.UsedNanoUSD)
	}
}

func TestNormalizeRequestLogMoneyRejectsNegative(t *testing.T) {
	entry := RequestLog{Cost: -1}
	if err := normalizeRequestLogMoney(&entry); err == nil {
		t.Fatal("negative cost accepted")
	}
}
