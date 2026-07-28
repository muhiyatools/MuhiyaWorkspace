package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConcurrentReservationsCannotOverspendOneWindow(t *testing.T) {
	database := openTestDB(t)
	defer database.Close()

	suffix := uuid.NewString()[:8]
	planID := "plan-reserve-" + suffix
	userID := "user-reserve-" + suffix
	plan := Plan{
		ID: planID, Name: "Reservation Test", RPMLimit: 100, TPMLimit: 1_000_000,
		BudgetWindows: []BudgetWindow{{
			ID: "window-reserve-" + suffix, Name: "daily", DurationSeconds: 24 * 60 * 60,
			BudgetNanoUSD: 50_000_000,
		}},
	}
	if err := database.CreatePlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(User{
		ID: userID, Name: "Reservation User", Email: suffix + "@test.invalid",
		PlanID: planID, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.conn.Exec("DELETE FROM account_ledger WHERE user_id = $1", userID)
		_, _ = database.conn.Exec("DELETE FROM request_logs WHERE user_id = $1", userID)
		_, _ = database.conn.Exec("DELETE FROM budget_reservations WHERE user_id = $1", userID)
		_ = database.DeleteUser(userID)
		_ = database.DeletePlan(planID)
	})

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := database.ReserveBudget(context.Background(), ReserveBudgetRequest{
				RequestID: "request-" + suffix + "-" + string(rune('a'+index)),
				UserID:    userID, Amount: 40_000_000,
				PriceSnapshot: "snapshot-1", LeaseDuration: time.Minute,
			})
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrBudgetExceeded):
			rejected++
		default:
			t.Fatalf("unexpected reservation error: %v", err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("successes=%d rejected=%d, want exactly one of each", successes, rejected)
	}
	available, err := database.AvailableBudgetNano(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if available != 10_000_000 {
		t.Fatalf("available=%s, want $0.010000000", available)
	}
}
