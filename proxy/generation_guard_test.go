package proxy

import (
	"context"
	"net/http"
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

func TestGenerationScopeUsesUserIDForInteractiveClients(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	scope := generationScope(r, "user-1", "req-abc")
	if scope != "user-1" {
		t.Fatalf("expected user-1, got %s", scope)
	}
}

func TestGenerationScopeUsesRequestIDForBatchClients(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Dawa-Org-ID", "42")
	scope := generationScope(r, "user-1", "req-abc")
	if scope != "req-abc" {
		t.Fatalf("expected req-abc, got %s", scope)
	}
}

func TestBatchClientsAcquireGenerationConcurrently(t *testing.T) {
	limiter := &RateLimiter{active: make(map[string]string)}

	// Simulate two concurrent batch requests (each gets its own scope via requestID).
	release1, err := limiter.AcquireGeneration(context.Background(), "req-1", "req-1")
	if err != nil {
		t.Fatalf("first batch acquire: %v", err)
	}
	release2, err := limiter.AcquireGeneration(context.Background(), "req-2", "req-2")
	if err != nil {
		t.Fatalf("second batch acquire should proceed immediately: %v", err)
	}

	release1()
	release2()
}
