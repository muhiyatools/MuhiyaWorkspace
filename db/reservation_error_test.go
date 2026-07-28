package db

import (
	"strings"
	"testing"

	"gateway/money"
)

func TestBudgetExceededExplainsInFlightAuthorization(t *testing.T) {
	err := (&BudgetExceededError{
		Requested: money.NanoUSD(20_000_000),
		Available: money.NanoUSD(5_000_000),
		Pending:   money.NanoUSD(30_000_000),
	}).Error()
	if !strings.Contains(err, "temporarily authorized by in-flight requests") {
		t.Fatalf("pending authorization was hidden from the caller: %q", err)
	}
}
