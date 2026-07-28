package proxy

import "testing"

func TestAffinityScopeIsStableAndFullyScoped(t *testing.T) {
	base := affinityScope("key-1", "session-1", "record-1")
	if base == "" || base != affinityScope("key-1", "session-1", "record-1") {
		t.Fatalf("affinity scope is not stable: %q", base)
	}
	cases := []string{
		affinityScope("key-2", "session-1", "record-1"),
		affinityScope("key-1", "session-2", "record-1"),
		affinityScope("key-1", "session-1", "record-2"),
	}
	for _, scope := range cases {
		if scope == base {
			t.Fatalf("affinity scope collision: %q", scope)
		}
	}
}

func TestDisabledAffinityStoreIsSafe(t *testing.T) {
	store := &routeAffinityStore{}
	if value, err := store.get(t.Context(), "scope"); err != nil || value != "" {
		t.Fatalf("disabled get = %q, %v", value, err)
	}
	if err := store.observe(t.Context(), "scope", "provider"); err != nil {
		t.Fatalf("disabled observe: %v", err)
	}
	if err := store.clear(t.Context(), "scope"); err != nil {
		t.Fatalf("disabled clear: %v", err)
	}
}
