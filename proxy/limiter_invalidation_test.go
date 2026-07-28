package proxy

import (
	"testing"

	"gateway/db"
)

func TestLimiterDefinitionInvalidation(t *testing.T) {
	limiter := &RateLimiter{defs: &limiterDefsCache{}}
	limiter.defs.set("user-a", limiterDefsEntry{user: &db.User{ID: "user-a"}})
	limiter.defs.set("user-b", limiterDefsEntry{user: &db.User{ID: "user-b"}})

	limiter.InvalidateUser("user-a")
	if _, found := limiter.defs.get("user-a"); found {
		t.Fatal("user-specific definition remained cached")
	}
	if _, found := limiter.defs.get("user-b"); !found {
		t.Fatal("unrelated user definition was evicted")
	}

	limiter.InvalidateAll()
	if _, found := limiter.defs.get("user-b"); found {
		t.Fatal("global invalidation left a definition cached")
	}
}
