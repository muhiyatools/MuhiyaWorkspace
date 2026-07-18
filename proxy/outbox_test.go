package proxy

import (
	"testing"

	"gateway/db"
)

// An outbox at capacity must not block the request-serving goroutine, and each
// dropped billing row must bump BillingLossCount so the loss is observable on
// /health rather than only in the logs.
func TestOutboxOverflowCountsBillingLoss(t *testing.T) {
	o := &logOutbox{queue: make(chan db.RequestLog, 1)} // db nil: run() is never started
	start := BillingLossCount.Load()

	o.enqueue(db.RequestLog{ID: "a"}) // fills the buffer (never drained)
	o.enqueue(db.RequestLog{ID: "b"}) // buffer full → counted loss
	o.enqueue(db.RequestLog{ID: "c"}) // counted loss

	if got := BillingLossCount.Load() - start; got != 2 {
		t.Fatalf("billing-loss delta = %d, want 2", got)
	}
}
