package proxy

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// keepAliveConfig configures the shared upstream fixture used by the streaming
// relay / watchdog regression tests (feature 007 T002). It models a real
// DeepSeek-style SSE upstream that can stall in each of the ways the gateway's
// relay and idle watchdog must survive.
type keepAliveConfig struct {
	// HeaderDelay withholds the response STATUS/headers for this long, emulating
	// the pre-inference window (queue + prompt-cache build) before the first byte.
	HeaderDelay time.Duration
	// KeepAlives is how many ": keep-alive" SSE comment lines to stream before any
	// real data - liveness traffic that carries no tokens.
	KeepAlives int
	// KeepAliveGap is the pause between successive keep-alive comment lines.
	KeepAliveGap time.Duration
	// SilentFor holds the connection open producing nothing (after the keep-alives)
	// to emulate a mid-response stall.
	SilentFor time.Duration
	// Body, when set, is the sequence of SSE data payloads (without the "data: "
	// prefix or trailing newlines) streamed after the keep-alive/silence phase.
	// A terminal "[DONE]" line is always appended.
	Body []string
	// Status, when non-zero, is written INSTEAD of a stream: the handler responds
	// with this status code (optionally after HeaderDelay), the RespHeaders, and
	// RespBody. Used for the 429 / Retry-After paths.
	Status      int
	RespHeaders map[string]string
	RespBody    string
}

// newKeepAliveUpstream returns an httptest.Server that replays cfg. The caller
// is responsible for Close(). It is safe for a single request per test.
func newKeepAliveUpstream(cfg keepAliveConfig) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.HeaderDelay > 0 {
			time.Sleep(cfg.HeaderDelay)
		}

		if cfg.Status != 0 {
			for k, v := range cfg.RespHeaders {
				w.Header().Set(k, v)
			}
			w.WriteHeader(cfg.Status)
			if cfg.RespBody != "" {
				_, _ = w.Write([]byte(cfg.RespBody))
			}
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}

		for i := 0; i < cfg.KeepAlives; i++ {
			// A comment line and a blank line - the exact liveness shape an
			// upstream sends to keep the socket warm without emitting tokens.
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flush()
			if cfg.KeepAliveGap > 0 {
				time.Sleep(cfg.KeepAliveGap)
			}
		}

		if cfg.SilentFor > 0 {
			time.Sleep(cfg.SilentFor)
		}

		for _, payload := range cfg.Body {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
	}))
}

// TestKeepAliveUpstreamFixture is a self-test of the shared fixture (no DB): it
// proves the fixture emits ": keep-alive" comment lines, then the data frames,
// then [DONE] - so the relay tests that consume it can trust its shape.
func TestKeepAliveUpstreamFixture(t *testing.T) {
	srv := newKeepAliveUpstream(keepAliveConfig{
		KeepAlives: 2,
		Body:       []string{`{"choices":[{"delta":{"content":"hi"}}]}`},
	})
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET fixture: %v", err)
	}
	defer resp.Body.Close()

	var keepAlives, dataLines int
	sawDone := false
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == ": keep-alive":
			keepAlives++
		case line == "data: [DONE]":
			sawDone = true
		case strings.HasPrefix(line, "data: "):
			dataLines++
		}
	}
	if keepAlives != 2 {
		t.Errorf("keep-alive lines = %d, want 2", keepAlives)
	}
	if dataLines != 1 {
		t.Errorf("data lines = %d, want 1", dataLines)
	}
	if !sawDone {
		t.Error("fixture never emitted the terminal [DONE] line")
	}
}
