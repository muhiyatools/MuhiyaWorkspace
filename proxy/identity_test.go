package proxy

import (
	"regexp"
	"testing"
)

// userIDPattern is the documented shape of an injected upstream user_id
// (feature 007 upstream-request §3): "mu-" + 40 lowercase hex chars.
var userIDPattern = regexp.MustCompile(`^mu-[0-9a-f]{40}$`)

// TestDeriveUserIDFormat locks the wire contract: every non-empty result must
// match ^mu-[0-9a-f]{40}$ so the upstream never sees a value of an unexpected
// shape (or a leaked raw caller id).
func TestDeriveUserIDFormat(t *testing.T) {
	for _, userID := range []string{"user-1", "alice@example.com", "u", "0000", "المستخدم"} {
		got := DeriveUserID("server-secret", userID)
		if !userIDPattern.MatchString(got) {
			t.Errorf("DeriveUserID(secret, %q) = %q, does not match %s", userID, got, userIDPattern)
		}
		if len(got) != 43 {
			t.Errorf("DeriveUserID(secret, %q) length = %d, want 43", userID, len(got))
		}
	}
}

// TestDeriveUserIDDeterministic: the same secret+userID must yield the same
// value across calls (a coding-agent session that re-derives per request must
// keep landing the DeepSeek prefix cache on a stable identity).
func TestDeriveUserIDDeterministic(t *testing.T) {
	first := DeriveUserID("secret", "user-42")
	for i := 0; i < 5; i++ {
		if got := DeriveUserID("secret", "user-42"); got != first {
			t.Fatalf("call %d = %q, want stable %q", i, got, first)
		}
	}
}

// TestDeriveUserIDDistinct: distinct callers (or distinct secrets) must map to
// distinct opaque ids - collisions would cross-attribute cache/usage.
func TestDeriveUserIDDistinct(t *testing.T) {
	a := DeriveUserID("secret", "user-a")
	b := DeriveUserID("secret", "user-b")
	if a == b {
		t.Errorf("distinct users collided: %q", a)
	}
	// Rotating the server secret must change the derived value for the same user.
	if DeriveUserID("secret-1", "user-a") == DeriveUserID("secret-2", "user-a") {
		t.Error("distinct secrets produced the same user_id")
	}
}

// TestDeriveUserIDEmptyDisables: an empty secret OR an empty userID disables
// injection by returning "" (signals callers to send no "user_id" at all).
func TestDeriveUserIDEmptyDisables(t *testing.T) {
	if got := DeriveUserID("", "user-1"); got != "" {
		t.Errorf("empty secret = %q, want \"\"", got)
	}
	if got := DeriveUserID("secret", ""); got != "" {
		t.Errorf("empty userID = %q, want \"\"", got)
	}
	if got := DeriveUserID("", ""); got != "" {
		t.Errorf("both empty = %q, want \"\"", got)
	}
}
