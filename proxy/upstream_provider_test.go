package proxy

import (
	"encoding/json"
	"testing"
)

// OpenRouter names the upstream it routed to in every chunk and in the
// non-streaming response. Both structs used to omit the field, and Go's
// encoding/json discards undeclared fields SILENTLY — so the one piece of
// evidence that could explain a per-upstream cache miss was being thrown away
// on arrival.
//
// Why this matters concretely: a model slug on OpenRouter is served by many
// upstreams (minimax-m3 by nine), each with its own prompt cache. A request
// re-routed to a peer re-reads the entire conversation at full input price,
// with byte-identical input. Without this field there is nothing anywhere —
// client, gateway, or dashboard — that can distinguish that from an unexplained
// miss. These tests exist so the field cannot be dropped again during a
// refactor of the response structs.

func TestChunkCarriesTheUpstreamProvider(t *testing.T) {
	var chunk OpenAIChunk
	raw := `{"id":"gen-1","object":"chat.completion.chunk","model":"minimax/minimax-m3","provider":"Novita","choices":[{"delta":{"content":"hi"}}]}`
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		t.Fatal(err)
	}
	if chunk.Provider != "Novita" {
		t.Fatalf("chunk.Provider = %q, want Novita — the routing evidence was dropped", chunk.Provider)
	}
}

func TestResponseCarriesTheUpstreamProvider(t *testing.T) {
	var response OpenAIResponse
	raw := `{"id":"gen-1","object":"chat.completion","model":"minimax/minimax-m3","provider":"DeepInfra","choices":[],"usage":{"prompt_tokens":10}}`
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if response.Provider != "DeepInfra" {
		t.Fatalf("response.Provider = %q, want DeepInfra", response.Provider)
	}
}

// A direct (non-routed) upstream sends no such field, and that must decode to
// empty rather than erroring — the column is nullable precisely so a direct
// connection stays distinguishable from a routed one we failed to parse.
func TestAbsentProviderDecodesEmpty(t *testing.T) {
	var chunk OpenAIChunk
	if err := json.Unmarshal([]byte(`{"id":"x","choices":[{"delta":{"content":"hi"}}]}`), &chunk); err != nil {
		t.Fatal(err)
	}
	if chunk.Provider != "" {
		t.Fatalf("a direct upstream must report no provider, got %q", chunk.Provider)
	}
}
