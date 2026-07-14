package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gateway/db"
	"github.com/google/uuid"
)

// relayTestEnv bundles a live-DB handler and a caller (plan/user/key) for the
// streaming-relay and billing regression tests. These paths exercise real
// persistence (saveRequestLog) so they require Postgres; without it they skip,
// mirroring TestRateLimiter's gate.
type relayTestEnv struct {
	h    *ProxyHandler
	db   *db.DB
	user db.User
	key  db.VirtualKey
}

func newRelayTestEnv(t *testing.T) *relayTestEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("Skipping database-backed relay test: TEST_DATABASE_URL or DATABASE_URL not set")
	}
	testDB, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}

	suffix := uuid.NewString()[:8]
	plan := db.Plan{ID: "plan-relay-" + suffix, Name: "Relay Plan"}
	user := db.User{ID: "user-relay-" + suffix, Name: "Relay User", Email: "relay-" + suffix + "@test.com", PlanID: plan.ID, Status: "active"}
	key := db.VirtualKey{ID: "key-relay-" + suffix, Name: "Relay Key", UserID: user.ID, Status: "active"}

	if err := testDB.CreatePlan(plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := testDB.CreateUser(user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := testDB.CreateVirtualKey(key); err != nil {
		t.Fatalf("create key: %v", err)
	}

	t.Cleanup(func() {
		_ = testDB.DeleteVirtualKey(key.ID)
		_ = testDB.DeleteUser(user.ID)
		_ = testDB.DeletePlan(plan.ID)
		_ = testDB.Close()
	})

	return &relayTestEnv{
		h:    NewProxyHandler(testDB, NewRateLimiter(testDB), "identity-secret"),
		db:   testDB,
		user: user,
		key:  key,
	}
}

// deepseekModelProvider returns an in-memory model+provider pointing a DeepSeek
// target at the given upstream URL (no models/providers rows needed - the proxy
// methods take these by pointer).
func deepseekModelProvider(upstreamURL string) (*db.Model, *db.Provider) {
	model := &db.Model{
		Name:                 "deepseek-relay",
		TargetModel:          "deepseek-reasoner",
		InputCostPerMillion:  1.0,
		OutputCostPerMillion: 2.0,
		Status:               "active",
	}
	provider := &db.Provider{Name: "deepseek", BaseURL: upstreamURL, APIKey: "sk-test", Status: "active"}
	return model, provider
}

func (e *relayTestEnv) newReqLog(path string) db.RequestLog {
	return db.RequestLog{
		ID:           uuid.NewString(),
		VirtualKeyID: e.key.ID,
		UserID:       e.user.ID,
		RequestPath:  path,
		ClientApp:    "MuhiyaCode",
		CreatedAt:    time.Now(),
	}
}

func (e *relayTestEnv) logCountForUser(t *testing.T) int {
	t.Helper()
	logs, err := e.db.ListRequestLogs(1000, 0, e.user.ID, "")
	if err != nil {
		t.Fatalf("list request logs: %v", err)
	}
	return len(logs)
}

// wrapWriter mirrors ServeHTTP's writer wrapping so logFailedUpstream/getRequest
// can recover the client request for dialect-aware error translation.
func wrapWriter(rec *httptest.ResponseRecorder, r *http.Request) *responseWriterWithRequest {
	return &responseWriterWithRequest{ResponseWriter: rec, req: r}
}

// TestRelayForwardsKeepAliveVerbatim (T015): the OpenAI->OpenAI streaming relay
// must forward ": keep-alive" comment lines and blank lines from the upstream
// verbatim to the client, then the real data frames and [DONE]. (The liveness
// reset those lines drive is covered independently at the primitive level in
// watchdog_test.go, since streamIdleTimeout is a production-scale constant.)
func TestRelayForwardsKeepAliveVerbatim(t *testing.T) {
	env := newRelayTestEnv(t)
	up := newKeepAliveUpstream(keepAliveConfig{
		KeepAlives: 3,
		Body: []string{
			`{"choices":[{"delta":{"content":"Hello"}}]}`,
			`{"choices":[{"delta":{"content":" world"}}],"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`,
		},
	})
	defer up.Close()

	model, provider := deepseekModelProvider(up.URL)
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	w := wrapWriter(rec, clientReq)

	body := []byte(`{"model":"deepseek-relay","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	before := env.logCountForUser(t)
	env.h.proxyOpenAIToOpenAI(w, clientReq, body, model, provider, env.newReqLog("/v1/chat/completions"), time.Now())

	out := rec.Body.String()
	if strings.Count(out, ": keep-alive") != 3 {
		t.Errorf("relay dropped/altered keep-alive comment lines: %q", out)
	}
	if !strings.Contains(out, `"content":"Hello"`) || !strings.Contains(out, `"content":" world"`) {
		t.Errorf("relay dropped data frames: %q", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("relay dropped terminal [DONE]: %q", out)
	}

	// Exactly one billing row for a single completed request (T035 no-duplicate).
	if got := env.logCountForUser(t) - before; got != 1 {
		t.Errorf("expected exactly 1 new billing row, got %d", got)
	}
}

// TestRelayRelaysUpstreamRetryAfter (T016): a non-stream upstream 429 carrying
// Retry-After must be relayed to the client with the header intact, and it must
// bill exactly one row (no duplicate on the pre-stream error path, T035).
func TestRelayRelaysUpstreamRetryAfter(t *testing.T) {
	env := newRelayTestEnv(t)
	up := newKeepAliveUpstream(keepAliveConfig{
		Status:      http.StatusTooManyRequests,
		RespHeaders: map[string]string{"Retry-After": "42"},
		RespBody:    `{"error":{"message":"slow down","type":"rate_limit_error"}}`,
	})
	defer up.Close()

	model, provider := deepseekModelProvider(up.URL)
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	w := wrapWriter(rec, clientReq)

	body := []byte(`{"model":"deepseek-relay","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	before := env.logCountForUser(t)
	env.h.proxyOpenAIToOpenAI(w, clientReq, body, model, provider, env.newReqLog("/v1/chat/completions"), time.Now())

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "42" {
		t.Errorf("Retry-After = %q, want relayed \"42\"", got)
	}
	if got := env.logCountForUser(t) - before; got != 1 {
		t.Errorf("expected exactly 1 new billing row on the 429 path, got %d", got)
	}
}

// TestRelayEstimatedUsageWithoutProviderUsage (T035): when the upstream never
// sends a usage payload (the same billing branch as a mid-stream disconnect),
// the request is billed exactly once with estimated usage - and
// cache_miss_tokens stays unset (NULL), since it is only ever recorded from a
// provider-reported value.
func TestRelayEstimatedUsageWithoutProviderUsage(t *testing.T) {
	env := newRelayTestEnv(t)
	// A content frame but NO usage frame, so finalUsage stays nil -> estimated.
	up := newKeepAliveUpstream(keepAliveConfig{
		Body: []string{`{"choices":[{"delta":{"content":"partial"}}]}`},
	})
	defer up.Close()

	model, provider := deepseekModelProvider(up.URL)
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	w := wrapWriter(rec, clientReq)

	body := []byte(`{"model":"deepseek-relay","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	before := env.logCountForUser(t)
	env.h.proxyOpenAIToOpenAI(w, clientReq, body, model, provider, env.newReqLog("/v1/chat/completions"), time.Now())

	if got := env.logCountForUser(t) - before; got != 1 {
		t.Errorf("expected exactly 1 billing row, got %d", got)
	}
	// The stream completed normally (fixture appends [DONE]) but reported no
	// usage, so the row is estimated. cache_miss NULL vs non-NULL persistence is
	// asserted directly against the column in db/request_log_billing_test.go.
	logs, err := env.db.ListRequestLogs(1000, 0, env.user.ID, "")
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("no billing row persisted")
	}
	if !logs[0].UsageEstimated {
		t.Errorf("expected usage_estimated=true when the upstream reported no usage")
	}
}
