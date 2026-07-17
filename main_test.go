package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
	keyValue := withPostgresConnectTimeout("host=localhost port=5432 user=postgres")
	if !strings.Contains(keyValue, "connect_timeout=5") {
		t.Fatalf("expected key/value DSN to include connect_timeout, got %q", keyValue)
	}

	parsed, err := url.Parse(withPostgresConnectTimeout("postgres://user:pass@example.com:5432/gateway?sslmode=disable"))
	if err != nil {
		t.Fatalf("expected postgres URL to parse: %v", err)
	}
	if parsed.Query().Get("connect_timeout") != "5" {
		t.Fatalf("expected postgres URL connect_timeout=5, got %q", parsed.RawQuery)
	}

	existing := "postgres://user:pass@example.com:5432/gateway?connect_timeout=12"
	if got := withPostgresConnectTimeout(existing); got != existing {
		t.Fatalf("existing timeout should be preserved, got %q", got)
	}
}
