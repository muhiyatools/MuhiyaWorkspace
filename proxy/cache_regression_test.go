package proxy

import (
	"encoding/json"
	"reflect"
	"testing"

	"gateway/db"
)

// deepSeekTransform mirrors the byte-affecting steps proxyOpenAIToOpenAI applies
// to a DeepSeek-bound body (model swap, web_search strip, thinking translation,
// the OpenRouter-cache no-op, stream_options, identity sanitization) then
// marshals — i.e. the exact bytes DeepSeek receives, and whose stability its
// prefix cache depends on.
func deepSeekTransform(t *testing.T, body map[string]interface{}, level string) []byte {
	t.Helper()
	body["model"] = "deepseek-chat"
	delete(body, "web_search")
	ApplyThinkingOpenAI(body, "https://api.deepseek.com", "deepseek-chat", level, false)
	// Non-OpenRouter provider: this must be a strict no-op (byte stability).
	InjectOpenRouterAnthropicCache(body, false, "deepseek-chat")
	if stream, _ := body["stream"].(bool); stream {
		body["stream_options"] = map[string]interface{}{"include_usage": true}
	}
	sanitizeUpstreamIdentity(body, famDeepseek, "", "")
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func cloneBody(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, _ := json.Marshal(body)
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// B1: the DeepSeek transform is a pure, deterministic function of its input, so
// two identical turns produce byte-identical upstream bodies — a prerequisite for
// prefix-cache hits. A future non-deterministic mutation (map iteration folded
// into the body, a timestamp) would break this and silently tank the hit rate.
func TestDeepSeekTransformDeterministic(t *testing.T) {
	base := feature009DeepSeekBody()
	a := deepSeekTransform(t, cloneBody(t, base), "high")
	b := deepSeekTransform(t, cloneBody(t, base), "high")
	if string(a) != string(b) {
		t.Fatalf("transform not deterministic:\n a=%s\n b=%s", a, b)
	}
}

// B1: turn 2 (same conversation plus one more message) must leave every earlier
// message byte-identical to turn 1. DeepSeek's prefix cache keys on the
// serialized prefix, so any mutation or reordering of prior messages misses it.
func TestDeepSeekTransformPreservesPrefix(t *testing.T) {
	base := feature009DeepSeekBody()
	turn1 := deepSeekTransform(t, cloneBody(t, base), "high")

	turn2Body := cloneBody(t, base)
	msgs := turn2Body["messages"].([]interface{})
	turn2Body["messages"] = append(msgs, map[string]interface{}{"role": "user", "content": "and now fix it"})
	turn2 := deepSeekTransform(t, turn2Body, "high")

	var m1, m2 map[string]interface{}
	if err := json.Unmarshal(turn1, &m1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(turn2, &m2); err != nil {
		t.Fatal(err)
	}
	msgs1 := m1["messages"].([]interface{})
	msgs2 := m2["messages"].([]interface{})
	if len(msgs2) != len(msgs1)+1 {
		t.Fatalf("turn 2 should have exactly one more message, got %d vs %d", len(msgs2), len(msgs1))
	}
	for i := range msgs1 {
		if !reflect.DeepEqual(msgs1[i], msgs2[i]) {
			t.Fatalf("prefix message %d changed between turns:\n t1=%v\n t2=%v", i, msgs1[i], msgs2[i])
		}
	}
}

// B3: an OpenRouter-shaped usage payload reports cache reads via
// prompt_tokens_details.cached_tokens and cache writes via
// prompt_tokens_details.cache_write_tokens. Both must be parsed and billed,
// including the write-rate fallback (a cache write bills at the input rate when
// the model row carries no explicit cache-write price).
func TestOpenRouterUsageParsingAndBilling(t *testing.T) {
	const raw = `{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,
		"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":150}}`
	var u OpenAIUsage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if got := u.CacheReadTokens(); got != 800 {
		t.Fatalf("cache read = %d, want 800", got)
	}
	if got := u.CacheWriteTokensReported(); got != 150 {
		t.Fatalf("cache write = %d, want 150", got)
	}

	// input $2/M, output $8/M, cache-read $0.5/M, NO explicit cache-write price.
	model := &db.Model{
		Name: "or-model", TargetModel: "anthropic/claude",
		InputCostPerMillion: 2.0, OutputCostPerMillion: 8.0, CacheReadCostPerMillion: 0.5,
	}
	cost := calculateCost(model, u.PromptTokens, u.CompletionTokens, u.CacheReadTokens(), u.CacheWriteTokensReported())
	// standardInput = 1000-800-150 = 50 → 50/1e6*2   = 0.0001
	// output        = 200/1e6*8               = 0.0016
	// cacheRead     = 800/1e6*0.5             = 0.0004
	// cacheWrite    = 150/1e6*2 (input-rate fallback) = 0.0003
	want := 0.0001 + 0.0016 + 0.0004 + 0.0003
	if diff := cost - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %.10f, want %.10f", cost, want)
	}
}

// B3: DeepSeek reports cache usage under its own field names; a hit must be read
// from prompt_cache_hit_tokens and the miss derived, so DeepSeek requests bill
// their cached prefix correctly rather than logging 0 cache tokens.
func TestDeepSeekUsageDialectParsing(t *testing.T) {
	const raw = `{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,
		"prompt_cache_hit_tokens":700,"prompt_cache_miss_tokens":300}`
	var u OpenAIUsage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if got := u.CacheReadTokensFor("https://api.deepseek.com", "deepseek-chat"); got != 700 {
		t.Fatalf("deepseek cache read = %d, want 700", got)
	}
	miss := u.CacheMissTokensFor("https://api.deepseek.com", "deepseek-chat")
	if miss == nil || *miss != 300 {
		t.Fatalf("deepseek cache miss = %v, want 300", miss)
	}
}
