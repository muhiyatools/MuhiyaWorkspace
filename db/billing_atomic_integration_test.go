package db

import (
	"context"
	"testing"
	"time"

	"gateway/money"
	"github.com/google/uuid"
)

func TestSettleUsageIsAtomicIdempotentAndBudgetBounded(t *testing.T) {
	database := openTestDB(t)
	defer database.Close()

	suffix := uuid.NewString()[:8]
	planID := "plan-settle-" + suffix
	userID := "user-settle-" + suffix
	keyID := "key-settle-" + suffix
	if err := database.CreatePlan(Plan{
		ID: planID, Name: "Settlement Test", RPMLimit: 100, TPMLimit: 1_000_000,
		BudgetWindows: []BudgetWindow{{
			ID: "window-settle-" + suffix, Name: "daily", DurationSeconds: 24 * 60 * 60,
			BudgetNanoUSD: 50_000_000,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(User{
		ID: userID, Name: "Settlement User", Email: suffix + "@test.invalid",
		PlanID: planID, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateVirtualKey(VirtualKey{
		ID: keyID, Name: "Settlement Key", UserID: userID, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.conn.Exec("DELETE FROM account_ledger WHERE user_id = $1", userID)
		_, _ = database.conn.Exec("DELETE FROM request_logs WHERE user_id = $1", userID)
		_ = database.DeleteVirtualKey(keyID)
		_ = database.DeleteUser(userID)
		_ = database.DeletePlan(planID)
	})

	settled := RequestLog{
		ID: "request-settle-" + suffix, VirtualKeyID: keyID, UserID: userID,
		RequestPath: "/v1/chat/completions", StatusCode: 200,
		Cost: 0.04, CostNanoUSD: 40_000_000, ChargeCeilingNanoUSD: 40_000_000,
		CreatedAt: time.Now().UTC(),
	}
	if err := database.SettleUsageAndLog(context.Background(), settled); err != nil {
		t.Fatalf("settle usage: %v", err)
	}
	if err := database.SettleUsageAndLog(context.Background(), settled); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}

	var logs, debits int
	if err := database.conn.QueryRow("SELECT COUNT(*) FROM request_logs WHERE id = $1", settled.ID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := database.conn.QueryRow("SELECT COUNT(*) FROM account_ledger WHERE request_id = $1", settled.ID).Scan(&debits); err != nil {
		t.Fatal(err)
	}
	if logs != 1 || debits != 1 {
		t.Fatalf("logs=%d debits=%d, want one atomic row of each", logs, debits)
	}
	available, err := database.AvailableBudgetNano(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if available != money.NanoUSD(10_000_000) {
		t.Fatalf("available=%s, want $0.010000000", available)
	}

	overBudget := settled
	overBudget.ID = "request-over-" + suffix
	overBudget.Cost = 0.02
	overBudget.CostNanoUSD = 20_000_000
	overBudget.ChargeCeilingNanoUSD = 20_000_000
	if err := database.SettleUsageAndLog(context.Background(), overBudget); err != nil {
		t.Fatalf("over-budget settlement should persist request log: %v", err)
	}
	if err := database.conn.QueryRow("SELECT COUNT(*) FROM request_logs WHERE id = $1", overBudget.ID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatalf("settlement persisted %d request logs, want 1", logs)
	}
	availableAfter, err := database.AvailableBudgetNano(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if availableAfter != 0 {
		t.Fatalf("availableAfter=%s, want 0", availableAfter)
	}
}
