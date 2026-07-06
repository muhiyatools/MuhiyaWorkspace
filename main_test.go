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
