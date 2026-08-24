package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRegisterProxyRoutesIncludesGatewayTools(t *testing.T) {
	mux := http.NewServeMux()
	registerProxyRoutes(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{
		"/v1/capabilities",
		"/capabilities",
		"/v1/tools/web_search",
		"/tools/web_search",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s should route to proxy wrapper, got status %d", path, rec.Code)
		}
	}
}

func TestRedactCredentialNeverLeaksFullKey(t *testing.T) {
	secret := "sk-virt-1234567890abcdef"

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	got := redactCredential(req)
	if strings.Contains(got, secret) {
		t.Fatalf("redactCredential leaked the full key: %q", got)
	}
	if !strings.HasPrefix(got, "set(") {
		t.Fatalf("expected a redacted fingerprint, got %q", got)
	}

	// x-api-key path.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("x-api-key", secret)
	if strings.Contains(redactCredential(req2), secret) {
		t.Fatalf("redactCredential leaked the x-api-key")
	}

	// No credentials.
	if got := redactCredential(httptest.NewRequest(http.MethodGet, "/health", nil)); got != "none" {
		t.Fatalf("expected 'none' for no credentials, got %q", got)
	}
}

// newAuthTestHandler wraps serviceOrAdminAuth around a sentinel that records
// whether the request reached next.
func newAuthTestHandler(reached *bool) http.Handler {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
	return serviceOrAdminAuth("admin", "adminpass", "", "", next)
}

// TestServiceOrAdminAuthAllowsAbsentOrigin is the single most important
// regression guard: the platform is a server-side client and sends no Origin and
// no Content-Type, and that request MUST reach next. Breaking this breaks
// platform->gateway traffic.
func TestServiceOrAdminAuthAllowsAbsentOrigin(t *testing.T) {
	var reached bool
	h := newAuthTestHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	req.SetBasicAuth("admin", "adminpass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("absent-Origin POST (the platform path) must reach next; got status %d", rec.Code)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for absent-Origin POST, got %d", rec.Code)
	}
}

// TestServiceOrAdminAuthAllowsSameOriginWrite covers the admin SPA, whose
// mutateJSON always sends Origin (same host) and Content-Type: application/json.
func TestServiceOrAdminAuthAllowsSameOriginWrite(t *testing.T) {
	var reached bool
	h := newAuthTestHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	req.Host = "gw.example.com"
	req.Header.Set("Origin", "https://gw.example.com")
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "adminpass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("same-origin JSON write (the SPA path) must reach next; got status %d", rec.Code)
	}
}

// TestServiceOrAdminAuthRejectsCrossOriginWrite is the CSRF guard: a cross-site
// write is rejected with 403 before any credential comparison, and no
// WWW-Authenticate header is emitted (that would pop a login prompt at the victim).
func TestServiceOrAdminAuthRejectsCrossOriginWrite(t *testing.T) {
	var reached bool
	h := newAuthTestHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	req.Host = "gw.example.com"
	req.Header.Set("Origin", "https://evil.example")
	req.SetBasicAuth("admin", "adminpass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatalf("cross-origin write must NOT reach next")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin write, got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("403 CSRF rejection must not send WWW-Authenticate")
	}
}

// TestServiceOrAdminAuthRejectsFormContentType kills the enctype=text/plain
// form-CSRF payload even when it carries no Origin.
func TestServiceOrAdminAuthRejectsFormContentType(t *testing.T) {
	var reached bool
	h := newAuthTestHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/api/logs", nil)
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.SetBasicAuth("admin", "adminpass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatalf("text/plain form write must NOT reach next")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for text/plain body, got %d", rec.Code)
	}
}

// TestServiceOrAdminAuthAllowsCrossOriginRead confirms reads are not gated — a
// cross-site GET's response is already unreadable without CORS headers.
func TestServiceOrAdminAuthAllowsCrossOriginRead(t *testing.T) {
	var reached bool
	h := newAuthTestHandler(&reached)

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.Host = "gw.example.com"
	req.Header.Set("Origin", "https://evil.example")
	req.SetBasicAuth("admin", "adminpass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("cross-origin GET must reach next; got status %d", rec.Code)
	}
}

// TestServiceOrAdminAuthStillRejectsBadCredentials is a regression guard on the
// existing 401 + WWW-Authenticate path (bad creds, no CSRF trigger).
func TestServiceOrAdminAuthStillRejectsBadCredentials(t *testing.T) {
	var reached bool
	h := newAuthTestHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	req.SetBasicAuth("admin", "wrongpass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatalf("bad credentials must NOT reach next")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad credentials, got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("401 must send WWW-Authenticate")
	}
}

func TestWithPostgresConnectTimeout(t *testing.T) {
	// All three safety params must be applied: connect_timeout bounds dialing,
	// while statement_timeout and lock_timeout are what stop one slow query or
	// one contended advisory lock from holding a pool connection indefinitely
	// and stalling every other tenant.
	keyValue := withPostgresConnectTimeout("host=localhost port=5432 user=postgres")
	for _, want := range []string{"connect_timeout=5", "statement_timeout=30000", "lock_timeout=5000"} {
		if !strings.Contains(keyValue, want) {
			t.Fatalf("expected key/value DSN to include %s, got %q", want, keyValue)
		}
	}

	parsed, err := url.Parse(withPostgresConnectTimeout("postgres://user:pass@example.com:5432/gateway?sslmode=disable"))
	if err != nil {
		t.Fatalf("expected postgres URL to parse: %v", err)
	}
	for name, want := range map[string]string{"connect_timeout": "5", "statement_timeout": "30000", "lock_timeout": "5000"} {
		if parsed.Query().Get(name) != want {
			t.Fatalf("expected postgres URL %s=%s, got %q", name, want, parsed.RawQuery)
		}
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("existing DSN params must survive, got %q", parsed.RawQuery)
	}

	// An operator's explicit value always wins over the default.
	existing, err := url.Parse(withPostgresConnectTimeout("postgres://user:pass@example.com:5432/gateway?connect_timeout=12&statement_timeout=90000"))
	if err != nil {
		t.Fatal(err)
	}
	if existing.Query().Get("connect_timeout") != "12" || existing.Query().Get("statement_timeout") != "90000" {
		t.Fatalf("operator-set timeouts must be preserved, got %q", existing.RawQuery)
	}
	if existing.Query().Get("lock_timeout") != "5000" {
		t.Fatalf("unset params must still be defaulted, got %q", existing.RawQuery)
	}
}

func TestLoginThrottleLocksOutRepeatedFailures(t *testing.T) {
	loginThrottleMu.Lock()
	loginThrottleBuckets = make(map[string]*loginBucket)
	loginThrottleGlobal = nil
	loginThrottleMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/stats", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	rec := httptest.NewRecorder()

	// First failure: allowed through (401 path proceeds).
	if !loginThrottleGuard(rec, req) {
		t.Fatal("first failed attempt must not be locked out")
	}
	for i := 0; i < loginLockThreshold; i++ {
		loginThrottleFail(req)
	}

	// Threshold reached: guard must now reject with 429.
	rec2 := httptest.NewRecorder()
	if loginThrottleGuard(rec2, req) {
		t.Fatal("expected lockout after repeated failures")
	}
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on lockout, got %d", rec2.Code)
	}

	// A different source is unaffected (per-source bucketing).
	reqOther := httptest.NewRequest(http.MethodPost, "/api/stats", nil)
	reqOther.RemoteAddr = "198.51.100.7:5555"
	rec3 := httptest.NewRecorder()
	if !loginThrottleGuard(rec3, reqOther) {
		t.Fatal("a different source must not inherit another source's lockout")
	}
}

func TestLoginThrottleGlobalBreakerAndReset(t *testing.T) {
	loginThrottleMu.Lock()
	loginThrottleBuckets = make(map[string]*loginBucket)
	loginThrottleGlobal = nil
	loginThrottleMu.Unlock()

	// Trip the global breaker with failures from many distinct sources.
	now := time.Now()
	loginThrottleMu.Lock()
	for i := 0; i < loginGlobalFailMax; i++ {
		loginThrottleGlobal = append(loginThrottleGlobal, now)
	}
	loginThrottleMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.RemoteAddr = "192.0.2.50:1234"
	rec := httptest.NewRecorder()
	if loginThrottleGuard(rec, req) {
		t.Fatal("global circuit breaker should reject while failure window is saturated")
	}

	// Drain the window; a success resets the per-source bucket entirely.
	loginThrottleMu.Lock()
	loginThrottleGlobal = nil
	loginThrottleMu.Unlock()
	if !loginThrottleGuard(rec, req) {
		t.Fatal("guard should pass once global breaker drains")
	}
	loginThrottleSuccess(req)

	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	if _, exists := loginThrottleBuckets[clientSource(req)]; exists {
		t.Fatal("successful login must clear the source's throttle state")
	}
}

func TestSecurityHeadersApplied(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	securityHeaders(base).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing security header %s", h)
		}
	}
}
