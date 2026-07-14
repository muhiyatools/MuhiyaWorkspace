package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// capturedRequest is the exact upstream request a capture helper observed:
// the raw body bytes and a clone of the headers, for assertions about what the
// gateway actually forwarded (feature 007 T003).
type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// JSON decodes the captured body into a generic map for field-level assertions.
func (c *capturedRequest) JSON(t *testing.T) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("captured body is not JSON: %v (body=%q)", err, string(c.Body))
	}
	return m
}

// captureUpstream is an httptest upstream that records the exact request it
// received before replying. It is the recording seam for relay tests: point a
// provider.BaseURL at .Server.URL and inspect .Last() afterwards. Replies with
// Status/Body (defaults: 200 and an empty SSE-safe payload).
type captureUpstream struct {
	Server *httptest.Server
	Status int
	Body   string

	mu   sync.Mutex
	last *capturedRequest
}

// newCaptureUpstream starts a recording upstream returning the given status and
// body. The caller must Close() it.
func newCaptureUpstream(status int, body string) *captureUpstream {
	c := &captureUpstream{Status: status, Body: body}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.last = &capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   raw,
		}
		c.mu.Unlock()
		if c.Status != 0 {
			w.WriteHeader(c.Status)
		}
		if c.Body != "" {
			_, _ = w.Write([]byte(c.Body))
		}
	}))
	return c
}

// Last returns the most recently captured request, or nil if none arrived.
func (c *captureUpstream) Last() *capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *captureUpstream) Close() { c.Server.Close() }

// TestCaptureUpstreamRecordsRequest self-tests the capture helper (no DB): it
// must record the exact body bytes and headers of the upstream request so the
// relay tests that rely on it are trustworthy.
func TestCaptureUpstreamRecordsRequest(t *testing.T) {
	up := newCaptureUpstream(http.StatusOK, `{"ok":true}`)
	defer up.Close()

	body := `{"model":"deepseek-reasoner","stream":false}`
	req, _ := http.NewRequest(http.MethodPost, up.Server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()

	last := up.Last()
	if last == nil {
		t.Fatal("capture upstream recorded no request")
	}
	if last.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", last.Method)
	}
	if last.Path != "/v1/chat/completions" {
		t.Errorf("path = %q", last.Path)
	}
	if got := last.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("captured Authorization = %q", got)
	}
	if m := last.JSON(t); m["model"] != "deepseek-reasoner" {
		t.Errorf("captured body model = %v", m["model"])
	}
}
