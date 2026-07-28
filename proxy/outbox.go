package proxy

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"gateway/db"
)

// BillingLossCount counts request-log rows that could not be persisted — retries
// exhausted, or the outbox buffer overflowed during a sustained outage. It is
// exposed on /health so silent billing loss is observable with a single curl
// instead of only by scraping logs for [BILLING-LOSS].
var BillingLossCount atomic.Int64

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

// outboxWorkers drain the queue concurrently. A single worker processed entries
// serially at up to 81 seconds each (the full backoff ladder), so a 30-second
// outage at even modest request rates filled the 4096-entry buffer faster than
// it could drain and turned a transient blip into permanent revenue loss.
const outboxWorkers = 8

func (o *logOutbox) run() {
	for i := 0; i < outboxWorkers; i++ {
		go func() {
			for entry := range o.queue {
				o.retry(entry)
			}
		}()
	}
}

func (o *logOutbox) retry(entry db.RequestLog) {
	// Attempt IMMEDIATELY before any backoff. The first insert failed at request
	// time, but by the moment this runs the blip is often already over — and
	// sleeping first spent a second of the buffer's drain budget to learn
	// nothing. Only genuine repeat failures pay the ladder.
	if err := o.persist(entry); err == nil {
		return
	}
	for _, d := range []time.Duration{1 * time.Second, 5 * time.Second, 15 * time.Second, 60 * time.Second} {
		time.Sleep(d)
		if err := o.persist(entry); err == nil {
			log.Printf("[BILLING-RECOVERED] request log %s persisted after retry", entry.ID)
			return
		}
	}
	BillingLossCount.Add(1)
	log.Printf("[BILLING-LOSS] CRITICAL: request log %s (key=%s user=%s cost=%.6f) could not be persisted after retries - billing data lost", entry.ID, entry.VirtualKeyID, entry.UserID, entry.Cost)
}

func (o *logOutbox) persist(entry db.RequestLog) error {
	if entry.ReservationID == "" {
		return o.db.InsertRequestLog(entry)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return o.db.SettleReservationAndLog(ctx, entry)
}

// enqueue is non-blocking: a full buffer means a sustained outage, in which
// case blocking the request-serving goroutine would be worse than logging
// the loss immediately.
func (o *logOutbox) enqueue(entry db.RequestLog) {
	select {
	case o.queue <- entry:
	default:
		BillingLossCount.Add(1)
		log.Printf("[BILLING-LOSS] CRITICAL: outbox full, dropping request log %s (key=%s user=%s cost=%.6f)", entry.ID, entry.VirtualKeyID, entry.UserID, entry.Cost)
	}
}
