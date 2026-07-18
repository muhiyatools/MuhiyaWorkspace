package proxy

import (
	"fmt"
	"testing"
	"time"
)

// A burst of more than stickyMapCap live (unexpired) sessions must not grow the
// map without bound: after the expired-only sweep the set() path evicts arbitrary
// live entries down to the cap. A dropped pin only costs one cache-cold turn.
func TestStickyMapBoundsLiveEntries(t *testing.T) {
	s := newModelSticky()
	total := stickyMapCap + 500
	for i := 0; i < total; i++ {
		s.set(fmt.Sprintf("key-%d", i), "deepseek-chat")
	}
	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n > stickyMapCap {
		t.Fatalf("sticky map grew to %d, must be bounded at %d", n, stickyMapCap)
	}
}

// The most-recently-set key is spared from arbitrary eviction so the active
// conversation keeps its pin even under overflow pressure.
func TestStickyMapKeepsJustInsertedKey(t *testing.T) {
	s := newModelSticky()
	for i := 0; i < stickyMapCap+50; i++ {
		s.set(fmt.Sprintf("filler-%d", i), "deepseek-chat")
	}
	s.set("current-session", "deepseek-reasoner")
	if got, ok := s.get("current-session"); !ok || got != "deepseek-reasoner" {
		t.Fatalf("just-inserted key lost its pin: got=%q ok=%v", got, ok)
	}
}

// Expired entries are swept and eviction (get) returns a miss past the TTL.
func TestStickyGetExpiredMisses(t *testing.T) {
	s := newModelSticky()
	s.set("k", "m")
	s.mu.Lock()
	s.entries["k"] = stickyEntry{modelID: "m", expires: time.Now().Add(-time.Minute)}
	s.mu.Unlock()
	if _, ok := s.get("k"); ok {
		t.Fatal("expired pin must miss")
	}
}
