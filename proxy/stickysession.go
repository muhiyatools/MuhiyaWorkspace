package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"time"
)

// SessionHeader lets a client pin the router's model choice for the life of a
// conversation. DeepSeek's prefix cache is per upstream model: without this,
// muhiya-ai-router re-evaluates complexity/thinking on every request and can
// silently swap deepseek-chat <-> deepseek-reasoner mid-session (e.g. a
// /reasoning change), wiping the entire cached prefix. Clients that care
// about cache stability should send the same opaque value for every request
// in one conversation.
const SessionHeader = "X-Muhiya-Session"

const stickyTTL = 24 * time.Hour

// stickyMapCap bounds the in-process map; once exceeded a sweep drops
// expired entries so long-running instances cannot leak memory indefinitely.
const stickyMapCap = 10000

type stickyEntry struct {
	modelID string
	expires time.Time
}

// modelSticky remembers the model chosen for a session key so the router
// reuses it on every subsequent request in that conversation. In-process
// only: a miss here (e.g. after a restart, or on a different replica behind
// a load balancer) only costs one cache-cold turn - never a wrong answer -
// so a distributed store is not required for correctness, only for maximum
// hit rate under horizontal scaling (a future enhancement, not implemented).
type modelSticky struct {
	mu      sync.Mutex
	entries map[string]stickyEntry
}

func newModelSticky() *modelSticky {
	return &modelSticky{entries: make(map[string]stickyEntry)}
}

// stickyKeyFor derives the stickiness key for a request. An explicit
// X-Muhiya-Session header (scoped to the virtual key, so one client's session
// IDs can never collide with another's) is required; absent that, stickiness
// is unavailable and routing falls back to per-request behavior unchanged.
func stickyKeyFor(virtualKeyID, sessionHeader string) string {
	sessionHeader = strings.TrimSpace(sessionHeader)
	if sessionHeader == "" || virtualKeyID == "" {
		return ""
	}
	h := sha256.Sum256([]byte(virtualKeyID + "|" + sessionHeader))
	return hex.EncodeToString(h[:])
}

func (s *modelSticky) get(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.modelID, true
}

// evict removes a session's pin so the next request in that conversation routes
// fresh. Called when the pinned model failed (e.g. a free-tier 429) so a session
// is never trapped on a broken model for the 24h TTL.
func (s *modelSticky) evict(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
}

func (s *modelSticky) set(key, modelID string) {
	if key == "" || modelID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Observe genuine mid-session model swaps (feature 007 R10 / FR-012): a
	// session key that still resolves to a *different* model is about to be
	// repinned, which abandons DeepSeek's per-model prefix cache for that
	// conversation. A first-time pin (no prior entry) or a refresh to the same
	// model is not a swap and stays silent. The pinning posture is unchanged -
	// this only records the event. The key is already a sha256 hash, so a short
	// prefix is safe to log for correlation without exposing the session secret.
	if prev, ok := s.entries[key]; ok && !time.Now().After(prev.expires) && prev.modelID != modelID {
		hashPrefix := key
		if len(hashPrefix) > 12 {
			hashPrefix = hashPrefix[:12]
		}
		log.Printf("[ROUTER-STICKY-SWAP] session %s… repinned from model %s to %s (per-model prefix cache abandoned)", hashPrefix, prev.modelID, modelID)
	}
	s.entries[key] = stickyEntry{modelID: modelID, expires: time.Now().Add(stickyTTL)}
	if len(s.entries) > stickyMapCap {
		now := time.Now()
		for k, v := range s.entries {
			if now.After(v.expires) {
				delete(s.entries, k)
			}
		}
		// A burst of more than stickyMapCap *live* sessions would survive the
		// expired-only sweep above and grow the map without bound for the 24h
		// TTL. Evict arbitrary live entries down to the cap - dropping a pin only
		// costs one cache-cold turn, never a wrong answer, so a bounded map is
		// strictly better than an unbounded one. The just-inserted key is spared
		// so the current conversation keeps its pin.
		for k := range s.entries {
			if len(s.entries) <= stickyMapCap {
				break
			}
			if k == key {
				continue
			}
			delete(s.entries, k)
		}
	}
}
