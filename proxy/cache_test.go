package proxy

import (
	"encoding/json"
	"testing"
)

func TestCacheReadTokensDialects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "openai details shape",
			body: `{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":768}}`,
			want: 768,
		},
		{
			name: "deepseek shape",
			body: `{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_cache_hit_tokens":896,"prompt_cache_miss_tokens":104}`,
			want: 896,
		},
		{
			name: "both shapes reported - larger wins",
			body: `{"prompt_tokens":1000,"completion_tokens":50,"prompt_cache_hit_tokens":896,"prompt_tokens_details":{"cached_tokens":512}}`,
			want: 896,
		},
		{
			name: "no cache fields",
			body: `{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050}`,
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var usage OpenAIUsage
			if err := json.Unmarshal([]byte(tc.body), &usage); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := usage.CacheReadTokens(); got != tc.want {
				t.Fatalf("CacheReadTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCacheReadTokensNilReceiver(t *testing.T) {
	var usage *OpenAIUsage
	if got := usage.CacheReadTokens(); got != 0 {
		t.Fatalf("nil receiver = %d, want 0", got)
	}
}

func TestStreamChunkCarriesDeepSeekCacheFields(t *testing.T) {
	chunkJSON := `{"id":"x","choices":[],"usage":{"prompt_tokens":2048,"completion_tokens":10,"prompt_cache_hit_tokens":1984}}`
	var chunk OpenAIChunk
	if err := json.Unmarshal([]byte(chunkJSON), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if chunk.Usage == nil || chunk.Usage.CacheReadTokens() != 1984 {
		t.Fatalf("expected 1984 cache-read tokens from stream chunk, got %+v", chunk.Usage)
	}
}

func TestMiniMaxCacheAccounting(t *testing.T) {
	const baseURL = "https://api.minimax.io/v1"
	var warm OpenAIUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":700}}`), &warm); err != nil {
		t.Fatal(err)
	}
	if got := warm.CacheReadTokensFor(baseURL, "MiniMax-M3"); got != 700 {
		t.Fatalf("cached tokens = %d, want 700", got)
	}
	miss := warm.CacheMissTokensFor(baseURL, "MiniMax-M3")
	if miss == nil || *miss != 300 {
		t.Fatalf("derived miss = %v, want 300", miss)
	}

	below := OpenAIUsage{PromptTokens: 511, PromptTokensDetails: &PromptTokensDetail{CachedTokens: 400}}
	if got := below.CacheReadTokensFor(baseURL, "MiniMax-M3"); got != 0 || below.CacheMissTokensFor(baseURL, "MiniMax-M3") != nil {
		t.Fatalf("sub-threshold cache accounting must be zero/unavailable: read=%d miss=%v", got, below.CacheMissTokensFor(baseURL, "MiniMax-M3"))
	}
}

func TestInjectAnthropicCacheControlStringShapes(t *testing.T) {
	body := map[string]interface{}{
		"system": "You are a coding agent.",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "fix the bug"},
		},
	}
	if !InjectAnthropicCacheControl(body, "claude-sonnet-4-5") {
		t.Fatal("expected injection on claude-sonnet-4-5")
	}
	system, ok := body["system"].([]interface{})
	if !ok || len(system) != 1 {
		t.Fatalf("system not converted to blocks: %#v", body["system"])
	}
	sysBlock := system[0].(map[string]interface{})
	if sysBlock["cache_control"] == nil || sysBlock["text"] != "You are a coding agent." {
		t.Fatalf("system block missing cache_control or text: %#v", sysBlock)
	}
	message := body["messages"].([]interface{})[0].(map[string]interface{})
	content, ok := message["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("message content not converted to blocks: %#v", message["content"])
	}
	if content[0].(map[string]interface{})["cache_control"] == nil {
		t.Fatal("last message block missing cache_control")
	}
}

func TestInjectAnthropicCacheControlBlockShapes(t *testing.T) {
	body := map[string]interface{}{
		"system": []interface{}{
			map[string]interface{}{"type": "text", "text": "sys"},
		},
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "t1", "content": "ok"},
			}},
		},
	}
	if !InjectAnthropicCacheControl(body, "claude-opus-4-8") {
		t.Fatal("expected injection")
	}
	sysBlock := body["system"].([]interface{})[0].(map[string]interface{})
	if sysBlock["cache_control"] == nil {
		t.Fatal("system block missing cache_control")
	}
	lastBlock := body["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if lastBlock["cache_control"] == nil {
		t.Fatal("tool_result block missing cache_control")
	}
}

func TestInjectAnthropicCacheControlRespectsClientBreakpoints(t *testing.T) {
	body := map[string]interface{}{
		"system": "sys",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "hello", "cache_control": map[string]interface{}{"type": "ephemeral"}},
			}},
		},
	}
	if InjectAnthropicCacheControl(body, "claude-sonnet-4-5") {
		t.Fatal("must not inject when the client already set cache_control")
	}
	if _, isString := body["system"].(string); !isString {
		t.Fatal("system must stay untouched when skipping")
	}
}

func TestInjectAnthropicCacheControlGating(t *testing.T) {
	for _, model := range []string{"claude-2.1", "claude-instant-1.2", "deepseek-chat", "glm-5"} {
		body := map[string]interface{}{"system": "sys", "messages": []interface{}{}}
		if InjectAnthropicCacheControl(body, model) {
			t.Fatalf("must not inject for %s", model)
		}
		if _, isString := body["system"].(string); !isString {
			t.Fatalf("system mutated for unsupported model %s", model)
		}
	}
	for _, model := range []string{"claude-3-5-sonnet-20241022", "claude-3-haiku-20240307", "claude-sonnet-5"} {
		body := map[string]interface{}{"system": "sys", "messages": []interface{}{}}
		if !InjectAnthropicCacheControl(body, model) {
			t.Fatalf("expected injection for %s", model)
		}
	}
}

func TestInjectAnthropicCacheControlSkipsThinkingBlocks(t *testing.T) {
	body := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "assistant", "content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "..."},
			}},
		},
	}
	InjectAnthropicCacheControl(body, "claude-sonnet-4-5")
	block := body["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if block["cache_control"] != nil {
		t.Fatal("thinking blocks must never receive cache_control")
	}
}
