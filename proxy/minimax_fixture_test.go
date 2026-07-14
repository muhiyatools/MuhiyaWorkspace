package proxy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// simulatedMiniMaxFixture is an authored, deterministic MiniMax-compatible
// upstream. It never calls the live service. Its Server.URL is the single
// base-URL seam a funded live conformance run could replace in the future.
type simulatedMiniMaxFixture struct {
	Server *httptest.Server

	mu         sync.Mutex
	requests   int
	rejections int
	seenBodies map[[32]byte]int
}

func newSimulatedMiniMaxFixture(t *testing.T) *simulatedMiniMaxFixture {
	t.Helper()
	f := &simulatedMiniMaxFixture{seenBodies: make(map[[32]byte]int)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *simulatedMiniMaxFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests++
	sequence := f.requests
	hash := sha256.Sum256(body)
	prior := f.seenBodies[hash]
	f.seenBodies[hash] = prior + 1
	f.mu.Unlock()

	if err := validateSimulatedMiniMaxRequest(r, body); err != nil {
		f.mu.Lock()
		f.rejections++
		f.mu.Unlock()
		http.Error(w, `{"error":{"type":"invalid_request_error","message":`+fmt.Sprintf("%q", err.Error())+`}}`, http.StatusBadRequest)
		return
	}

	cached := 0
	if prior > 0 {
		cached = 700
	}
	w.Header().Set("Content-Type", "text/event-stream")
	callID := fmt.Sprintf("call-sim-%02d", sequence)
	arguments := fmt.Sprintf(`{"path":"fixture-%02d.go"}`, sequence)
	frames := []map[string]interface{}{
		{"id": "chatcmpl-sim", "object": "chat.completion.chunk", "model": "MiniMax-M3", "choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]interface{}{"role": "assistant", "reasoning_details": []interface{}{map[string]interface{}{"type": "text", "text": "simulated reasoning"}}}}}},
		{"id": "chatcmpl-sim", "object": "chat.completion.chunk", "model": "MiniMax-M3", "choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"index": 0, "id": callID, "type": "function", "function": map[string]interface{}{"name": "read_file", "arguments": arguments}}}}, "finish_reason": "tool_calls"}}},
		{"id": "chatcmpl-sim", "object": "chat.completion.chunk", "model": "MiniMax-M3", "choices": []interface{}{}, "usage": map[string]interface{}{"prompt_tokens": 1000, "completion_tokens": 25, "total_tokens": 1025, "prompt_tokens_details": map[string]interface{}{"cached_tokens": cached}}},
	}
	for _, frame := range frames {
		encoded, _ := json.Marshal(frame)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func validateSimulatedMiniMaxRequest(r *http.Request, body []byte) error {
	if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
		return fmt.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return fmt.Errorf("missing bearer authorization")
	}
	var request map[string]interface{}
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if request["model"] != "MiniMax-M3" || request["reasoning_split"] != true {
		return fmt.Errorf("model/reasoning normalization missing")
	}
	if _, leaked := request["reasoning_effort"]; leaked {
		return fmt.Errorf("raw reasoning_effort leaked")
	}
	tools, ok := request["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return fmt.Errorf("standard tools array missing")
	}
	tool, ok := tools[0].(map[string]interface{})
	if !ok || tool["type"] != "function" || tool["function"] == nil {
		return fmt.Errorf("invalid standard tool shape")
	}
	messages, ok := request["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return fmt.Errorf("messages missing")
	}
	knownCalls := make(map[string]bool)
	for _, raw := range messages {
		message, ok := raw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("message is not an object")
		}
		if calls, ok := message["tool_calls"].([]interface{}); ok {
			for _, rawCall := range calls {
				call := rawCall.(map[string]interface{})
				id, _ := call["id"].(string)
				function, _ := call["function"].(map[string]interface{})
				if id == "" {
					return fmt.Errorf("tool call ID missing")
				}
				if _, ok := function["arguments"].(string); !ok {
					return fmt.Errorf("tool arguments are not a JSON string")
				}
				knownCalls[id] = true
			}
		}
		if message["role"] == "tool" {
			id, _ := message["tool_call_id"].(string)
			if !knownCalls[id] {
				return fmt.Errorf("tool result lost its matching call ID")
			}
		}
	}
	return nil
}

func (f *simulatedMiniMaxFixture) stats() (requests, rejections int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.rejections
}

func TestMiniMaxFixtureRejectsLegacyShapeSimulated(t *testing.T) {
	fixture := newSimulatedMiniMaxFixture(t)
	req, _ := http.NewRequest(http.MethodPost, fixture.Server.URL+"/chat/completions", strings.NewReader(`{"model":"MiniMax-M3","messages":[]}`))
	req.Header.Set("Authorization", "Bearer simulated")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy request status = %d", resp.StatusCode)
	}
}
