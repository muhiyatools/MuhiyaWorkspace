package db

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// openTestDB opens a live database for billing-persistence tests, skipping when
// none is configured (mirrors proxy.TestRateLimiter's gate).
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("Skipping database billing test: TEST_DATABASE_URL or DATABASE_URL not set")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	return d
}

// TestCacheMissTokensPersistence (feature 007 T035): a completed response with a
// provider-reported cache-miss count persists cache_miss_tokens; an estimated
// response (no provider figure - CacheMissTokens nil) leaves the column NULL so
// the dashboard falls back to its input-token approximation.
func TestCacheMissTokensPersistence(t *testing.T) {
	d := openTestDB(t)
	defer d.Close()

	suffix := uuid.NewString()[:8]
	plan := Plan{ID: "plan-miss-" + suffix, Name: "Miss Plan"}
	user := User{ID: "user-miss-" + suffix, Name: "Miss User", Email: "miss-" + suffix + "@test.com", PlanID: plan.ID, Status: "active"}
	if err := d.CreatePlan(plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := d.CreateUser(user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	miss := int64(104)
	completed := RequestLog{
		ID: "log-completed-" + suffix, UserID: user.ID, RequestPath: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 1000, OutputTokens: 50, CacheReadTokens: 896,
		CacheMissTokens: &miss, Cost: 0.001, LatencyMS: 10, CreatedAt: time.Now(),
	}
	estimated := RequestLog{
		ID: "log-estimated-" + suffix, UserID: user.ID, RequestPath: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 500, OutputTokens: 20, CacheMissTokens: nil,
		UsageEstimated: true, Cost: 0.0005, LatencyMS: 10, CreatedAt: time.Now(),
	}

	t.Cleanup(func() {
		_, _ = d.conn.Exec("DELETE FROM request_logs WHERE id = $1 OR id = $2", completed.ID, estimated.ID)
		_ = d.DeleteUser(user.ID)
		_ = d.DeletePlan(plan.ID)
	})

	if err := d.InsertRequestLog(completed); err != nil {
		t.Fatalf("insert completed log: %v", err)
	}
	if err := d.InsertRequestLog(estimated); err != nil {
		t.Fatalf("insert estimated log: %v", err)
	}

	// Completed row: cache_miss_tokens is present with the provider figure.
	var got sql.NullInt64
	if err := d.conn.QueryRow("SELECT cache_miss_tokens FROM request_logs WHERE id = $1", completed.ID).Scan(&got); err != nil {
		t.Fatalf("scan completed: %v", err)
	}
	if !got.Valid || got.Int64 != miss {
		t.Errorf("completed cache_miss_tokens = %+v, want valid %d", got, miss)
	}

	// Estimated row: cache_miss_tokens must be NULL.
	if err := d.conn.QueryRow("SELECT cache_miss_tokens FROM request_logs WHERE id = $1", estimated.ID).Scan(&got); err != nil {
		t.Fatalf("scan estimated: %v", err)
	}
	if got.Valid {
		t.Errorf("estimated cache_miss_tokens = %d, want NULL", got.Int64)
	}

	// No duplicate rows: each InsertRequestLog persisted exactly one row per id.
	var count int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM request_logs WHERE id IN ($1, $2)", completed.ID, estimated.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("row count = %d, want 2 (no duplicate BILLED rows)", count)
	}
}
