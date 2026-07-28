package admin

import (
	"testing"

	"gateway/db"
)

func TestValidatePlanRejectsAmbiguousBudgetWindows(t *testing.T) {
	plan := db.Plan{
		ID:       "plan-production",
		Name:     "Production",
		RPMLimit: 100,
		TPMLimit: 100_000,
		BudgetWindows: []db.BudgetWindow{
			{Name: "Daily", DurationSeconds: 86_400, BudgetUSD: 5},
			{Name: "Duplicate daily", DurationSeconds: 86_400, BudgetUSD: 10},
		},
	}
	if message := validatePlan(plan); message == "" {
		t.Fatal("duplicate budget durations should be rejected")
	}
}

func TestValidatePlanAcceptsIndependentWindows(t *testing.T) {
	plan := db.Plan{
		ID:       "plan-production",
		Name:     "Production",
		RPMLimit: 100,
		TPMLimit: 100_000,
		BudgetWindows: []db.BudgetWindow{
			{Name: "Daily", DurationSeconds: 86_400, BudgetUSD: 5},
			{Name: "Monthly", DurationSeconds: 2_592_000, BudgetUSD: 100},
		},
	}
	if message := validatePlan(plan); message != "" {
		t.Fatalf("valid plan rejected: %s", message)
	}
}
