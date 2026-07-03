package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func iptr(i int) *int { return &i }

func TestToolCallAccumulatorIndexed(t *testing.T) {
	a := newToolCallAccumulator()
	// Fragmented single call at index 0.
	a.add([]OpenAIToolCall{{Index: iptr(0), ID: "call_1", Function: OpenAIFunctionCall{Name: "web_search"}}})
	a.add([]OpenAIToolCall{{Index: iptr(0), Function: OpenAIFunctionCall{Arguments: `{"query":"m`}}})
	a.add([]OpenAIToolCall{{Index: iptr(0), Function: OpenAIFunctionCall{Arguments: `essi"}`}}})

	calls := a.finalize()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "web_search" {
		t.Fatalf("wrong name: %s", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"query":"messi"}` {
		t.Fatalf("args not stitched: %q", calls[0].Function.Arguments)
	}
	if calls[0].ID != "call_1" {
		t.Fatalf("id lost: %q", calls[0].ID)
	}
}

func TestToolCallAccumulatorTwoIndexed(t *testing.T) {
	a := newToolCallAccumulator()
	a.add([]OpenAIToolCall{{Index: iptr(0), ID: "c1", Function: OpenAIFunctionCall{Name: "search_places"}}})
	a.add([]OpenAIToolCall{{Index: iptr(1), ID: "c2", Function: OpenAIFunctionCall{Name: "web_search"}}})
	a.add([]OpenAIToolCall{{Index: iptr(0), Function: OpenAIFunctionCall{Arguments: `{"query":"restaurants"}`}}})
	a.add([]OpenAIToolCall{{Index: iptr(1), Function: OpenAIFunctionCall{Arguments: `{"query":"x"}`}}})

	calls := a.finalize()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "search_places" || calls[1].Function.Name != "web_search" {
		t.Fatalf("order/name wrong: %+v", calls)
	}
}

func TestToolCallAccumulatorSequentialNoIndex(t *testing.T) {
	a := newToolCallAccumulator()
	// No index provided — rely on the sequential heuristic.
	a.add([]OpenAIToolCall{{ID: "c1", Function: OpenAIFunctionCall{Name: "get_weather"}}})
	a.add([]OpenAIToolCall{{Function: OpenAIFunctionCall{Arguments: `{}`}}})

	calls := a.finalize()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "get_weather" || calls[0].Function.Arguments != "{}" {
		t.Fatalf("sequential accumulation failed: %+v", calls[0])
	}
}

func TestFinalizeSkipsNamelessCalls(t *testing.T) {
	a := newToolCallAccumulator()
	// A stray fragment with args but never a name must be dropped.
	a.add([]OpenAIToolCall{{Index: iptr(0), Function: OpenAIFunctionCall{Arguments: "{}"}}})
	if calls := a.finalize(); len(calls) != 0 {
		t.Fatalf("expected nameless call to be skipped, got %d", len(calls))
	}
}

func TestInjectToolGuidanceAppendsToSystem(t *testing.T) {
	msgs := []OpenAIMessage{
		{Role: "system", Content: "You are Muhiya."},
		{Role: "user", Content: "hi"},
	}
	out := injectToolGuidance(msgs, "dummy skill")
	if len(out) != 2 {
		t.Fatalf("should not add a message when a system prompt exists, got %d", len(out))
	}
	s, _ := out[0].Content.(string)
	if !strings.HasPrefix(s, "You are Muhiya.") || !strings.Contains(s, "web_search") {
		t.Fatalf("guidance not appended to system prompt: %q", s)
	}
	// Original slice must be untouched.
	if msgs[0].Content.(string) != "You are Muhiya." {
		t.Fatalf("injectToolGuidance mutated the caller's slice")
	}
}

func TestInjectToolGuidancePrependsWhenNoSystem(t *testing.T) {
	msgs := []OpenAIMessage{{Role: "user", Content: "hi"}}
	out := injectToolGuidance(msgs, "dummy skill")
	if len(out) != 2 || out[0].Role != "system" {
		t.Fatalf("expected a prepended system message, got %+v", out)
	}
}

func TestShouldRunAgentLoopGating(t *testing.T) {
	h := &ProxyHandler{}
	enabled := ToolSettings{SerperAPIKey: "x"}

	mk := func(clientApp string, stream bool, tools []OpenAITool) bool {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if clientApp != "" {
			r.Header.Set("X-Client-App", clientApp)
		}
		req := &OpenAIRequest{Stream: stream, Tools: tools}
		return h.shouldRunAgentLoop(r, req, enabled)
	}

	if !mk("MuhiyaChat", true, nil) {
		t.Fatalf("should run for MuhiyaChat streaming with tools configured")
	}
	if mk("Claude Code", true, nil) {
		t.Fatalf("must NOT run for other clients")
	}
	if mk("MuhiyaChat", false, nil) {
		t.Fatalf("must NOT run for non-streaming")
	}
	if mk("MuhiyaChat", true, []OpenAITool{{Type: "function"}}) {
		t.Fatalf("must NOT run when client supplied its own tools")
	}
	// No keys configured -> disabled regardless.
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Client-App", "MuhiyaChat")
	if h.shouldRunAgentLoop(r, &OpenAIRequest{Stream: true}, ToolSettings{}) {
		t.Fatalf("must NOT run when no tool keys are configured")
	}
}
