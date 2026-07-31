package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
	"github.com/google/uuid"
)

// upstreamTotalTimeout bounds an ENTIRE upstream call including streaming. It
// was 15 minutes, which a long agentic turn (deep reasoning plus a large diff)
// can genuinely reach — and hitting it kills the stream mid-answer with no
// report, which for a coding agent means losing the work. 30 minutes leaves
// real headroom; the 120s idle watchdog below is what actually catches stalls,
// and it does so in seconds rather than minutes.
//
// Overridable so an operator can tune it without a rebuild.
var upstreamTotalTimeout = envDuration("UPSTREAM_TOTAL_TIMEOUT", 30*time.Minute)

func envDuration(name string, fallback time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
		log.Printf("[CONFIG] %s=%q is not a valid duration; using %s", name, raw, fallback)
	}
	return fallback
}

var httpClient = &http.Client{
	Timeout: upstreamTotalTimeout,
	Transport: &http.Transport{
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
		// A hung TCP connect/TLS handshake/response-header wait used to pin a
		// request goroutine (and an upstream connection) for up to the full
		// 15-minute client timeout with no way to distinguish it from a
		// legitimately slow reasoning model. These bound only the
		// connection-establishment phases, not in-progress streaming.
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		// A DeepSeek/reasoning upstream can withhold response headers for up to
		// its documented 10-minute pre-inference window (queue + prompt-cache
		// build) before the first byte. 630s (10m + margin) keeps that
		// legitimate wait from being killed as a stall; in-progress streaming
		// remains governed separately by streamIdleTimeout.
		ResponseHeaderTimeout: 630 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// streamIdleTimeout bounds how long a streaming read loop tolerates total
// silence from the upstream before assuming the connection has stalled.
// Reasoning models can legitimately pause for tens of seconds mid-response,
// so this is generous relative to a typical token cadence while still being
// far short of the 15-minute client ceiling.
const streamIdleTimeout = 120 * time.Second

// armIdleWatchdog closes body if reset is not called within d; a stalled
// bufio.Reader.ReadString then returns promptly with an error instead of
// blocking until the full client timeout. Call reset() after every
// successful read and stop() once the read loop exits normally.
func armIdleWatchdog(body io.Closer, d time.Duration) (reset func(), stop func(), timedOut func() bool) {
	var fired atomic.Bool
	timer := time.AfterFunc(d, func() {
		fired.Store(true)
		log.Printf("[STREAM-IDLE] upstream produced no data for %s - closing the connection", d)
		_ = body.Close()
	})
	return func() { timer.Reset(d) }, func() { timer.Stop() }, fired.Load
}

// maxChatRequestBytes bounds chat/messages request bodies. Large coding-agent
// contexts fit comfortably; this only stops memory-exhaustion abuse.
const maxChatRequestBytes = 24 << 20 // 24 MiB

// maxTranscriptionRequestBytes bounds multipart audio uploads. Generous enough
// for long recordings (roughly 3 hours at a typical speech bitrate) while still
// bounding what a single request can pull into memory.
const maxTranscriptionRequestBytes = 100 << 20 // 100 MiB

// maxAccumulatedTextBytes caps the per-stream fallback text buffer used only to
// estimate completion tokens when the upstream omits usage. Bounding it keeps a
// pathologically long stream from holding its entire response text in memory;
// once the cap is hit the estimate is based on the first 2 MiB, which is more
// than enough signal (and moot whenever real usage is reported).
const maxAccumulatedTextBytes = 2 << 20 // 2 MiB

type noopFlusher struct{}

func (noopFlusher) Flush() {}

// asFlusher returns the writer's Flusher or a no-op, so streaming paths never
// panic on an unchecked type assertion if the writer chain ever changes.
func asFlusher(w http.ResponseWriter) http.Flusher {
	if f, ok := w.(http.Flusher); ok {
		return f
	}
	return noopFlusher{}
}

type ProxyHandler struct {
	db       *db.DB
	limiter  *RateLimiter
	outbox   *logOutbox
	affinity *routeAffinityStore
	// identitySecret is the HMAC key used to derive a stable, opaque upstream
	// "user" identifier (see DeriveUserID). Empty disables identity injection.
	identitySecret string
}

func NewProxyHandler(database *db.DB, limiter *RateLimiter, identitySecret string) *ProxyHandler {
	return &ProxyHandler{
		db:             database,
		limiter:        limiter,
		outbox:         newLogOutbox(database),
		affinity:       newRouteAffinityStore(),
		identitySecret: identitySecret,
	}
}

// routerModelDeprecated is the legacy virtual model name that used to select a
// model automatically by task complexity. Automatic/cross-model routing was
// removed to enforce the single-active-model invariant: the model selected for
// a session is the only model that may generate any token for that session.
// Requests naming it must fail explicitly so they never silently resolve to a
// default model; the message tells the caller how to migrate.
const routerModelDeprecated = "muhiya-ai-router"

// clientRequestIDHeader is the logical request ID sent by MuhiyaCode. The
// gateway combines it with the authenticated key and attempt number to derive
// a stable row ID, keeping retries observable and same-attempt replays
// idempotent.
const clientRequestIDHeader = "X-Muhiya-Request-ID"
const clientAttemptHeader = "X-Muhiya-Attempt"
const clientCacheEpochHeader = "X-Muhiya-Cache-Epoch"

type requestCorrelation struct {
	LogID           string
	ClientRequestID string
	AttemptNumber   int
}

// requestCorrelationFor creates one stable request-log identity per client
// attempt. Replaying the same attempt is idempotent; a transport retry increments
// X-Muhiya-Attempt and receives a distinct row under the same logical request.
func requestCorrelationFor(r *http.Request, keyID string) requestCorrelation {
	attempt := requestAttemptNumber(r)
	logicalID := strings.TrimSpace(r.Header.Get(clientRequestIDHeader))
	if logicalID == "" {
		return requestCorrelation{LogID: uuid.New().String(), AttemptNumber: attempt}
	}
	if !validLogicalRequestID(logicalID) {
		log.Printf("[REQUEST] ignored malformed %s header", clientRequestIDHeader)
		return requestCorrelation{LogID: uuid.New().String(), AttemptNumber: attempt}
	}
	digest := sha256.Sum256([]byte(keyID + "\x00" + logicalID + "\x00" + strconv.Itoa(attempt)))
	return requestCorrelation{
		LogID:           fmt.Sprintf("req-%x", digest[:]),
		ClientRequestID: logicalID,
		AttemptNumber:   attempt,
	}
}

func requestAttemptNumber(r *http.Request) int {
	raw := strings.TrimSpace(r.Header.Get(clientAttemptHeader))
	if raw == "" {
		return 1
	}
	attempt, err := strconv.Atoi(raw)
	if err != nil || attempt < 1 || attempt > 100 {
		log.Printf("[REQUEST] ignored malformed %s header", clientAttemptHeader)
		return 1
	}
	return attempt
}

func validLogicalRequestID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, char := range id {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func requestSessionID(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Muhiya-Session"))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("X-Session-Id"))
	}
	if len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func requestCacheEpoch(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get(clientCacheEpochHeader))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func applyRequestContext(entry *db.RequestLog, r *http.Request, key *db.VirtualKey, identity requestCorrelation) {
	entry.ID = identity.LogID
	entry.VirtualKeyID = key.ID
	entry.UserID = key.UserID
	entry.SessionID = requestSessionID(r)
	entry.ClientRequestID = identity.ClientRequestID
	entry.AttemptNumber = identity.AttemptNumber
	entry.CacheEpoch = requestCacheEpoch(r)
}

func (h *ProxyHandler) rejectDeprecatedRouterModel(w http.ResponseWriter, requested string) {
	h.writeError(w, http.StatusBadRequest,
		"Model '"+requested+"' is no longer supported. The automatic model router was removed: "+
			"select a concrete model for the session (e.g. via /model) and send its exact name as the "+
			"`model` parameter. See the MuhiyaCode single-model migration guide.",
		"invalid_request_error")
}

// internalErrorResponse logs the real error server-side (with a short
// context label) and writes a generic message to the client - a raw
// err.Error() on the public proxy endpoint risked leaking internal
// schema/driver/query details to any caller with a valid key.
func (h *ProxyHandler) internalErrorResponse(w http.ResponseWriter, context string, err error) {
	log.Printf("[ERROR] %s: %v", context, err)
	h.writeError(w, http.StatusInternalServerError, "Internal server error", "api_error")
}

// serviceUnavailableResponse signals a transient backend outage (typically the
// database being briefly unreachable) with a retryable 503 + Retry-After rather
// than a 500. A 500 reads as "your request is broken, do not retry"; a 503 tells
// well-behaved clients (and the MuhiyaCode agent) to back off and try again, so a
// momentary Postgres blip during auth no longer surfaces as a hard sign-in error.
func (h *ProxyHandler) serviceUnavailableResponse(w http.ResponseWriter, context string, err error) {
	log.Printf("[UNAVAILABLE] %s: %v", context, err)
	w.Header().Set("Retry-After", "2")
	h.writeError(w, http.StatusServiceUnavailable, "Gateway temporarily unavailable, please retry.", "api_error")
}

// saveRequestLog persists one request's billing/usage row. A failed insert is
// never silently discarded: it is logged immediately and handed to a
// bounded, backoff-retrying outbox so a transient database blip cannot lose
// billing history (see outbox.go).
func (h *ProxyHandler) saveRequestLog(entry db.RequestLog) {
	var err error
	if entry.ChargeCeilingNanoUSD > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = h.db.SettleUsageAndLog(ctx, entry)
		cancel()
	} else {
		err = h.db.InsertRequestLog(entry)
	}
	if err != nil {
		log.Printf("[BILLING-ERROR] failed to insert request log %s: %v - queued for retry", entry.ID, err)
		if h.outbox != nil {
			h.outbox.enqueue(entry)
		}
		return
	}
	event, _ := json.Marshal(map[string]any{
		"event": "request_settled", "request_log_id": entry.ID,
		"client_request_id": entry.ClientRequestID, "attempt": entry.AttemptNumber,
		"user_id": entry.UserID, "session_id": entry.SessionID,
		"provider_id": entry.ProviderID, "model_id": entry.ModelID,
		"status": db.RequestStatusForHTTP(entry.StatusCode), "status_code": entry.StatusCode,
		"cost_nano_usd": entry.CostNanoUSD, "budget_window_id": entry.BudgetWindowID,
	})
	log.Printf("%s", event)
}

type responseWriterWithRequest struct {
	http.ResponseWriter
	req *http.Request
}

func (w *responseWriterWithRequest) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *ProxyHandler) getRequest(w http.ResponseWriter) *http.Request {
	if wrap, ok := w.(*responseWriterWithRequest); ok {
		return wrap.req
	}
	return nil
}

func isAnthropicRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.Contains(r.URL.Path, "/messages") {
		return true
	}
	if r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" {
		return true
	}
	return false
}

func getClientAppName(r *http.Request) string {
	if r == nil {
		return "Unknown"
	}
	if clientApp := r.Header.Get("X-Client-App"); clientApp != "" {
		return clientApp
	}
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		if r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" {
			return "Anthropic Client"
		}
		return "API Client"
	}
	uaLower := strings.ToLower(ua)
	if strings.Contains(uaLower, "claude-code") || strings.Contains(uaLower, "claude-cli") {
		return "Claude Code"
	}
	if strings.Contains(uaLower, "anthropic") {
		return "Anthropic SDK"
	}
	if strings.Contains(uaLower, "openai-python") {
		return "OpenAI Python"
	}
	if strings.Contains(uaLower, "openai-node") {
		return "OpenAI Node"
	}
	if strings.Contains(uaLower, "openai") {
		return "OpenAI SDK"
	}
	if strings.Contains(uaLower, "curl") {
		return "curl"
	}
	if strings.Contains(uaLower, "mozilla") || strings.Contains(uaLower, "safari") || strings.Contains(uaLower, "chrome") {
		return "Web Browser"
	}
	if strings.Contains(uaLower, "postman") {
		return "Postman"
	}
	if len(ua) > 20 {
		return ua[:17] + "..."
	}
	return ua
}

type openaiErrorPayload struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
}

type anthropicErrorPayload struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func translateErrorBytes(respBytes []byte, clientIsAnthropic bool) []byte {
	if len(respBytes) == 0 {
		return respBytes
	}

	// Try parsing as Anthropic error
	var anthErr anthropicErrorPayload
	isAnth := json.Unmarshal(respBytes, &anthErr) == nil && anthErr.Type == "error" && anthErr.Error.Message != ""

	// Try parsing as OpenAI error
	var oaiErr openaiErrorPayload
	isOAI := json.Unmarshal(respBytes, &oaiErr) == nil && oaiErr.Error.Message != ""

	if clientIsAnthropic {
		if isAnth {
			return respBytes
		}
		if isOAI {
			translated := anthropicErrorPayload{
				Type: "error",
			}
			translated.Error.Type = oaiErr.Error.Type
			if translated.Error.Type == "" {
				translated.Error.Type = "api_error"
			}
			translated.Error.Message = oaiErr.Error.Message
			out, err := json.Marshal(translated)
			if err == nil {
				return out
			}
		}
		// Fallback: wrap unknown error in Anthropic format
		translated := anthropicErrorPayload{
			Type: "error",
		}
		translated.Error.Type = "api_error"
		translated.Error.Message = string(respBytes)
		out, err := json.Marshal(translated)
		if err == nil {
			return out
		}
	} else {
		// Client is OpenAI
		if isOAI {
			return respBytes
		}
		if isAnth {
			translated := openaiErrorPayload{}
			translated.Error.Message = anthErr.Error.Message
			translated.Error.Type = anthErr.Error.Type
			if translated.Error.Type == "" {
				translated.Error.Type = "api_error"
			}
			out, err := json.Marshal(translated)
			if err == nil {
				return out
			}
		}
		// Fallback: wrap unknown error in OpenAI format
		translated := openaiErrorPayload{}
		translated.Error.Message = string(respBytes)
		translated.Error.Type = "api_error"
		out, err := json.Marshal(translated)
		if err == nil {
			return out
		}
	}

	return respBytes
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = &responseWriterWithRequest{ResponseWriter: w, req: r}

	if r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/v1/muhiyacode/models" {
		if _, ok := h.authenticateVirtualKey(w, r); !ok {
			return
		}
		h.handleMuhiyaCodeCatalog(w, r)
		return
	}

	// Handle GET /v1/models and /v1/models/{id} endpoints (Model Discovery).
	// Requires a valid virtual key, same as /capabilities and
	// /tools/web_search below: an anonymous caller was previously able to
	// enumerate every configured model (and trigger a DB query per hit) with
	// no credential at all.
	if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/models") || strings.Contains(r.URL.Path, "/models/")) {
		if _, ok := h.authenticateVirtualKey(w, r); !ok {
			return
		}
		h.handleModelDiscovery(w, r)
		return
	}

	// GET /v1/pricing: the effective price of every visible model right now,
	// including which time-of-day window is in force and when it next changes.
	// Authenticated like /models — prices are catalog data, not public.
	if strings.HasSuffix(r.URL.Path, "/pricing") {
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
			return
		}
		if _, ok := h.authenticateVirtualKey(w, r); !ok {
			return
		}
		h.handlePricing(w, r)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/capabilities") {
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
			return
		}
		key, ok := h.authenticateVirtualKey(w, r)
		if !ok {
			return
		}
		h.handleCapabilities(w, r, key)
		return
	}

	// 003 (T006): self-service usage for the caller's own key. Authenticated by
	// the virtual key (no admin credentials), exempt from RPM/TPM counting.
	if strings.HasSuffix(r.URL.Path, "/usage") {
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
			return
		}
		key, ok := h.authenticateVirtualKey(w, r)
		if !ok {
			return
		}
		h.handleUsage(w, key)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/tools/web_search") {
		if r.Method != http.MethodPost {
			h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
			return
		}
		key, ok := h.authenticateVirtualKey(w, r)
		if !ok {
			return
		}
		// Bounded the same way the chat body is below - an authenticated but
		// unbounded io.ReadAll here was a memory-exhaustion vector.
		r.Body = http.MaxBytesReader(w, r.Body, maxChatRequestBytes)
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			if strings.Contains(err.Error(), "http: request body too large") {
				h.writeError(w, http.StatusRequestEntityTooLarge, "Request body exceeds the maximum allowed size.", "invalid_request_error")
				return
			}
			h.writeError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error")
			return
		}
		r.Body.Close()
		h.handleGatewayWebSearch(w, r, bodyBytes, key)
		return
	}

	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
		return
	}

	// 1. Authenticate Virtual Key (Support Authorization: Bearer OR x-api-key)
	key, ok := h.authenticateVirtualKey(w, r)
	if !ok {
		return
	}

	// 2. Read Request Body (bounded to prevent memory-exhaustion DoS).
	//
	// Transcription gets a LARGER cap, not no cap. The previous comment claimed
	// it was "capped separately in its handler", but the only limit there is
	// ParseMultipartForm's 10 MiB, which is the in-memory spill threshold — not
	// a total size bound — and it runs AFTER this ReadAll has already pulled the
	// whole body into memory. A single multi-gigabyte upload could exhaust the
	// process before any handler code ran.
	bodyLimit := int64(maxChatRequestBytes)
	if strings.Contains(r.URL.Path, "/audio/transcriptions") {
		bodyLimit = maxTranscriptionRequestBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			h.writeError(w, http.StatusRequestEntityTooLarge, "Request body exceeds the maximum allowed size.", "invalid_request_error")
			return
		}
		h.writeError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error")
		return
	}
	r.Body.Close()

	// 3. Detect Route Protocol
	isAnthropicRoute := strings.Contains(r.URL.Path, "/messages")
	isTranscriptionRoute := strings.Contains(r.URL.Path, "/audio/transcriptions")

	if isTranscriptionRoute {
		h.serveTranscriptionClient(w, r, bodyBytes, key)
	} else if isAnthropicRoute {
		h.serveAnthropicClient(w, r, bodyBytes, key)
	} else {
		h.serveOpenAIClient(w, r, bodyBytes, key)
	}
}

func (h *ProxyHandler) authenticateVirtualKey(w http.ResponseWriter, r *http.Request) (*db.VirtualKey, bool) {
	keyID := ""
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		keyID = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		keyID = r.Header.Get("x-api-key")
	}

	if keyID == "" {
		h.writeError(w, http.StatusUnauthorized, "Missing or invalid credentials. Use Bearer token or x-api-key.", "invalid_request_error")
		return nil, false
	}

	key, err := h.db.GetVirtualKey(keyID)
	if err != nil {
		// A transient DB error during auth is not a client fault: return a
		// retryable 503 instead of a 500 so a momentary Postgres blip does not
		// surface to the user as an unrecoverable sign-in failure.
		h.serviceUnavailableResponse(w, "auth: database error resolving virtual key", err)
		return nil, false
	}
	if key == nil || key.Status != "active" {
		h.writeError(w, http.StatusUnauthorized, "Invalid or revoked virtual key.", "invalid_request_error")
		return nil, false
	}

	if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
		h.writeError(w, http.StatusUnauthorized, "Virtual key has expired.", "invalid_request_error")
		return nil, false
	}

	// A suspended owner cannot transact on ANY route. This lives here rather
	// than in the rate limiter because the limiter is called per handler, and a
	// handler that forgot to call it (transcription did) honored the suspension
	// for nobody — an operator's explicit revocation silently did nothing.
	// Authentication is the one place every route passes through.
	if key.OwnerStatus != "" && key.OwnerStatus != "active" {
		h.writeError(w, http.StatusForbidden, "This account is suspended.", "permission_error")
		return nil, false
	}
	return key, true
}

// handleUsage returns the caller's account usage (003 T006, usage-api.md §2):
// plan budget windows (USD, with current spend + reset), extra credits, and
// today's spend. Composed from existing db methods plus GetUserSpendingToday.
func (h *ProxyHandler) handleUsage(w http.ResponseWriter, key *db.VirtualKey) {
	user, err := h.db.GetUser(key.UserID)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	if user == nil {
		h.writeError(w, http.StatusUnauthorized, "Account not found for this key.", "invalid_request_error")
		return
	}
	today, err := h.db.GetUserSpendingToday(user.ID)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	planName := user.PlanID
	if plan, planErr := h.db.GetPlan(user.PlanID); planErr == nil && plan != nil && plan.Name != "" {
		planName = plan.Name
	}
	windows := make([]map[string]interface{}, 0, len(user.BudgetUsage))
	for _, wnd := range user.BudgetUsage {
		reset := ""
		if wnd.ResetTime != nil {
			reset = wnd.ResetTime.UTC().Format(time.RFC3339)
		}
		windows = append(windows, map[string]interface{}{
			"name":              wnd.Name,
			"duration_seconds":  wnd.DurationSeconds,
			"budget_usd":        wnd.BudgetUSD,
			"current_spent_usd": wnd.CurrentSpent,
			"reset_time":        reset,
		})
	}
	response := map[string]interface{}{
		"user": map[string]interface{}{"id": user.ID, "name": user.Name},
		"plan": map[string]interface{}{"name": planName, "windows": windows},
		"credits": map[string]interface{}{
			"extra_total":     user.ExtraCredits,
			"extra_remaining": user.RemainingExtraCredits,
		},
		"spend": map[string]interface{}{"today_usd": today},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (h *ProxyHandler) handleCapabilities(w http.ResponseWriter, _ *http.Request, _ *db.VirtualKey) {
	settings := LoadToolSettings(h.db)
	schemas := BuildToolSchemas(settings)
	tools := make([]map[string]interface{}, 0, len(schemas))
	for _, schema := range schemas {
		tools = append(tools, map[string]interface{}{
			"name":        schema.Function.Name,
			"available":   true,
			"description": schema.Function.Description,
			"parameters":  schema.Function.Parameters,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"object":  "muhiya.capabilities",
		"gateway": "MuhiyaLLM Gateway",
		"features": map[string]interface{}{
			"web_search": settings.WebSearchEnabled(),
		},
		"tools": tools,
	})
}

func (h *ProxyHandler) handleGatewayWebSearch(w http.ResponseWriter, _ *http.Request, bodyBytes []byte, _ *db.VirtualKey) {
	settings := LoadToolSettings(h.db)
	if !settings.WebSearchEnabled() {
		h.writeError(w, http.StatusNotImplemented, "web_search is not configured on this gateway.", "unsupported_feature")
		return
	}
	var req WebSearchRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid web_search JSON body: "+err.Error(), "invalid_request_error")
		return
	}
	tc := &ToolContext{DB: h.db, Settings: settings, Complexity: "medium"}
	result, err := tc.RunWebSearch(req)
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "query is required") {
			status = http.StatusBadRequest
		}
		h.writeError(w, status, err.Error(), "api_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// ------------------------------------------
// OpenAI Client Routing (Incoming: OpenAI format)
// ------------------------------------------
func (h *ProxyHandler) serveOpenAIClient(w http.ResponseWriter, r *http.Request, bodyBytes []byte, key *db.VirtualKey) {
	// Pin the pricing instant before admission so the quote and the final
	// charge cannot land on opposite sides of a peak-pricing boundary.
	r = pinPricedAt(r)
	var oaiReq OpenAIRequest
	if err := json.Unmarshal(bodyBytes, &oaiReq); err != nil {
		// Never log request bodies: they routinely contain user prompts and
		// proprietary source code. A length + truncated-prefix is enough to
		// debug a malformed-JSON report without leaking content into logs.
		log.Printf("[ERROR] failed to unmarshal OpenAI request (%d bytes): %v", len(bodyBytes), err)
		h.writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error(), "invalid_request_error")
		return
	}

	if oaiReq.Model == "" {
		h.writeError(w, http.StatusBadRequest, "Model parameter is required", "invalid_request_error")
		return
	}

	// MuhiyaChat native agent loop: when tools are configured and the client is
	// MuhiyaChat, run the in-gateway tool loop. It returns false (and does not
	// write a response) when the resolved provider is Anthropic-format, so the
	// standard proxy path below handles it. All other traffic skips this.
	if toolSettings := LoadToolSettings(h.db); h.shouldRunAgentLoop(r, &oaiReq, toolSettings) {
		if h.serveMuhiyaAgent(w, r, &oaiReq, key, toolSettings) {
			return
		}
	}

	// Canonical thinking level for this request (header > body). An explicit
	// client `thinking` object is NOT a level: it passes through untouched.
	thinkingLevel := ResolveThinkingLevel(r, oaiReq.ReasoningEffort)

	// The automatic model router was removed (single-active-model invariant).
	// Legacy router requests fail explicitly; they must never resolve to a
	// default model. The caller selects the exact model for the session.
	if oaiReq.Model == routerModelDeprecated {
		h.rejectDeprecatedRouterModel(w, oaiReq.Model)
		return
	}

	targetModel, err := h.db.GetModelByName(oaiReq.Model)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	if targetModel == nil {
		h.writeError(w, http.StatusNotFound, fmt.Sprintf("Model '%s' not found or inactive", oaiReq.Model), "invalid_request_error")
		return
	}
	if !h.establishModelResolution(w, r, targetModel) {
		return
	}
	// One model, one attempt. Cross-model failover was removed: it could
	// silently substitute a different model mid-session and bust the upstream
	// prefix cache, violating the single-active-model invariant. A transient
	// upstream failure is reported to the caller rather than rerouted.
	provider, err := h.db.GetProvider(targetModel.ProviderID)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	if provider == nil || provider.Status != "active" {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("Provider for model '%s' is unavailable", targetModel.Name), "api_error")
		return
	}

	// Estimate prompt tokens once for rate/budget admission.
	var textBuilder strings.Builder
	for _, m := range oaiReq.Messages {
		textBuilder.WriteString(GetMessageContentString(m.Content))
	}
	promptTokens := estimateTokens(textBuilder.String())
	requestedOutput := requestedOpenAIOutput(&oaiReq, targetModel)
	correlation := requestCorrelationFor(r, key.ID)
	requestID := correlation.LogID
	if err := h.limiter.CheckLimit(key, promptTokens+requestedOutput); err != nil {
		h.saveAdmissionFailure(admissionFailure{
			request: r, key: key, model: targetModel, provider: provider,
			requestID: requestID, requestedModel: oaiReq.Model,
			inputTokens: promptTokens, status: http.StatusTooManyRequests, err: err,
		})
		h.writeLimitError(w, err)
		return
	}
	releaseGeneration, err := h.limiter.AcquireGeneration(r.Context(), key.UserID, requestID)
	if err != nil {
		h.saveAdmissionFailure(admissionFailure{
			request: r, key: key, model: targetModel, provider: provider,
			requestID: requestID, requestedModel: oaiReq.Model,
			inputTokens: promptTokens, status: http.StatusServiceUnavailable, err: err,
		})
		h.serviceUnavailableResponse(w, "generation guard failed", err)
		return
	}
	defer releaseGeneration()

	var targetURL string
	var useAnthropicUpstream bool

	if provider.BaseURL != "" {
		targetURL = provider.BaseURL
		useAnthropicUpstream = false
	} else if provider.AnthropicBaseURL != "" {
		targetURL = provider.AnthropicBaseURL
		useAnthropicUpstream = true
	} else {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("Provider '%s' has no base URL", provider.ID), "api_error")
		return
	}

	resolvedProvider := *provider
	resolvedProvider.BaseURL = targetURL

	allowedOutput, chargeCeiling, budgetWindow, err := h.affordableGeneration(r.Context(), generationQuoteRequest{
		userID:          key.UserID,
		model:           targetModel,
		inputUpperBound: conservativeInputTokenBound(bodyBytes, promptTokens, targetModel),
		requestedOutput: requestedOutput,
		pricedAt:        pricedAtFrom(r.Context()),
	})
	if err != nil {
		h.saveAdmissionFailure(admissionFailure{
			request: r, key: key, model: targetModel, provider: provider,
			requestID: requestID, requestedModel: oaiReq.Model,
			inputTokens: promptTokens, status: budgetErrorStatus(err), err: err,
			budget: budgetWindow,
		})
		h.writeBudgetError(w, err)
		return
	}
	bodyBytes, err = rewriteOpenAIOutputLimit(bodyBytes, allowedOutput)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error(), "invalid_request_error")
		return
	}

	reqCopy := oaiReq
	reqCopy.Model = targetModel.Name
	reqCopy.MaxTokens = &allowedOutput
	reqCopy.MaxCompletionTokens = nil

	startTime := time.Now()
	reqLog := db.RequestLog{
		ModelID:              targetModel.ID,
		ProviderID:           provider.ID,
		ChargeCeilingNanoUSD: chargeCeiling,
		RequestPath:          r.URL.Path,
		InputTokens:          promptTokens,
		ClientApp:            getClientAppName(r),
		RequestedModel:       oaiReq.Model,
		Complexity:           "direct",
		ThinkingLevel:        thinkingLevel,
		Streamed:             oaiReq.Stream,
		CreatedAt:            startTime,
	}
	applyRequestContext(&reqLog, r, key, correlation)
	applyBudgetWindow(&reqLog, budgetWindow)

	if useAnthropicUpstream {
		h.proxyOpenAIToAnthropic(w, r, &reqCopy, targetModel, &resolvedProvider, reqLog, startTime)
	} else {
		// Forward the client's ORIGINAL bytes, not a re-marshal of the
		// lossy typed struct: proxyOpenAIToOpenAI already unmarshals its
		// origBody into a map and overwrites "model" there, so passing
		// bodyBytes directly preserves every field the client sent
		// (reasoning_content, stop, response_format, seed, ...) instead
		// of silently dropping anything OpenAIRequest doesn't declare.
		h.proxyOpenAIToOpenAI(w, r, bodyBytes, targetModel, &resolvedProvider, reqLog, startTime)
	}
}

// ------------------------------------------
// Anthropic Client Routing (Incoming: Anthropic format)
// ------------------------------------------
func (h *ProxyHandler) serveAnthropicClient(w http.ResponseWriter, r *http.Request, bodyBytes []byte, key *db.VirtualKey) {
	// Pin the pricing instant before admission so the quote and the final
	// charge cannot land on opposite sides of a peak-pricing boundary.
	r = pinPricedAt(r)
	var anthReq AnthropicRequest
	if err := json.Unmarshal(bodyBytes, &anthReq); err != nil {
		// See serveOpenAIClient: never log request bodies.
		log.Printf("[ERROR] failed to unmarshal Anthropic request (%d bytes): %v", len(bodyBytes), err)
		h.writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error(), "invalid_request_error")
		return
	}

	if anthReq.Model == "" {
		h.writeError(w, http.StatusBadRequest, "Model parameter is required", "invalid_request_error")
		return
	}

	// Same canonical thinking resolution as the OpenAI path; the Anthropic
	// request struct also accepts reasoning_effort for symmetric clients.
	// A native `thinking` object is the client's own control - not a level.
	thinkingLevel := ResolveThinkingLevel(r, anthReq.ReasoningEffort)

	// The automatic model router was removed (single-active-model invariant).
	if anthReq.Model == routerModelDeprecated {
		h.rejectDeprecatedRouterModel(w, anthReq.Model)
		return
	}

	targetModel, err := h.db.GetModelByName(anthReq.Model)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	if targetModel == nil {
		h.writeError(w, http.StatusNotFound, fmt.Sprintf("Model '%s' not found or inactive", anthReq.Model), "invalid_request_error")
		return
	}
	if !h.establishModelResolution(w, r, targetModel) {
		return
	}

	// One model, one attempt. Cross-model failover was removed (see
	// serveOpenAIClient). A transient upstream failure is reported, never
	// rerouted to a different model.
	provider, err := h.db.GetProvider(targetModel.ProviderID)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	if provider == nil || provider.Status != "active" {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("Provider for model '%s' is unavailable", targetModel.Name), "api_error")
		return
	}

	// Estimate prompt tokens and check limits once.
	var textBuilder strings.Builder
	textBuilder.WriteString(string(anthReq.System))
	for _, m := range anthReq.Messages {
		for _, b := range m.Content {
			textBuilder.WriteString(b.Text)
			textBuilder.WriteString(b.Thinking)
		}
	}
	promptTokens := estimateTokens(textBuilder.String())
	requestedOutput := boundedOutputLimit(anthReq.MaxTokens, targetModel)
	correlation := requestCorrelationFor(r, key.ID)
	requestID := correlation.LogID
	if err := h.limiter.CheckLimit(key, promptTokens+requestedOutput); err != nil {
		h.saveAdmissionFailure(admissionFailure{
			request: r, key: key, model: targetModel, provider: provider,
			requestID: requestID, requestedModel: anthReq.Model,
			inputTokens: promptTokens, status: http.StatusTooManyRequests, err: err,
		})
		h.writeLimitError(w, err)
		return
	}
	releaseGeneration, err := h.limiter.AcquireGeneration(r.Context(), key.UserID, requestID)
	if err != nil {
		h.saveAdmissionFailure(admissionFailure{
			request: r, key: key, model: targetModel, provider: provider,
			requestID: requestID, requestedModel: anthReq.Model,
			inputTokens: promptTokens, status: http.StatusServiceUnavailable, err: err,
		})
		h.serviceUnavailableResponse(w, "generation guard failed", err)
		return
	}
	defer releaseGeneration()

	var targetURL string
	var useAnthropicUpstream bool

	if provider.BaseURL != "" {
		targetURL = provider.BaseURL
		useAnthropicUpstream = false
	} else if provider.AnthropicBaseURL != "" {
		targetURL = provider.AnthropicBaseURL
		useAnthropicUpstream = true
	} else {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("Provider '%s' has no base URL", provider.ID), "api_error")
		return
	}

	resolvedProvider := *provider
	resolvedProvider.BaseURL = targetURL

	allowedOutput, chargeCeiling, budgetWindow, err := h.affordableGeneration(r.Context(), generationQuoteRequest{
		userID:          key.UserID,
		model:           targetModel,
		inputUpperBound: conservativeInputTokenBound(bodyBytes, promptTokens, targetModel),
		requestedOutput: requestedOutput,
		pricedAt:        pricedAtFrom(r.Context()),
	})
	if err != nil {
		h.saveAdmissionFailure(admissionFailure{
			request: r, key: key, model: targetModel, provider: provider,
			requestID: requestID, requestedModel: anthReq.Model,
			inputTokens: promptTokens, status: budgetErrorStatus(err), err: err,
			budget: budgetWindow,
		})
		h.writeBudgetError(w, err)
		return
	}

	reqCopy := anthReq
	reqCopy.Model = targetModel.Name
	reqCopy.MaxTokens = allowedOutput

	startTime := time.Now()
	reqLog := db.RequestLog{
		ModelID:              targetModel.ID,
		ProviderID:           provider.ID,
		ChargeCeilingNanoUSD: chargeCeiling,
		RequestPath:          r.URL.Path,
		InputTokens:          promptTokens,
		ClientApp:            getClientAppName(r),
		RequestedModel:       anthReq.Model,
		Complexity:           "direct",
		ThinkingLevel:        thinkingLevel,
		Streamed:             anthReq.Stream,
		CreatedAt:            startTime,
	}
	applyRequestContext(&reqLog, r, key, correlation)
	applyBudgetWindow(&reqLog, budgetWindow)

	// Perform proxy
	if useAnthropicUpstream {
		newBody, _ := json.Marshal(reqCopy)
		h.proxyAnthropicToAnthropic(w, r, newBody, &reqCopy, targetModel, &resolvedProvider, reqLog, startTime)
	} else {
		h.proxyAnthropicToOpenAI(w, r, &reqCopy, targetModel, &resolvedProvider, reqLog, startTime)
	}
}

// ------------------------------------------
// Proxies Implementation
// ------------------------------------------

// sanitizeUpstreamIdentity conditions an OpenAI-dialect body before it is
// forwarded (feature 007 upstream-request §2/§3). For every upstream it drops
// any caller-supplied identity fields (user, user_id), then, when identity
// injection is configured, stamps a stable opaque per-caller user_id derived
// server-side - dropping the inbound fields first guarantees the caller can
// never forge or leak one. The sampling-knob strip (frequency_penalty,
// presence_penalty) applies to DeepSeek ONLY: DeepSeek rejects those params,
// while GLM/OpenAI/others support them legitimately and must receive them
// untouched.
func sanitizeUpstreamIdentity(bodyMap map[string]interface{}, family upstreamFamily, identitySecret, userID string) {
	if family == famDeepseek {
		delete(bodyMap, "frequency_penalty")
		delete(bodyMap, "presence_penalty")
	}
	delete(bodyMap, "user")
	delete(bodyMap, "user_id")
	if derived := DeriveUserID(identitySecret, userID); derived != "" {
		bodyMap["user_id"] = derived
	}
}

// setOpenRouterHeaders adds OpenRouter-specific request headers when the
// upstream provider is OpenRouter: attribution (rankings) and X-Session-Id.
// No-op for every other provider.
//
// X-Session-Id is a TRACE header, nothing more. This comment used to claim it
// "pins the same underlying provider across a conversation so the prompt cache
// stays warm" — that guarantee does not exist. OpenRouter documents no
// session-based or sticky routing feature; upstream selection is controlled
// only by the request body's `provider` object (order / only / allow_fallbacks).
// The false claim mattered: it sent an investigation into a real, expensive
// per-upstream cache-miss bug looking anywhere but here.
//
// Upstream affinity is owned by the gateway. It scopes the observed provider to
// the virtual key, session, and immutable model record, then injects a strict
// provider order before forwarding the request. Clients supply only the
// session identity and never select a hidden model or provider.
func setOpenRouterHeaders(req *http.Request, provider *db.Provider, r *http.Request) {
	if provider == nil || provider.ID != "openrouter" {
		return
	}
	req.Header.Set("HTTP-Referer", "https://muhiya.com")
	req.Header.Set("X-Title", "Muhiya")
	if sess := r.Header.Get("X-Muhiya-Session"); sess != "" {
		req.Header.Set("X-Session-Id", sess)
	}
}

func (h *ProxyHandler) proxyOpenAIToOpenAI(w http.ResponseWriter, r *http.Request, origBody []byte, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	var bodyMap map[string]interface{}
	// A body that decodes to a non-object (e.g. the literal `null`, which the
	// typed request decode accepts as a zero value) leaves bodyMap nil; assigning
	// to a nil map panics. Reject it as a 400 instead of turning it into an opaque
	// recovered 500.
	if err := json.Unmarshal(origBody, &bodyMap); err != nil || bodyMap == nil {
		h.logAndWriteError(w, http.StatusBadRequest, "Request body must be a JSON object", "invalid_request_error", &log, startTime)
		return
	}
	bodyMap["model"] = model.TargetModel
	delete(bodyMap, "web_search")

	// Media parts the target cannot accept are dropped with an honest text note
	// (providers 400 on unsupported parts, which would otherwise poison every
	// later turn of the conversation). Router-selected models already support
	// everything present, so this is a no-op on the router path. Any document
	// part that SURVIVES the strip and heads to OpenRouter pins the free PDF
	// parsing engine (strip first, then inject — never plug in a parser for a
	// part that was just removed).
	stripUnsupportedMediaInBodyMap(bodyMap, model)
	maybeInjectOpenRouterPDFParser(bodyMap, provider, h.openRouterPDFEngine())

	// Translate the canonical thinking level into this provider's dialect
	// (and strip the gateway-level fields regardless).
	applied := ApplyThinkingOpenAI(bodyMap, provider.BaseURL, model.TargetModel, log.ThinkingLevel)
	log.ThinkingLevel = ThinkingLogValue(log.ThinkingLevel, applied)

	// OpenRouter → Claude only: add cache_control breakpoints. Auto-caching
	// targets (DeepSeek, GLM, Kimi, ...) are left byte-for-byte untouched so
	// their prefix caches keep hitting; this is a strict no-op for them.
	InjectOpenRouterAnthropicCache(bodyMap, provider != nil && provider.ID == "openrouter", model.TargetModel)

	stream, _ := bodyMap["stream"].(bool)
	if stream {
		bodyMap["stream_options"] = map[string]interface{}{
			"include_usage": true,
		}
	}

	sanitizeUpstreamIdentity(bodyMap, classifyUpstream(provider.BaseURL, model.TargetModel), h.identitySecret, log.UserID)
	// OpenRouter reads the standard `user` field as the stable end-user
	// identifier — its documented purpose, and the field sanitizeUpstreamIdentity
	// strips for every upstream because most of them do not want it. Restoring it
	// here gives OpenRouter correct per-user attribution, and it is also the
	// field OpenRouter's own routing affinity is documented against, so it
	// complements the X-Session-Id header set below. Deterministic per caller, so
	// the request body stays byte-stable across a conversation's turns.
	if provider.ID == "openrouter" {
		if derived := DeriveUserID(h.identitySecret, log.UserID); derived != "" {
			bodyMap["user"] = derived
		}
	}
	routeScope := h.applyProviderAffinity(r.Context(), bodyMap, r, model, &log)

	newBody, _ := json.Marshal(bodyMap)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") && !strings.HasSuffix(url, "/completions") {
		url += "/chat/completions"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	setOpenRouterHeaders(req, provider, r)

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Connection failed: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()
	if _, wasPinned := bodyMap["provider"]; wasPinned && routeScope != "" && resp.StatusCode >= 400 {
		// One same-model rebind is allowed only before any downstream bytes.
		// Clear the rejected route and retry the byte-identical model request
		// without provider.order; never retry a partial stream.
		originalStatus := resp.StatusCode
		originalHeader := resp.Header.Clone()
		originalBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		clearCtx, clearCancel := context.WithTimeout(context.Background(), time.Second)
		_ = h.affinity.clear(clearCtx, routeScope)
		clearCancel()
		delete(bodyMap, "provider")
		unpinnedBody, marshalErr := json.Marshal(bodyMap)
		if marshalErr != nil {
			h.logFailedUpstream(w, originalStatus, originalBody, originalHeader, &log, startTime)
			return
		}
		retryReq, requestErr := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(unpinnedBody))
		if requestErr != nil {
			h.logFailedUpstream(w, originalStatus, originalBody, originalHeader, &log, startTime)
			return
		}
		retryReq.Header.Set("Content-Type", "application/json")
		retryReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
		setOpenRouterHeaders(retryReq, provider, r)
		retryResp, retryErr := httpClient.Do(retryReq)
		if retryErr != nil {
			h.logAndWriteError(w, http.StatusBadGateway, "Affinity rebind failed: "+retryErr.Error(), "api_error", &log, startTime)
			return
		}
		resp = retryResp
		defer resp.Body.Close()
	}

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, resp.Header, &log, startTime)
		return
	}

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := asFlusher(w)

		reader := bufio.NewReader(resp.Body)
		resetIdle, stopIdle, idleTimedOut := armIdleWatchdog(resp.Body, streamIdleTimeout)
		defer stopIdle()
		var textAccumulator strings.Builder
		var finalUsage *OpenAIUsage
		var upstreamProvider string
		normalClose := false
		logged := false

		// finish persists billing/usage and sends the MuhiyaChat meta chunk
		// exactly once, BEFORE the terminal [DONE] line - a data frame after
		// [DONE] violates SSE/OpenAI stream semantics. Idempotent so it is
		// safe to call from both the normal (inline, pre-DONE) and abnormal
		// (post-loop, on disconnect) completion paths below.
		finish := func() {
			if logged {
				return
			}
			logged = true
			completionTokens := estimateTokens(textAccumulator.String())
			inputTokens := log.InputTokens
			cacheRead := 0
			cacheWrite := 0
			if finalUsage != nil {
				inputTokens = finalUsage.PromptTokens
				completionTokens = finalUsage.CompletionTokens
				cacheRead = finalUsage.CacheReadTokensFor(provider.BaseURL, model.TargetModel)
				cacheWrite = finalUsage.CacheWriteTokensReported()
				log.CacheMissTokens = finalUsage.CacheMissTokensFor(provider.BaseURL, model.TargetModel)
			}
			log.UsageEstimated = finalUsage == nil
			log.UpstreamProvider = upstreamProvider
			h.observeProviderAffinity(routeScope, upstreamProvider)
			setCalculatedCost(&log, model, pricing.ReportedUsage{
				PromptTokens:     int64(inputTokens),
				OutputTokens:     int64(completionTokens),
				CacheReadTokens:  int64(cacheRead),
				CacheWriteTokens: int64(cacheWrite),
			}, pricedAtFrom(r.Context()))
			setStreamOutcome(&log, streamResult{
				normalClose: normalClose,
				idleTimeout: idleTimedOut(),
				requestErr:  r.Context().Err(),
			})
			log.LatencyMS = int(time.Since(startTime).Milliseconds())
			h.saveRequestLog(log)
			sendMuhiyaMetaChunk(w, &log, model.Name)
		}

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			resetIdle()

			if strings.HasPrefix(line, "data:") {
				dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if dataStr == "[DONE]" {
					normalClose = true
					finish()
					w.Write([]byte(line))
					flusher.Flush()
					continue
				} else if dataStr != "" {
					var chunk OpenAIChunk
					if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
						// The accumulator only feeds the fallback token estimator used
						// when the upstream reports no usage; cap it so a very long
						// response cannot hold unbounded text in memory per stream. The
						// forwarded bytes to the client below are unaffected.
						if len(chunk.Choices) > 0 && textAccumulator.Len() < maxAccumulatedTextBytes {
							textAccumulator.WriteString(chunk.Choices[0].Delta.Content)
						}
						if chunk.Usage != nil {
							finalUsage = chunk.Usage
						}
						// Which upstream OpenRouter picked. Repeated on every
						// chunk; last writer wins, and they agree within a
						// stream. Empty for a direct connection.
						if chunk.Provider != "" {
							upstreamProvider = chunk.Provider
						}
					}
				}
			}
			w.Write([]byte(line))
			flusher.Flush()
		}

		if !normalClose {
			w.Write([]byte("data: {\"error\": {\"message\": \"Upstream connection disconnected prematurely.\", \"type\": \"api_error\"}}\n\n"))
			flusher.Flush()
		}
		finish()
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			h.logAndWriteError(w, http.StatusInternalServerError, "Read response error", "api_error", &log, startTime)
			return
		}

		var oaiResp OpenAIResponse
		completionTokens := 0
		cacheRead := 0
		cacheWrite := 0
		if err := json.Unmarshal(respBody, &oaiResp); err == nil && oaiResp.Usage.TotalTokens > 0 {
			log.InputTokens = oaiResp.Usage.PromptTokens
			completionTokens = oaiResp.Usage.CompletionTokens
			cacheRead = oaiResp.Usage.CacheReadTokensFor(provider.BaseURL, model.TargetModel)
			cacheWrite = oaiResp.Usage.CacheWriteTokensReported()
			log.CacheMissTokens = oaiResp.Usage.CacheMissTokensFor(provider.BaseURL, model.TargetModel)
			log.UsageEstimated = false
		} else {
			if len(oaiResp.Choices) > 0 {
				completionTokens = estimateTokens(GetMessageContentString(oaiResp.Choices[0].Message.Content))
			}
			log.UsageEstimated = true
		}

		log.StatusCode = http.StatusOK
		log.UpstreamProvider = oaiResp.Provider
		h.observeProviderAffinity(routeScope, oaiResp.Provider)
		setCalculatedCost(&log, model, pricing.ReportedUsage{
			PromptTokens:     int64(log.InputTokens),
			OutputTokens:     int64(completionTokens),
			CacheReadTokens:  int64(cacheRead),
			CacheWriteTokens: int64(cacheWrite),
		}, pricedAtFrom(r.Context()))
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		h.saveRequestLog(log)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBody)
	}
}

func (h *ProxyHandler) proxyOpenAIToAnthropic(w http.ResponseWriter, r *http.Request, oaiReq *OpenAIRequest, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	anthRequest, err := TranslateOpenAIToAnthropic(oaiReq, model.TargetModel)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadRequest, "Payload translation error: "+err.Error(), "invalid_request_error", &log, startTime)
		return
	}
	translated, _ := json.Marshal(anthRequest)
	var bodyMap map[string]interface{}
	_ = json.Unmarshal(translated, &bodyMap)
	applied := ApplyThinkingAnthropic(bodyMap, provider.BaseURL, model.TargetModel, log.ThinkingLevel)
	log.ThinkingLevel = ThinkingLogValue(log.ThinkingLevel, applied)
	// Anthropic caches nothing without explicit breakpoints; inject them so
	// agent loops stop re-billing their full history at full price.
	InjectAnthropicCacheControl(bodyMap, model.TargetModel)
	newBody, _ := json.Marshal(bodyMap)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/v1/messages") && !strings.HasSuffix(url, "/messages") {
		url += "/v1/messages"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Upstream error: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, resp.Header, &log, startTime)
		return
	}

	if oaiReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := asFlusher(w)

		reader := bufio.NewReader(resp.Body)
		resetIdle, stopIdle, idleTimedOut := armIdleWatchdog(resp.Body, streamIdleTimeout)
		defer stopIdle()
		var usageTracker OpenAIUsage
		msgID := "chatcmpl-" + uuid.New().String()

		normalClose := false
		logged := false
		// finish persists billing/usage and sends the meta chunk exactly
		// once, before the terminal [DONE] frame (see proxyOpenAIToOpenAI).
		finish := func() {
			if logged {
				return
			}
			logged = true
			flatWrite, write5m, write1h := usageTracker.CacheWriteTokensByTTL()
			setCalculatedCost(&log, model, pricing.ReportedUsage{
				PromptTokens:       int64(usageTracker.PromptTokens),
				OutputTokens:       int64(usageTracker.CompletionTokens),
				CacheReadTokens:    int64(usageTracker.CacheReadTokens()),
				CacheWriteTokens:   int64(flatWrite),
				CacheWrite5mTokens: int64(write5m),
				CacheWrite1hTokens: int64(write1h),
			}, pricedAtFrom(r.Context()))
			setStreamOutcome(&log, streamResult{
				normalClose: normalClose,
				idleTimeout: idleTimedOut(),
				requestErr:  r.Context().Err(),
			})
			log.LatencyMS = int(time.Since(startTime).Milliseconds())
			h.saveRequestLog(log)
			sendMuhiyaMetaChunk(w, &log, model.Name)
		}

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			resetIdle()
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			oaiChunkBytes, done, err := TranslateAnthropicChunkToOpenAI(line, msgID, oaiReq.Model, &usageTracker)
			if err != nil {
				continue
			}
			if len(oaiChunkBytes) > 0 {
				w.Write([]byte("data: " + string(oaiChunkBytes) + "\n\n"))
				flusher.Flush()
			}
			if done {
				normalClose = true
				break
			}
		}

		if !normalClose {
			w.Write([]byte("data: {\"error\": {\"message\": \"Upstream connection disconnected prematurely.\", \"type\": \"api_error\"}}\n\n"))
			flusher.Flush()
		}
		finish()
		if normalClose {
			w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
		}
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		var anthResp AnthropicResponse
		_ = json.Unmarshal(respBody, &anthResp)

		oaiResponse := TranslateAnthropicToOpenAIResponse(&anthResp, oaiReq.Model)
		translated, _ := json.Marshal(oaiResponse)

		log.StatusCode = http.StatusOK
		// The Anthropic response was translated into OpenAI shape for the
		// client, but its prompt count is still Anthropic's cache-exclusive
		// number, so it is priced under this model's own accounting mode.
		setCalculatedCost(&log, model, anthropicReportedUsage(&anthResp.Usage), pricedAtFrom(r.Context()))
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		h.saveRequestLog(log)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(translated)
	}
}

func (h *ProxyHandler) proxyAnthropicToOpenAI(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	oaiReq, err := TranslateAnthropicToOpenAI(anthReq, model.TargetModel)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadRequest, "Payload translation error: "+err.Error(), "invalid_request_error", &log, startTime)
		return
	}
	translated, _ := json.Marshal(oaiReq)
	var bodyMap map[string]interface{}
	_ = json.Unmarshal(translated, &bodyMap)
	applied := ApplyThinkingOpenAI(bodyMap, provider.BaseURL, model.TargetModel, log.ThinkingLevel)
	log.ThinkingLevel = ThinkingLogValue(log.ThinkingLevel, applied)
	sanitizeUpstreamIdentity(bodyMap, classifyUpstream(provider.BaseURL, model.TargetModel), h.identitySecret, log.UserID)
	newBody, _ := json.Marshal(bodyMap)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") && !strings.HasSuffix(url, "/completions") {
		url += "/chat/completions"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	setOpenRouterHeaders(req, provider, r)

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Upstream connection failed: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, resp.Header, &log, startTime)
		return
	}

	if anthReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := asFlusher(w)

		reader := bufio.NewReader(resp.Body)
		resetIdle, stopIdle, idleTimedOut := armIdleWatchdog(resp.Body, streamIdleTimeout)
		defer stopIdle()
		var usageTracker AnthropicUsage
		msgID := "msg_" + uuid.New().String()

		normalClose := false
		logged := false
		// finish persists billing/usage and sends the meta chunk exactly
		// once, before the terminal message_stop event.
		finish := func() {
			if logged {
				return
			}
			logged = true
			setCalculatedCost(&log, model, anthropicReportedUsage(&usageTracker), pricedAtFrom(r.Context()))
			setStreamOutcome(&log, streamResult{
				normalClose: normalClose,
				idleTimeout: idleTimedOut(),
				requestErr:  r.Context().Err(),
			})
			log.LatencyMS = int(time.Since(startTime).Milliseconds())
			h.saveRequestLog(log)
			sendMuhiyaMetaChunk(w, &log, model.Name)
		}

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			resetIdle()
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			anthChunkBytes, eventType, done, err := TranslateOpenAIChunkToAnthropic(line, msgID, anthReq.Model, &usageTracker)
			if err != nil {
				continue
			}
			if len(anthChunkBytes) > 0 {
				w.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(anthChunkBytes))))
				flusher.Flush()
			}
			if done {
				normalClose = true
				break
			}
		}

		if !normalClose {
			w.Write([]byte("event: error\ndata: {\"type\": \"error\", \"error\": {\"type\": \"api_error\", \"message\": \"Upstream connection disconnected prematurely.\"}}\n\n"))
			flusher.Flush()
		}
		finish()
		if normalClose {
			w.Write([]byte("event: message_stop\ndata: {\"type\": \"message_stop\"}\n\n"))
			flusher.Flush()
		}
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		var oaiResp OpenAIResponse
		_ = json.Unmarshal(respBody, &oaiResp)

		anthResponse := TranslateOpenAIToAnthropicResponse(&oaiResp, anthReq.Model)
		translated, _ := json.Marshal(anthResponse)

		log.StatusCode = http.StatusOK
		log.CacheMissTokens = oaiResp.Usage.CacheMissTokensFor(provider.BaseURL, model.TargetModel)
		setCalculatedCost(&log, model, pricing.ReportedUsage{
			PromptTokens:     int64(oaiResp.Usage.PromptTokens),
			OutputTokens:     int64(oaiResp.Usage.CompletionTokens),
			CacheReadTokens:  int64(oaiResp.Usage.CacheReadTokensFor(provider.BaseURL, model.TargetModel)),
			CacheWriteTokens: int64(oaiResp.Usage.CacheWriteTokensReported()),
		}, pricedAtFrom(r.Context()))
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		h.saveRequestLog(log)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(translated)
	}
}

func (h *ProxyHandler) proxyAnthropicToAnthropic(w http.ResponseWriter, r *http.Request, origBody []byte, anthReq *AnthropicRequest, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	var bodyMap map[string]interface{}
	// Guard against a non-object body (e.g. `null`) that would leave bodyMap nil
	// and panic on the assignment below; return a clean 400 instead.
	if err := json.Unmarshal(origBody, &bodyMap); err != nil || bodyMap == nil {
		h.logAndWriteError(w, http.StatusBadRequest, "Request body must be a JSON object", "invalid_request_error", &log, startTime)
		return
	}
	bodyMap["model"] = model.TargetModel

	applied := ApplyThinkingAnthropic(bodyMap, provider.BaseURL, model.TargetModel, log.ThinkingLevel)
	log.ThinkingLevel = ThinkingLogValue(log.ThinkingLevel, applied)
	// No-op when the client already placed its own cache_control breakpoints.
	InjectAnthropicCacheControl(bodyMap, model.TargetModel)

	newBody, _ := json.Marshal(bodyMap)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/v1/messages") && !strings.HasSuffix(url, "/messages") {
		url += "/v1/messages"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Upstream connection failed: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, resp.Header, &log, startTime)
		return
	}

	if anthReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := asFlusher(w)

		reader := bufio.NewReader(resp.Body)
		resetIdle, stopIdle, idleTimedOut := armIdleWatchdog(resp.Body, streamIdleTimeout)
		defer stopIdle()
		var usageTracker AnthropicUsage

		normalClose := false
		logged := false
		// finish persists billing/usage and sends the meta chunk exactly
		// once, before the terminal message_stop line is forwarded.
		finish := func() {
			if logged {
				return
			}
			logged = true
			setCalculatedCost(&log, model, anthropicReportedUsage(&usageTracker), pricedAtFrom(r.Context()))
			setStreamOutcome(&log, streamResult{
				normalClose: normalClose,
				idleTimeout: idleTimedOut(),
				requestErr:  r.Context().Err(),
			})
			log.LatencyMS = int(time.Since(startTime).Milliseconds())
			h.saveRequestLog(log)
			sendMuhiyaMetaChunk(w, &log, model.Name)
		}

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			resetIdle()

			// Parse usage/terminal-event out of the chunk BEFORE forwarding
			// it, so the meta chunk precedes message_stop rather than
			// following it (a data frame after the stream's terminal event
			// is invalid SSE).
			trimmed := strings.TrimSpace(line)
			isMessageStop := false
			if strings.HasPrefix(trimmed, "data:") {
				dataStr := strings.TrimPrefix(trimmed, "data:")
				var event map[string]interface{}
				if err := json.Unmarshal([]byte(dataStr), &event); err == nil {
					eventType, _ := event["type"].(string)
					if eventType == "message_stop" {
						isMessageStop = true
						normalClose = true
					} else if eventType == "message_start" {
						if message, ok := event["message"].(map[string]interface{}); ok {
							if usage, ok := message["usage"].(map[string]interface{}); ok {
								if in, ok := usage["input_tokens"].(float64); ok {
									usageTracker.InputTokens = int(in)
								}
								if cr, ok := usage["cache_read_input_tokens"].(float64); ok {
									usageTracker.CacheReadInputTokens = int(cr)
								}
							}
						}
					} else if eventType == "message_delta" {
						if usage, ok := event["usage"].(map[string]interface{}); ok {
							if out, ok := usage["output_tokens"].(float64); ok {
								usageTracker.OutputTokens = int(out)
							}
							if cw, ok := usage["cache_creation_input_tokens"].(float64); ok {
								usageTracker.CacheCreationInputTokens = int(cw)
							}
							usageTracker.ApplyCacheCreation(ParseAnthropicCacheCreation(usage))
						}
					}
				}
			}

			if isMessageStop {
				finish()
			}
			w.Write([]byte(line))
			flusher.Flush()
		}

		if !normalClose {
			w.Write([]byte("event: error\ndata: {\"type\": \"error\", \"error\": {\"type\": \"api_error\", \"message\": \"Upstream connection disconnected prematurely.\"}}\n\n"))
			flusher.Flush()
		}
		finish()
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		var anthResp AnthropicResponse
		_ = json.Unmarshal(respBody, &anthResp)

		log.StatusCode = http.StatusOK
		setCalculatedCost(&log, model, anthropicReportedUsage(&anthResp.Usage), pricedAtFrom(r.Context()))
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		h.saveRequestLog(log)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBody)
	}
}

// ------------------------------------------
// Model Discovery Endpoint (Anthropic Specification)
// ------------------------------------------
// discoverableModel reports whether a model should appear in /v1/models for the
// current caller. Every caller: the model must be active, not transcribe-only,
// and not the deprecated router virtual model (automatic model selection is no
// longer part of the gateway contract — see migration 027 — so it must never be
// offered as a selectable model to any client, MuhiyaCode or otherwise). The
// MuhiyaCode app additionally sees only models marked muhiyacode_visible.
// This gates DISCOVERY only - inference by exact name and router selection never
// consult this, so a hidden model stays fully usable.
func discoverableModel(m *db.Model, onlyMuhiyaCodeVisible bool) bool {
	if m == nil || m.Status != "active" || m.Transcribe || m.Name == routerModelDeprecated {
		return false
	}
	if onlyMuhiyaCodeVisible && !m.MuhiyaCodeVisible {
		return false
	}
	return true
}

func (h *ProxyHandler) handleModelDiscovery(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	models, err := h.db.ListModels()
	if err != nil {
		h.internalErrorResponse(w, "database error listing models", err)
		return
	}

	clientIsAnthropic := isAnthropicRequest(r)

	// MuhiyaCode discoverability filter: when the caller identifies as the
	// MuhiyaCode app, only models an operator has explicitly marked
	// muhiyacode_visible are listed/resolvable here. Every other client app
	// (MuhiyaChat, the platform, third-party SDKs) sees the full active catalog
	// unchanged. Discovery-only: inference by exact name and router selection
	// are never gated, so a hidden model stays fully usable.
	onlyMuhiyaCodeVisible := getClientAppName(r) == "MuhiyaCode"

	// If asking for a specific model details
	pathParts := strings.Split(r.URL.Path, "/models/")
	if len(pathParts) > 1 && pathParts[1] != "" {
		modelID := strings.TrimSuffix(pathParts[1], "/")
		var matchedModel *db.Model
		for i := range models {
			m := &models[i]
			// The detail branch must apply the same visibility rule as the list
			// branches so /v1/models/{id} cannot leak a disabled or hidden model.
			if m.Name == modelID && discoverableModel(m, onlyMuhiyaCodeVisible) {
				matchedModel = m
				break
			}
		}

		if matchedModel == nil {
			h.writeError(w, http.StatusNotFound, "Model not found: "+modelID, "not_found_error")
			return
		}

		displayName := matchedModel.DisplayName
		if displayName == "" {
			if clientIsAnthropic {
				displayName = matchedModel.Name + " (via MuhiyaLLM)"
			} else {
				displayName = matchedModel.Name
			}
		}

		ownedBy := matchedModel.OwnedBy
		if ownedBy == "" {
			ownedBy = "MuhiyaLLM"
		}

		if clientIsAnthropic {
			response := map[string]interface{}{
				"type":              "model",
				"id":                matchedModel.Name,
				"display_name":      displayName,
				"description":       matchedModel.Description,
				"context_window":    matchedModel.ContextWindow,
				"max_output_tokens": matchedModel.MaxOutputTokens,
				"created_at":        matchedModel.CreatedAt.Format(time.RFC3339),
			}
			for k, v := range modelCapabilityFields(matchedModel) {
				response[k] = v
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(response)
		} else {
			info := map[string]interface{}{
				"context_window":    matchedModel.ContextWindow,
				"max_output_tokens": matchedModel.MaxOutputTokens,
				"max_tokens":        matchedModel.MaxOutputTokens,
				"display_name":      displayName,
				"description":       matchedModel.Description,
				"owned_by":          ownedBy,
			}
			response := map[string]interface{}{
				"id":                matchedModel.Name,
				"object":            "model",
				"created":           matchedModel.CreatedAt.Unix(),
				"owned_by":          ownedBy,
				"display_name":      displayName,
				"description":       matchedModel.Description,
				"context_window":    matchedModel.ContextWindow,
				"max_output_tokens": matchedModel.MaxOutputTokens,
				"max_tokens":        matchedModel.MaxOutputTokens,
			}
			for k, v := range modelCapabilityFields(matchedModel) {
				response[k] = v
				info[k] = v
			}
			response["info"] = info
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(response)
		}
		return
	}

	// List all models
	if clientIsAnthropic {
		var data []map[string]interface{}
		for i := range models {
			m := &models[i]
			if discoverableModel(m, onlyMuhiyaCodeVisible) {
				displayName := m.DisplayName
				if displayName == "" {
					displayName = m.Name + " (via MuhiyaLLM)"
				}
				entry := map[string]interface{}{
					"type":              "model",
					"id":                m.Name,
					"display_name":      displayName,
					"description":       m.Description,
					"context_window":    m.ContextWindow,
					"max_output_tokens": m.MaxOutputTokens,
					"created_at":        m.CreatedAt.Format(time.RFC3339),
				}
				for k, v := range modelCapabilityFields(m) {
					entry[k] = v
				}
				data = append(data, entry)
			}
		}

		response := map[string]interface{}{
			"data":     data,
			"has_more": false,
			"first_id": nil,
			"last_id":  nil,
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	} else {
		var data []map[string]interface{}
		for i := range models {
			m := &models[i]
			if discoverableModel(m, onlyMuhiyaCodeVisible) {
				displayName := m.DisplayName
				if displayName == "" {
					displayName = m.Name
				}
				ownedBy := m.OwnedBy
				if ownedBy == "" {
					ownedBy = "MuhiyaLLM"
				}
				info := map[string]interface{}{
					"context_window":    m.ContextWindow,
					"max_output_tokens": m.MaxOutputTokens,
					"max_tokens":        m.MaxOutputTokens,
					"display_name":      displayName,
					"description":       m.Description,
					"owned_by":          ownedBy,
				}
				entry := map[string]interface{}{
					"id":                m.Name,
					"object":            "model",
					"created":           m.CreatedAt.Unix(),
					"owned_by":          ownedBy,
					"display_name":      displayName,
					"description":       m.Description,
					"context_window":    m.ContextWindow,
					"max_output_tokens": m.MaxOutputTokens,
					"max_tokens":        m.MaxOutputTokens,
				}
				for k, v := range modelCapabilityFields(m) {
					entry[k] = v
					info[k] = v
				}
				entry["info"] = info
				data = append(data, entry)
			}
		}

		response := map[string]interface{}{
			"object": "list",
			"data":   data,
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}
}

// ------------------------------------------
// Common Error Logging Helpers
// ------------------------------------------

// durationFromBytes estimates audio seconds from the uploaded byte count, as a
// billing FLOOR for providers that report no duration of their own.
//
// It assumes a high bitrate on purpose. A high assumed bitrate yields a SHORT
// duration, so this can only ever under-bill relative to reality — the estimate
// is a floor that stops a request from being billed at zero, never a ceiling
// that could over-charge a caller for audio they did not send. 320 kbit/s is
// above essentially all speech encoding.
func durationFromBytes(uploaded int64) float64 {
	if uploaded <= 0 {
		return 0
	}
	const maxBitsPerSecond = 320_000.0
	return (float64(uploaded) * 8) / maxBitsPerSecond
}

// writeLimitError renders a CheckLimit failure according to WHY it happened.
// One shape for all three causes forced clients to guess, and a client that
// guesses "retry" against a spent budget hammers the gateway until the user
// gives up. MuhiyaCode reads these to decide whether to wait or to stop.
func (h *ProxyHandler) writeLimitError(w http.ResponseWriter, err error) {
	switch KindOf(err) {
	case LimitInfrastructure:
		w.Header().Set("Retry-After", "2")
		h.writeError(w, http.StatusServiceUnavailable, err.Error(), "api_error")
	case LimitSuspended:
		// Not a rate limit at all: no amount of waiting changes it.
		h.writeError(w, http.StatusForbidden, err.Error(), "permission_error")
	case LimitBudget:
		// 429 for wire compatibility with OpenAI clients, but the type says
		// quota so a client can tell this from throttling. No Retry-After: the
		// window roll, not a backoff, is what releases this.
		h.writeError(w, http.StatusTooManyRequests, "Budget exhausted: "+err.Error(), "insufficient_quota")
	default:
		// The RPM/TPM windows are one minute wide; advise clients to back off
		// for that long rather than hammering into a still-full window.
		w.Header().Set("Retry-After", "60")
		h.writeError(w, http.StatusTooManyRequests, "Limit exceeded: "+err.Error(), "rate_limit_error")
	}
}

func (h *ProxyHandler) writeError(w http.ResponseWriter, code int, msg string, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	r := h.getRequest(w)
	if isAnthropicRequest(r) {
		anthErrType := errType
		if errType == "unauthorized" {
			anthErrType = "authentication_error"
		}
		errPayload := anthropicErrorPayload{
			Type: "error",
		}
		errPayload.Error.Type = anthErrType
		errPayload.Error.Message = msg
		_ = json.NewEncoder(w).Encode(errPayload)
		return
	}

	errPayload := map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"type":    errType,
		},
	}
	_ = json.NewEncoder(w).Encode(errPayload)
}

type streamResult struct {
	normalClose bool
	idleTimeout bool
	requestErr  error
}

type requestFailure struct {
	requestErr  error
	message     string
	idleTimeout bool
}

func setStreamOutcome(entry *db.RequestLog, result streamResult) {
	if result.normalClose {
		entry.StatusCode = http.StatusOK
		entry.RequestStatus = db.RequestStatusForHTTP(entry.StatusCode)
		return
	}
	entry.StatusCode = requestFailureStatus(requestFailure{
		requestErr: result.requestErr, idleTimeout: result.idleTimeout,
	})
	entry.ErrorMessage = "upstream stream ended before the terminal event"
	if entry.StatusCode == http.StatusGatewayTimeout {
		entry.ErrorMessage = "stream timed out before the terminal event"
	} else if entry.StatusCode == 499 {
		entry.ErrorMessage = "request cancelled before the terminal event"
	}
	entry.RequestStatus = db.RequestStatusForHTTP(entry.StatusCode)
	// Failed/cancelled attempts remain observable with their token estimates,
	// but never consume customer credits. A later retry has its own attempt row.
	entry.Cost = 0
	entry.CostNanoUSD = 0
}

func requestFailureStatus(failure requestFailure) int {
	switch {
	case errors.Is(failure.requestErr, context.DeadlineExceeded),
		failure.idleTimeout,
		strings.Contains(strings.ToLower(failure.message), "timeout"),
		strings.Contains(strings.ToLower(failure.message), "deadline exceeded"):
		return http.StatusGatewayTimeout
	case errors.Is(failure.requestErr, context.Canceled):
		return 499
	default:
		return http.StatusBadGateway
	}
}

func (h *ProxyHandler) logAndWriteError(w http.ResponseWriter, code int, msg string, errType string, log *db.RequestLog, startTime time.Time) {
	request := h.getRequest(w)
	if request != nil {
		classified := requestFailureStatus(requestFailure{
			requestErr: request.Context().Err(),
			message:    msg,
		})
		if classified != http.StatusBadGateway {
			code = classified
		}
	}
	log.StatusCode = code
	log.RequestStatus = db.RequestStatusForHTTP(code)
	log.ErrorMessage = msg
	log.LatencyMS = int(time.Since(startTime).Milliseconds())
	h.saveRequestLog(*log)

	h.writeError(w, code, msg, errType)
}

func (h *ProxyHandler) logFailedUpstream(w http.ResponseWriter, statusCode int, respBytes []byte, respHeader http.Header, log *db.RequestLog, startTime time.Time) {
	log.StatusCode = statusCode
	log.ErrorMessage = string(respBytes)
	log.LatencyMS = int(time.Since(startTime).Milliseconds())
	h.saveRequestLog(*log)

	w.Header().Set("Content-Type", "application/json")
	// Relay the upstream's Retry-After (rate-limit / overloaded backpressure) so
	// clients honor the provider's requested cooldown instead of retrying blind.
	if ra := respHeader.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.WriteHeader(statusCode)

	r := h.getRequest(w)
	clientIsAnth := isAnthropicRequest(r)
	translated := translateErrorBytes(respBytes, clientIsAnth)
	w.Write(translated)
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}

	tokens := 0
	words := strings.Fields(text)

	for _, word := range words {
		isArabic := false
		for _, r := range word {
			if r >= 0x0600 && r <= 0x06FF { // Arabic unicode block
				isArabic = true
				break
			}
		}

		if isArabic {
			// Arabic words average ~2.5 tokens
			tokens += 3
		} else {
			// English words average ~1.3 tokens (approx 4 chars per token)
			wordLen := len(word)
			tokens += (wordLen + 3) / 4
		}
	}

	// Add tokens for common code punctuations that might be stripped by Fields
	punctuations := []string{"{", "}", "(", ")", "[", "]", ";", ",", ".", "=", "+", "-", "*", "/", "<", ">", "!", "&", "|"}
	for _, p := range punctuations {
		tokens += strings.Count(text, p)
	}

	// Add tokens for indentation (4 spaces = 1 token)
	spaces := strings.Count(text, " ")
	tokens += spaces / 4

	if tokens == 0 {
		return 1
	}
	return tokens
}

// MiniMax-M3 bills a second, doubled rate tier once the request's input
// exceeds 512k tokens (platform.minimax.io pricing, captured 2026-07-14).
// The base tier (0.30/1.20/0.06 per M in/out/cached) lives on the model row
// seeded by db/migrations (009 MiniMax models); the >512k tier below MUST be
// updated in lockstep if that row is ever repriced. Data-driven tier columns
// are the durable follow-up; today only M3 is tier-priced.
func calculateCost(model *db.Model, input, output, cacheRead, cacheWrite int) float64 {
	rules, err := pricingRulesForModel(model)
	if err != nil {
		return 0
	}
	accounting := pricing.PromptInclusive
	if model != nil {
		accounting = pricing.ParsePromptAccounting(model.PromptAccounting)
	}
	usage, _ := pricing.NormalizeUsage(pricing.ReportedUsage{
		PromptTokens:     int64(input),
		OutputTokens:     int64(output),
		CacheReadTokens:  int64(cacheRead),
		CacheWriteTokens: int64(cacheWrite),
	}, accounting)
	quote, err := rules.Quote(usage)
	if err != nil {
		return 0
	}
	return quote.Cost.USD()
}

// anthropicReportedUsage maps an Anthropic usage payload onto the canonical
// reported shape, splitting cache creation by requested entry lifetime where
// the upstream provides that breakdown.
func anthropicReportedUsage(usage *AnthropicUsage) pricing.ReportedUsage {
	if usage == nil {
		return pricing.ReportedUsage{}
	}
	reported := pricing.ReportedUsage{
		PromptTokens:       int64(usage.InputTokens),
		OutputTokens:       int64(usage.OutputTokens),
		CacheReadTokens:    int64(usage.CacheReadInputTokens),
		CacheWrite5mTokens: int64(usage.CacheCreation5mInputTokens),
		CacheWrite1hTokens: int64(usage.CacheCreation1hInputTokens),
	}
	// The flat total covers only what the per-TTL breakdown did not already
	// account for, so a provider reporting both shapes is not billed twice.
	remainder := int64(usage.CacheCreationInputTokens) -
		reported.CacheWrite5mTokens - reported.CacheWrite1hTokens
	if remainder > 0 {
		reported.CacheWriteTokens = remainder
	}
	return reported
}

// setCalculatedCost prices a request from the token counts an upstream
// reported, in that upstream's own accounting convention.
//
// This is the single place provider usage becomes money. It used to be four
// near-identical copies scattered across the streaming and non-streaming
// OpenAI and Anthropic paths, each assigning token fields by hand — which is
// exactly how one of them came to feed Anthropic's cache-exclusive token
// counts into arithmetic that assumed the inclusive convention, billing fresh
// input at zero. Keeping one funnel keeps that class of bug out.
func setCalculatedCost(entry *db.RequestLog, model *db.Model, reported pricing.ReportedUsage, pricedAt time.Time) {
	accounting := pricing.PromptInclusive
	if model != nil {
		accounting = pricing.ParsePromptAccounting(model.PromptAccounting)
	}
	usage, anomaly := pricing.NormalizeUsage(reported, accounting)

	entry.PromptAccounting = string(accounting)
	entry.UsageAnomaly = string(anomaly)
	// The canonical prompt total is recorded rather than the provider's raw
	// number, so input_tokens means the same thing for every provider and a
	// dashboard can sum the column without comparing dialects.
	entry.InputTokens = int(usage.PromptTotalTokens)
	entry.OutputTokens = int(usage.OutputTokens)
	entry.CacheReadTokens = int(usage.CacheReadTokens + usage.CacheRead5mTokens)
	entry.CacheWriteTokens = int(usage.CacheWriteTokens + usage.CacheWrite5mTokens + usage.CacheWrite1hTokens)

	if anomaly != pricing.AnomalyNone {
		log.Printf("[BILLING-ANOMALY] request %s model %s reported inconsistent usage (%s): %+v",
			entry.ID, modelName(model), anomaly, reported)
	}

	rules, err := pricingRulesForModel(model)
	if err == nil {
		var quote pricing.Quote
		quote, err = rules.QuoteAt(usage, pricedAt)
		if err == nil {
			applyQuoteToLog(entry, quote)
			return
		}
	}

	entry.Cost = 0
	entry.CostNanoUSD = 0
	entry.UsageEstimated = true
	if entry.ErrorMessage != "" {
		entry.ErrorMessage += "; "
	}
	entry.ErrorMessage += "exact pricing failed: " + err.Error()
	log.Printf("[BILLING-ERROR] exact pricing failed for request %s: %v", entry.ID, err)
}

// applyQuoteToLog records both the charge and the derivation that produced it.
func applyQuoteToLog(entry *db.RequestLog, quote pricing.Quote) {
	entry.CostNanoUSD = quote.Cost
	entry.Cost = quote.Cost.USD()

	receipt := quote.Receipt
	entry.PricingRuleSetID = receipt.RuleSetID
	entry.PriceWindowID = receipt.WindowID
	entry.PriceMultiplierNum = receipt.MultiplierNum
	entry.PriceMultiplierDen = receipt.MultiplierDen
	if receipt.TierThreshold != pricing.BaseTierThreshold {
		threshold := receipt.TierThreshold
		entry.PricingTierThreshold = &threshold
	} else {
		entry.PricingTierThreshold = nil
	}
	if !receipt.PricedAt.IsZero() {
		pricedAt := receipt.PricedAt
		entry.PricedAt = &pricedAt
	}
	entry.PricingLines = entry.PricingLines[:0]
	for _, line := range receipt.Lines {
		entry.PricingLines = append(entry.PricingLines, db.RequestPriceLine{
			TokenClass:     string(line.Class),
			Tokens:         line.Tokens,
			RatePerMillion: line.RatePerMillion,
			Cost:           line.Cost,
		})
	}
}

func modelName(model *db.Model) string {
	if model == nil {
		return "<unknown>"
	}
	return model.Name
}

func pricingRulesForModel(model *db.Model) (pricing.RuleSet, error) {
	if model == nil {
		return pricing.RuleSet{}, fmt.Errorf("model is required for pricing")
	}
	input, err := exactRate(model.InputCostNanoPerMillion, model.InputCostPerMillion)
	if err != nil {
		return pricing.RuleSet{}, err
	}
	output, err := exactRate(model.OutputCostNanoPerMillion, model.OutputCostPerMillion)
	if err != nil {
		return pricing.RuleSet{}, err
	}
	cacheRead, err := exactRate(model.CacheReadCostNanoPerMillion, model.CacheReadCostPerMillion)
	if err != nil {
		return pricing.RuleSet{}, err
	}
	cacheWrite, err := exactRate(model.CacheWriteCostNanoPerMillion, model.CacheWriteCostPerMillion)
	if err != nil {
		return pricing.RuleSet{}, err
	}
	base := pricing.Rates{
		InputPerMillion:      input,
		OutputPerMillion:     output,
		CacheReadPerMillion:  cacheRead,
		CacheWritePerMillion: cacheWrite,
	}
	base = pricing.OverlayTTLRates(base, model.CacheTTLRates, nil)

	tiers := make([]pricing.Tier, 0, len(model.PricingTiers))
	for _, tier := range model.PricingTiers {
		threshold := tier.MinInputTokensExclusive
		tier.Rates = pricing.OverlayTTLRates(tier.Rates, model.CacheTTLRates, &threshold)
		tiers = append(tiers, tier)
	}

	accounting := pricing.ParsePromptAccounting(model.PromptAccounting)
	// Every input to the resolved price is folded into the snapshot id. A
	// changed TTL rate, price window or accounting mode must produce a new id,
	// otherwise two requests billed at genuinely different rates would claim
	// the same rule set and the audit trail would be a lie.
	canonical := fmt.Sprintf("%s|%d|%d|%d|%d|%v|%v|%v|%s",
		model.ID, input, output, cacheRead, cacheWrite,
		tiers, model.CacheTTLRates, model.PriceWindows, accounting)
	snapshot := sha256.Sum256([]byte(canonical))
	ruleID := fmt.Sprintf("pricing:%x", snapshot)

	return pricing.New(pricing.Spec{
		ID:         ruleID,
		Base:       base,
		Tiers:      tiers,
		Windows:    model.PriceWindows,
		Accounting: accounting,
	})
}

func exactRate(exact money.NanoUSD, legacy float64) (money.NanoUSD, error) {
	if exact != 0 || legacy == 0 {
		return exact, nil
	}
	return money.FromUSD(legacy)
}

func requestedOpenAIOutput(req *OpenAIRequest, model *db.Model) int {
	requested := 0
	if req.MaxCompletionTokens != nil {
		requested = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		requested = *req.MaxTokens
	}
	return boundedOutputLimit(requested, model)
}

func boundedOutputLimit(requested int, model *db.Model) int {
	modelLimit := 0
	if model != nil {
		modelLimit = model.MaxOutputTokens
	}
	if requested <= 0 {
		requested = modelLimit
	}
	if requested <= 0 {
		requested = 8192
	}
	if modelLimit > 0 && requested > modelLimit {
		requested = modelLimit
	}
	return requested
}

func conservativeInputTokenBound(rawBody []byte, estimated int, model *db.Model) int {
	// Pricing every serialized byte as a token rejected affordable coding
	// sessions before inference. JSON/code averages several ASCII bytes per
	// token; non-ASCII text is priced more conservatively per rune.
	bound := serializedTokenEstimate(rawBody) + 512
	if estimated > bound {
		bound = estimated
	}
	if model != nil && model.ContextWindow > 0 && bound > model.ContextWindow {
		bound = model.ContextWindow
	}
	if bound < 1 {
		return 1
	}
	return bound
}

func serializedTokenEstimate(rawBody []byte) int {
	asciiBytes := 0
	nonASCII := 0
	for len(rawBody) > 0 {
		if rawBody[0] < utf8.RuneSelf {
			asciiBytes++
			rawBody = rawBody[1:]
			continue
		}
		_, size := utf8.DecodeRune(rawBody)
		nonASCII++
		rawBody = rawBody[size:]
	}
	return (asciiBytes+2)/3 + nonASCII*2
}

type generationQuoteRequest struct {
	userID          string
	model           *db.Model
	inputUpperBound int
	requestedOutput int
	// pricedAt pins the instant this request is priced at. Settlement re-uses
	// it instead of reading the clock, so a request admitted just before a
	// peak-pricing boundary is billed at the rate it was quoted rather than
	// at whatever rate happens to be in force when it finishes.
	pricedAt time.Time
}

func (h *ProxyHandler) affordableGeneration(ctx context.Context, request generationQuoteRequest) (int, money.NanoUSD, db.BudgetAvailability, error) {
	rules, err := pricingRulesForModel(request.model)
	if err != nil {
		return 0, 0, db.BudgetAvailability{}, err
	}
	usage := pricing.InputUsage(int64(request.inputUpperBound))
	usage.OutputTokens = int64(request.requestedOutput)
	quote, err := rules.QuoteAt(usage, request.pricedAt)
	if err != nil {
		return 0, 0, db.BudgetAvailability{}, err
	}
	availability, err := h.db.GetBudgetAvailability(ctx, request.userID)
	if err != nil {
		return 0, 0, db.BudgetAvailability{}, err
	}
	available := availability.Available
	if quote.Cost <= available {
		return request.requestedOutput, quote.Cost, availability, nil
	}
	allowed, maxErr := rules.MaxOutputTokensAt(
		pricing.InputUsage(int64(request.inputUpperBound)),
		int64(request.requestedOutput),
		available,
		request.pricedAt,
	)
	if maxErr != nil {
		return 0, 0, db.BudgetAvailability{}, maxErr
	}
	if allowed < 1 {
		return 0, 0, availability, &db.BudgetExceededError{Requested: quote.Cost, Available: available}
	}
	usage.OutputTokens = allowed
	reduced, quoteErr := rules.QuoteAt(usage, request.pricedAt)
	if quoteErr != nil {
		return 0, 0, db.BudgetAvailability{}, quoteErr
	}
	return int(allowed), reduced.Cost, availability, nil
}

func applyBudgetWindow(entry *db.RequestLog, availability db.BudgetAvailability) {
	entry.BudgetWindowID = availability.WindowID
	if !availability.WindowStartedAt.IsZero() {
		started := availability.WindowStartedAt
		entry.BudgetWindowStartedAt = &started
	}
	if !availability.WindowResetAt.IsZero() {
		reset := availability.WindowResetAt
		entry.BudgetWindowResetAt = &reset
	}
}

func (h *ProxyHandler) writeBudgetError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrBudgetExceeded) || errors.Is(err, db.ErrNoBudgetWindow) {
		h.writeError(w, http.StatusPaymentRequired, err.Error(), "budget_exceeded")
		return
	}
	h.serviceUnavailableResponse(w, "budget admission failed", err)
}

func budgetErrorStatus(err error) int {
	if errors.Is(err, db.ErrBudgetExceeded) || errors.Is(err, db.ErrNoBudgetWindow) {
		return http.StatusPaymentRequired
	}
	return http.StatusServiceUnavailable
}

type admissionFailure struct {
	request        *http.Request
	key            *db.VirtualKey
	model          *db.Model
	provider       *db.Provider
	requestID      string
	requestedModel string
	inputTokens    int
	status         int
	err            error
	budget         db.BudgetAvailability
}

func (h *ProxyHandler) saveAdmissionFailure(failure admissionFailure) {
	correlation := requestCorrelationFor(failure.request, failure.key.ID)
	entry := db.RequestLog{
		ModelID:        failure.model.ID,
		ProviderID:     failure.provider.ID,
		RequestPath:    failure.request.URL.Path,
		StatusCode:     failure.status,
		InputTokens:    failure.inputTokens,
		ErrorMessage:   failure.err.Error(),
		ClientApp:      getClientAppName(failure.request),
		RequestedModel: failure.requestedModel,
		Complexity:     "admission",
		CreatedAt:      time.Now().UTC(),
	}
	if failure.requestID != "" {
		correlation.LogID = failure.requestID
	}
	applyRequestContext(&entry, failure.request, failure.key, correlation)
	applyBudgetWindow(&entry, failure.budget)
	h.saveRequestLog(entry)
}

func rewriteOpenAIOutputLimit(raw []byte, limit int) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if _, present := payload["max_completion_tokens"]; present {
		payload["max_completion_tokens"] = limit
		delete(payload, "max_tokens")
	} else {
		payload["max_tokens"] = limit
	}
	return json.Marshal(payload)
}

// normalizeLangCode reduces a language value to a clean ISO-639-1 primary
// subtag (e.g. "ar-EG" → "ar", "EN" → "en"). Returns "" for anything that is
// not a 2-letter code, so callers can fall back to a default.
func normalizeLangCode(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '-'); i > 0 {
		s = s[:i]
	}
	if len(s) != 2 {
		return ""
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return s
}

func (h *ProxyHandler) serveTranscriptionClient(w http.ResponseWriter, r *http.Request, bodyBytes []byte, key *db.VirtualKey) {
	// Restore body to parse multipart form
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Max upload size 10MB
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Failed to parse multipart form: "+err.Error(), "invalid_request_error")
		return
	}

	modelName := r.FormValue("model")
	if modelName == "" {
		modelName = "whisper-1"
	}

	// Retrieve target model
	targetModel, err := h.db.GetModelByName(modelName)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	if targetModel == nil {
		h.writeError(w, http.StatusNotFound, fmt.Sprintf("Model '%s' not found or inactive", modelName), "invalid_request_error")
		return
	}
	correlation := requestCorrelationFor(r, key.ID)
	requestID := correlation.LogID

	// Entitlement is checked HERE, like every other billable route. This handler
	// used to skip CheckLimit entirely, so a tenant who had exhausted their plan
	// budget and every extra credit kept transcribing indefinitely at the
	// operator's expense — the budget was enforced on chat and nowhere else.
	// (Account suspension is enforced earlier, in authenticateVirtualKey, so it
	// cannot be missed by a handler again.)
	if err := h.limiter.CheckLimit(key, estimateTokens(modelName)); err != nil {
		entry := db.RequestLog{
			ModelID: targetModel.ID, ProviderID: targetModel.ProviderID,
			RequestPath: r.URL.Path, StatusCode: http.StatusTooManyRequests,
			ErrorMessage: err.Error(), ClientApp: getClientAppName(r),
			RequestedModel: modelName, Complexity: "admission", CreatedAt: time.Now().UTC(),
		}
		applyRequestContext(&entry, r, key, correlation)
		h.saveRequestLog(entry)
		h.writeLimitError(w, err)
		return
	}
	releaseGeneration, err := h.limiter.AcquireGeneration(r.Context(), key.UserID, requestID)
	if err != nil {
		entry := db.RequestLog{
			ModelID: targetModel.ID, ProviderID: targetModel.ProviderID,
			RequestPath: r.URL.Path, StatusCode: http.StatusServiceUnavailable,
			ErrorMessage: err.Error(), ClientApp: getClientAppName(r),
			RequestedModel: modelName, Complexity: "admission", CreatedAt: time.Now().UTC(),
		}
		applyRequestContext(&entry, r, key, correlation)
		h.saveRequestLog(entry)
		h.serviceUnavailableResponse(w, "generation guard failed", err)
		return
	}
	defer releaseGeneration()

	// Retrieve provider
	provider, err := h.db.GetProvider(targetModel.ProviderID)
	if err != nil {
		h.internalErrorResponse(w, "database error", err)
		return
	}
	// saveTranscriptionFailure records a failed transcription so it is VISIBLE in
	// the admin logs (previously only successes were logged, so a misconfigured
	// or upstream-rejected transcription vanished silently). Cost is always 0 on
	// failure — the provider produced nothing.
	var budgetWindow db.BudgetAvailability
	saveTranscriptionFailure := func(status int, msg string, started time.Time) {
		entry := db.RequestLog{
			ModelID:        targetModel.ID,
			ProviderID:     targetModel.ProviderID,
			RequestPath:    "/v1/audio/transcriptions",
			StatusCode:     status,
			Cost:           0,
			LatencyMS:      int(time.Since(started).Milliseconds()),
			ErrorMessage:   msg,
			ClientApp:      getClientAppName(r),
			RequestedModel: targetModel.Name,
			Complexity:     "direct",
			CreatedAt:      started,
		}
		applyRequestContext(&entry, r, key, correlation)
		applyBudgetWindow(&entry, budgetWindow)
		h.saveRequestLog(entry)
	}

	if provider == nil || provider.Status != "active" {
		saveTranscriptionFailure(http.StatusBadRequest, fmt.Sprintf("provider '%s' is inactive or missing", targetModel.ProviderID), time.Now())
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("Provider '%s' is inactive or missing", targetModel.ProviderID), "invalid_request_error")
		return
	}

	// Extract file
	file, header, err := r.FormFile("file")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Failed to get file field: "+err.Error(), "invalid_request_error")
		return
	}
	defer file.Close()

	// Recreate multipart request to send upstream
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to create upstream multipart file: "+err.Error(), "api_error")
		return
	}
	// The byte count is captured here because it is the one measure of the audio
	// the server observes directly. It backstops the billed duration when the
	// provider reports none (see durationFromBytes).
	uploadedBytes, err := io.Copy(part, file)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to copy file data: "+err.Error(), "api_error")
		return
	}

	// Set target model
	if err := writer.WriteField("model", targetModel.TargetModel); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to write model field: "+err.Error(), "api_error")
		return
	}

	// Copy other form fields (language and response_format are handled
	// explicitly below so exactly one normalized value is sent).
	for k, vals := range r.MultipartForm.Value {
		if k != "model" && k != "file" && k != "durationSec" && k != "language" && k != "response_format" {
			for _, v := range vals {
				_ = writer.WriteField(k, v)
			}
		}
	}

	// response_format is pinned server-side: verbose_json is the only shape that
	// carries the provider's own `duration`, which is what this route bills on.
	// Letting the client choose the response shape let it choose whether the
	// server could see the duration at all.
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to write response_format field: "+err.Error(), "api_error")
		return
	}

	// Always send exactly one transcription language. Precedence: the client's
	// field → the system-setting default (default_transcription_language) → "ar".
	// Some providers behind OpenRouter 400 without a language, and Whisper
	// otherwise auto-detects and often mis-labels short/accented Arabic as
	// English — an explicit hint fixes both.
	lang := normalizeLangCode(r.FormValue("language"))
	if lang == "" {
		def, _ := h.db.GetSetting("default_transcription_language")
		lang = normalizeLangCode(def)
	}
	if lang == "" {
		lang = "ar"
	}
	_ = writer.WriteField("language", lang)

	if err := writer.Close(); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to close multipart writer: "+err.Error(), "api_error")
		return
	}

	// Determine upstream URL
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/audio/transcriptions") {
		url += "/audio/transcriptions"
	}

	startTime := time.Now()
	// Audio providers do not expose usage before inference. Derive a
	// conservative duration ceiling from the accepted upload size at 8 kbit/s,
	// with a one-minute minimum. Completed usage is capped at this amount.
	maxDurationMillis := uploadedBytes
	if maxDurationMillis < 60_000 {
		maxDurationMillis = 60_000
	}
	chargeCeiling, err := money.MulDivCeil(maxDurationMillis, targetModel.PricePerMinuteNano, 60_000)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid transcription pricing: "+err.Error(), "invalid_request_error")
		return
	}
	budgetWindow, err = h.db.GetBudgetAvailability(r.Context(), key.UserID)
	if err != nil {
		saveTranscriptionFailure(budgetErrorStatus(err), err.Error(), startTime)
		h.writeBudgetError(w, err)
		return
	}
	if chargeCeiling > budgetWindow.Available {
		err = &db.BudgetExceededError{Requested: chargeCeiling, Available: budgetWindow.Available}
		saveTranscriptionFailure(http.StatusPaymentRequired, err.Error(), startTime)
		h.writeBudgetError(w, err)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, &requestBody)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to create upstream request: "+err.Error(), "api_error")
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	// Attribution + sticky-session parity with the chat path when the
	// transcription provider is OpenRouter (no-op for any other provider).
	setOpenRouterHeaders(req, provider, r)

	resp, err := httpClient.Do(req)
	if err != nil {
		status := requestFailureStatus(requestFailure{
			requestErr: r.Context().Err(),
			message:    err.Error(),
		})
		saveTranscriptionFailure(status, "connection to upstream failed: "+err.Error(), startTime)
		h.writeError(w, status, "Connection to upstream failed: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to read upstream response: "+err.Error(), "api_error")
		return
	}

	latencyMs := int(time.Since(startTime).Milliseconds())

	if resp.StatusCode >= 400 {
		// Log the upstream rejection (e.g. an unkeyed provider's 401, or a 429)
		// so the failure is visible in the admin logs with its real cause.
		upstreamMsg := string(respBody)
		if len(upstreamMsg) > 500 {
			upstreamMsg = upstreamMsg[:500]
		}
		saveTranscriptionFailure(resp.StatusCode, "upstream returned status "+strconv.Itoa(resp.StatusCode)+": "+upstreamMsg, startTime)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// Extract text for log
	var textResult string
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(respBody, &jsonMap); err == nil {
		if t, ok := jsonMap["text"].(string); ok {
			textResult = t
		}
	}

	// Duration is what this route BILLS ON, so it must not come from the client.
	// It used to be read straight out of the multipart form: omit the field and
	// the persisted cost was $0, which meant free transcription at the
	// operator's expense AND — because transcription spend shares the per-user
	// budget window that gates chat — usage that never accrued against any
	// limit. Every other billable path derives cost from upstream-reported
	// usage; this one now does too.
	//
	// Preference order: the provider's own reported duration, then a floor
	// derived from the uploaded bytes, and only then the client's hint (kept as
	// a last resort for providers that report nothing, and never trusted below
	// the byte-derived floor).
	durationSec := 0.0
	if reported, ok := jsonMap["duration"].(float64); ok && reported > 0 {
		durationSec = reported
	}
	if durationSec <= 0 {
		durationSec = durationFromBytes(uploadedBytes)
	}
	if hint := r.FormValue("durationSec"); hint != "" {
		if d, err := strconv.ParseFloat(hint, 64); err == nil && d > durationSec {
			durationSec = d // a client may only ever revise the bill UPWARD
		}
	}

	durationMillis := int64(math.Ceil(durationSec * 1000))
	costNano, err := money.MulDivCeil(durationMillis, targetModel.PricePerMinuteNano, 60_000)
	if err != nil {
		costNano = chargeCeiling
	}

	// Log request and deduct credits
	logEntry := db.RequestLog{
		ChargeCeilingNanoUSD: chargeCeiling,
		ModelID:              targetModel.ID,
		ProviderID:           targetModel.ProviderID,
		RequestPath:          "/v1/audio/transcriptions",
		StatusCode:           resp.StatusCode,
		InputTokens:          int(durationSec),
		OutputTokens:         len(strings.Fields(textResult)),
		Cost:                 costNano.USD(),
		CostNanoUSD:          costNano,
		LatencyMS:            latencyMs,
		ClientApp:            getClientAppName(r),
		RequestedModel:       targetModel.Name,
		Complexity:           "direct",
		CreatedAt:            startTime,
	}
	applyRequestContext(&logEntry, r, key, correlation)
	applyBudgetWindow(&logEntry, budgetWindow)

	h.saveRequestLog(logEntry)

	// Return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}

// metaChunkClientApps is the allowlist of client apps that receive the
// non-standard muhiya_log cost chunk (003 D1). MuhiyaCode is included so the
// terminal agent can display exact per-request credits.
var metaChunkClientApps = map[string]bool{
	"MuhiyaChat": true,
	"MuhiyaCode": true,
}

func sendMuhiyaMetaChunk(w http.ResponseWriter, log *db.RequestLog, modelName string) {
	if !metaChunkClientApps[log.ClientApp] {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	metaBytes, err := json.Marshal(map[string]interface{}{
		"id":     log.ID,
		"object": "chat.completion.chunk",
		"model":  modelName,
		"usage": map[string]interface{}{
			"prompt_tokens":     log.InputTokens,
			"completion_tokens": log.OutputTokens,
			"total_tokens":      log.InputTokens + log.OutputTokens,
		},
		"muhiya_log": map[string]interface{}{
			"cost":            log.Cost,
			"log_id":          log.ID,
			"usage_estimated": log.UsageEstimated,
		},
	})
	if err == nil {
		w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(metaBytes))))
		flusher.Flush()
	}
}
