package proxy

import (
	"log"
	"time"

	"gateway/db"
)

// logOutbox retries a failed request-log insert with backoff before giving
// up, so a transient Postgres blip (failover, pool exhaustion, a brief
// network blip) does not silently lose a billing row and its credit
// deduction. InsertRequestLog errors used to be discarded outright
// ("_ = h.db.InsertRequestLog(log)"); this is the durable-retry-buffer
// minimum called for by the gateway audit (bounded channel with backoff).
type logOutbox struct {
	queue chan db.RequestLog
	db    *db.DB
}

// logOutboxCapacity bounds memory during a sustained outage; beyond this,
// further failures log immediately as an unrecoverable loss rather than
// blocking the request-serving goroutine on a full channel.
const logOutboxCapacity = 4096

func newLogOutbox(database *db.DB) *logOutbox {
	o := &logOutbox{queue: make(chan db.RequestLog, logOutboxCapacity), db: database}
	go o.run()
	return o
}

func (o *logOutbox) run() {
	for entry := range o.queue {
		o.retry(entry)
	}
}

func (o *logOutbox) retry(entry db.RequestLog) {
	backoff := []time.Duration{1 * time.Second, 5 * time.Second, 15 * time.Second, 60 * time.Second}
	for _, d := range backoff {
		time.Sleep(d)
		if err := o.db.InsertRequestLog(entry); err == nil {
			log.Printf("[BILLING-RECOVERED] request log %s persisted after retry", entry.ID)
			return
		}
	}
	log.Printf("[BILLING-LOSS] CRITICAL: request log %s (key=%s user=%s cost=%.6f) could not be persisted after retries - billing data lost", entry.ID, entry.VirtualKeyID, entry.UserID, entry.Cost)
}

// enqueue is non-blocking: a full buffer means a sustained outage, in which
// case blocking the request-serving goroutine would be worse than logging
// the loss immediately.
func (o *logOutbox) enqueue(entry db.RequestLog) {
	select {
	case o.queue <- entry:
	default:
		log.Printf("[BILLING-LOSS] CRITICAL: outbox full, dropping request log %s (key=%s user=%s cost=%.6f)", entry.ID, entry.VirtualKeyID, entry.UserID, entry.Cost)
	}
}
