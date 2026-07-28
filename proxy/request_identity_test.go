package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"gateway/db"
)

func TestRequestCorrelationIsIdempotentPerAttempt(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	request.Header.Set(clientRequestIDHeader, "logical-request-1")
	request.Header.Set(clientAttemptHeader, "1")

	first := requestCorrelationFor(request, "key-1")
	replayed := requestCorrelationFor(request, "key-1")
	if first != replayed {
		t.Fatalf("same attempt changed identity: first=%+v replayed=%+v", first, replayed)
	}

	request.Header.Set(clientAttemptHeader, "2")
	retry := requestCorrelationFor(request, "key-1")
	if retry.LogID == first.LogID {
		t.Fatal("a retry attempt must receive a distinct request-log row")
	}
	if retry.ClientRequestID != first.ClientRequestID || retry.AttemptNumber != 2 {
		t.Fatalf("retry grouping was lost: first=%+v retry=%+v", first, retry)
	}
}

func TestRequestContextCapturesSessionAndCacheEpoch(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	request.Header.Set(clientRequestIDHeader, "logical-request-2")
	request.Header.Set("X-Muhiya-Session", "session-42")
	request.Header.Set(clientCacheEpochHeader, "7")
	key := &db.VirtualKey{ID: "key-2", UserID: "user-2"}
	identity := requestCorrelationFor(request, key.ID)

	var entry db.RequestLog
	applyRequestContext(&entry, request, key, identity)
	if entry.UserID != key.UserID || entry.VirtualKeyID != key.ID ||
		entry.SessionID != "session-42" || entry.CacheEpoch != 7 ||
		entry.ClientRequestID != "logical-request-2" || entry.AttemptNumber != 1 {
		t.Fatalf("request context incomplete: %+v", entry)
	}
}

func TestInterruptedStreamNeverConsumesCredits(t *testing.T) {
	entry := db.RequestLog{Cost: 1, CostNanoUSD: 1_000_000_000}
	setStreamOutcome(&entry, false, true, nil)
	if entry.StatusCode != 504 || entry.RequestStatus != "timed_out" {
		t.Fatalf("timeout outcome = %+v", entry)
	}
	if entry.Cost != 0 || entry.CostNanoUSD != 0 {
		t.Fatalf("timed-out stream retained a customer charge: %+v", entry)
	}

	entry = db.RequestLog{Cost: 1, CostNanoUSD: 1_000_000_000}
	setStreamOutcome(&entry, false, false, context.Canceled)
	if entry.StatusCode != 499 || entry.RequestStatus != "cancelled" {
		t.Fatalf("cancel outcome = %+v", entry)
	}
}
