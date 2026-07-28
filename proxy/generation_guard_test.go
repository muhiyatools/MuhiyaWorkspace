package proxy

import (
	"context"
	"testing"
	"time"
)

func TestInMemoryGenerationGuardSerializesOneUser(t *testing.T) {
	limiter := &RateLimiter{active: make(map[string]string)}

	releaseFirst, err := limiter.AcquireGeneration(context.Background(), "user-1", "request-1")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	releaseOtherUser, err := limiter.AcquireGeneration(context.Background(), "user-2", "request-3")
	if err != nil {
		t.Fatalf("different user should proceed: %v", err)
	}
	releaseOtherUser()

	acquired := make(chan func(), 1)
	go func() {
		release, acquireErr := limiter.AcquireGeneration(context.Background(), "user-1", "request-2")
		if acquireErr != nil {
			t.Errorf("queued acquire: %v", acquireErr)
			return
		}
		acquired <- release
	}()
	select {
	case <-acquired:
		t.Fatal("second request acquired before the first released")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	releaseFirst() // Release is intentionally idempotent.
	select {
	case releaseAfter := <-acquired:
		releaseAfter()
	case <-time.After(time.Second):
		t.Fatal("queued request did not acquire after release")
	}
}
