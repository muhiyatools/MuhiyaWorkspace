package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gateway/db"
	"github.com/redis/go-redis/v9"
)

type routeAffinityStore struct {
	client *redis.Client
	ttl    time.Duration
}

func newRouteAffinityStore() *routeAffinityStore {
	redisURL := strings.TrimSpace(os.Getenv("REDIS_URL"))
	if redisURL == "" {
		return &routeAffinityStore{}
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Printf("[AFFINITY] REDIS_URL invalid; route affinity disabled: %v", err)
		return &routeAffinityStore{}
	}
	return &routeAffinityStore{
		client: redis.NewClient(options),
		ttl:    envDuration("ROUTE_AFFINITY_TTL", 6*time.Hour),
	}
}

func affinityScope(keyID, sessionID, recordID string) string {
	raw := keyID + "\x00" + sessionID + "\x00" + recordID
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("route-affinity:v1:%x", digest)
}

func (s *routeAffinityStore) get(ctx context.Context, scope string) (string, error) {
	if s == nil || s.client == nil || scope == "" {
		return "", nil
	}
	value, err := s.client.Get(ctx, scope).Result()
	if err == redis.Nil {
		return "", nil
	}
	return strings.TrimSpace(value), err
}

func (s *routeAffinityStore) observe(ctx context.Context, scope, provider string) error {
	if s == nil || s.client == nil || scope == "" || strings.TrimSpace(provider) == "" {
		return nil
	}
	return s.client.Set(ctx, scope, strings.TrimSpace(provider), s.ttl).Err()
}

func (s *routeAffinityStore) clear(ctx context.Context, scope string) error {
	if s == nil || s.client == nil || scope == "" {
		return nil
	}
	return s.client.Del(ctx, scope).Err()
}

func (h *ProxyHandler) applyProviderAffinity(
	ctx context.Context,
	body map[string]any,
	request *http.Request,
	model *db.Model,
	logEntry *db.RequestLog,
) string {
	if h.affinity == nil || model.ProviderFamily != "minimax-openrouter" {
		return ""
	}
	sessionID := strings.TrimSpace(request.Header.Get("X-Session-Id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(request.Header.Get("X-Muhiya-Session"))
	}
	if sessionID == "" {
		return ""
	}
	record, err := catalogRecord(model)
	if err != nil {
		return ""
	}
	scope := affinityScope(logEntry.VirtualKeyID, sessionID, record.RecordID)
	lookupCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	upstream, err := h.affinity.get(lookupCtx, scope)
	cancel()
	if err != nil {
		log.Printf("[AFFINITY] lookup failed: %v", err)
		return scope
	}
	if upstream == "" {
		return scope
	}
	body["provider"] = map[string]any{
		"order":           []string{upstream},
		"allow_fallbacks": false,
	}
	return scope
}

func (h *ProxyHandler) observeProviderAffinity(scope, upstream string) {
	if scope == "" || upstream == "" || h.affinity == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.affinity.observe(ctx, scope, upstream); err != nil {
		log.Printf("[AFFINITY] observation write failed: %v", err)
	}
}
