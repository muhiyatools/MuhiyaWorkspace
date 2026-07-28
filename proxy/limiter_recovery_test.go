package proxy

import (
	"testing"

	"gateway/db"
)

func TestRequiredRedisMisconfigurationFailsRequestsClosedWithoutKillingProcess(t *testing.T) {
	t.Setenv("REQUIRE_REDIS", "1")
	t.Setenv("REDIS_URL", "not-a-redis-url")
	limiter := NewRateLimiter(&db.DB{})
	err := limiter.CheckLimit(&db.VirtualKey{ID: "key", UserID: "user"}, 10)
	if err == nil || KindOf(err) != LimitInfrastructure {
		t.Fatalf("error=%v kind=%v, want infrastructure failure", err, KindOf(err))
	}
}
