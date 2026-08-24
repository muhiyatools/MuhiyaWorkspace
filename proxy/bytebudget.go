package proxy

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// byteBudget caps the TOTAL number of request bytes resident in memory across
// all concurrently-buffered requests. ServeHTTP reads every body fully into
// RAM before rate limiting or the generation guard run (24 MiB chat, 100 MiB
// transcription), so without this cap N concurrent authenticated uploads pin
// N × body-limit bytes of heap ahead of any admission control - an OOM vector.
//
// avail is denominated in raw bytes: Acquire carves out up to n bytes at a
// time and blocks (bounded by the request context) while capacity remains
// exhausted; Release returns it. A blocked Acquire delays, never fails,
// well-behaved traffic; abusive concurrency queues instead of exhausting
// memory.
type byteBudget struct {
	mu    sync.Mutex
	avail int64
}

const byteBudgetUnit = int64(1) << 20 // 1 MiB per token

// defaultMaxInflightRequestBytes bounds buffered request bytes process-wide:
// ~10 parallel 24 MiB chat bodies, or 2 full-size transcription uploads plus
// headroom. Tunable via MAX_INFLIGHT_REQUEST_BYTES without a rebuild.
const defaultMaxInflightRequestBytes = 256 << 20

func maxInflightRequestBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("MAX_INFLIGHT_REQUEST_BYTES"))
	if raw == "" {
		return defaultMaxInflightRequestBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < byteBudgetUnit {
		log.Printf("[CONFIG] MAX_INFLIGHT_REQUEST_BYTES=%q is not an integer >= %d; using %d", raw, byteBudgetUnit, defaultMaxInflightRequestBytes)
		return defaultMaxInflightRequestBytes
	}
	return n
}

func newByteBudget(total int64) *byteBudget {
	if total < byteBudgetUnit {
		total = byteBudgetUnit
	}
	return &byteBudget{avail: total}
}

// Acquire reserves n bytes of buffer budget, blocking while insufficient
// capacity remains. Gives up only when ctx is cancelled.
func (b *byteBudget) Acquire(ctx context.Context, n int64) error {
	acquired := int64(0)
	for acquired < n {
		b.mu.Lock()
		if b.avail > 0 {
			take := b.avail
			if take > n-acquired {
				take = n - acquired
			}
			b.avail -= take
			acquired += take
			b.mu.Unlock()
			continue
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			b.Release(acquired)
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return nil
}

// Release returns n bytes of budget. Never panics on over-release.
func (b *byteBudget) Release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.avail += n
}
