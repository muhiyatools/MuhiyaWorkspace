package proxy

import (
	"net/http/httptest"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestNormalizeThinkingLevel(t *testing.T) {
	cases := map[string]string{
		"minimal": "minimal", "MIN": "minimal", "none": "minimal", "off": "minimal",
		"low": "low", "medium": "medium", "MID": "medium",
		"high": "high", "ultra": "high",
		"max": "max", "xhigh": "max",
		"garbage": "", "": "", "  high  ": "high",
	}
	for input, want := range cases {
		if got := NormalizeThinkingLevel(input); got != want {
			t.Errorf("NormalizeThinkingLevel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveThinkingLevelPrecedence(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(EffortHeader, "max")

	// The header is the full-fidelity Muhiya channel and WINS over the body:
	// Muhiya clients send header "max" alongside a clamped body "high" for
	// generic-endpoint compatibility - the clamp must not downgrade us.
	if got := ResolveThinkingLevel(r, strPtr("high")); got != "max" {
		t.Errorf("header should win over clamped body, got %q", got)
	}
	// Body used when no header is present.
	noHeader := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if got := ResolveThinkingLevel(noHeader, strPtr("low")); got != "low" {
		t.Errorf("body fallback failed, got %q", got)
	}
	if got := ResolveThinkingLevel(r, nil); got != "max" {
		t.Errorf("header alone failed, got %q", got)
	}
	// Garbage header falls through to the body.
	badHeader := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	badHeader.Header.Set(EffortHeader, "nonsense")
	if got := ResolveThinkingLevel(badHeader, strPtr("medium")); got != "medium" {
		t.Errorf("garbage header should fall back to body, got %q", got)
	}
	if got := ResolveThinkingLevel(nil, nil); got != "" {
		t.Errorf("no signals should resolve to empty, got %q", got)
	}
	// Explicit thinking objects are a router signal, never a mapping level.
	if !clientRequestsThinking(&AnthropicThinking{Type: "enabled", BudgetTokens: 2048}) {
		t.Error("enabled thinking object should count as a thinking request")
	}
	if clientRequestsThinking(&AnthropicThinking{Type: "disabled"}) || clientRequestsThinking(nil) {
		t.Error("disabled/absent thinking object must not count as a thinking request")
	}
}

func TestThinkingRequestsReasoning(t *testing.T) {
	if ThinkingRequestsReasoning("minimal") || ThinkingRequestsReasoning("low") || ThinkingRequestsReasoning("") {
		t.Error("minimal/low/unset must not count as thinking requested")
	}
	if !ThinkingRequestsReasoning("medium") || !ThinkingRequestsReasoning("max") {
		t.Error("medium+ must count as thinking requested")
	}
}

func TestThinkingLogValue(t *testing.T) {
	if got := ThinkingLogValue("", "anything"); got != "" {
		t.Errorf("unset request must log empty, got %q", got)
	}
	if got := ThinkingLogValue("high", "high"); got != "high" {
		t.Errorf("same applied collapses, got %q", got)
	}
	if got := ThinkingLogValue("max", "max"); got != "max" {
		t.Errorf("got %q", got)
	}
	if got := ThinkingLogValue("low", "disabled"); got != "low>disabled" {
		t.Errorf("got %q", got)
	}
}

func applyOpenAI(t *testing.T, baseURL, model, level string, extra map[string]interface{}) (map[string]interface{}, string) {
	t.Helper()
	body := map[string]interface{}{
		"model":            model,
		"reasoning_effort": "high",
		"thinking":         map[string]interface{}{"type": "enabled"},
	}
	for k, v := range extra {
		body[k] = v
	}
	applied := ApplyThinkingOpenAI(body, baseURL, model, level)
	return body, applied
}

func TestApplyThinkingOpenAI_DeepSeek(t *testing.T) {
	// Real DeepSeek target model names only: "deepseek-reasoner" is the
	// thinking-capable one. MuhiyaLLM's own virtual aliases (deepseek-v4-pro,
	// deepseek-v4-flash, ...) resolve to a concrete target_model before this
	// function ever sees them (see handler.go), so exercising the mapper
	// with an alias here would not reflect a real call.
	body, applied := applyOpenAI(t, "https://api.deepseek.com", "deepseek-reasoner", "max", nil)
	if applied != "max" {
		t.Fatalf("applied = %q", applied)
	}
	if body["reasoning_effort"] != "max" {
		t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
	}
	thinking := body["thinking"].(map[string]interface{})
	if thinking["type"] != "enabled" {
		t.Errorf("thinking = %v", thinking)
	}

	// Thinking is NEVER disabled: DeepSeek supports high|max only, so
	// low/medium ride the "high" floor and high/max ride "max".
	body, applied = applyOpenAI(t, "https://api.deepseek.com", "deepseek-reasoner", "low", nil)
	if applied != "high" || body["reasoning_effort"] != "high" {
		t.Fatalf("low -> %q / %v", applied, body["reasoning_effort"])
	}
	if body["thinking"].(map[string]interface{})["type"] != "enabled" {
		t.Errorf("thinking = %v", body["thinking"])
	}

	body, applied = applyOpenAI(t, "https://api.deepseek.com", "deepseek-reasoner", "medium", nil)
	if applied != "high" || body["reasoning_effort"] != "high" {
		t.Errorf("medium -> %q / %v", applied, body["reasoning_effort"])
	}
	body, applied = applyOpenAI(t, "https://api.deepseek.com", "deepseek-reasoner", "high", nil)
	if applied != "max" || body["reasoning_effort"] != "max" {
		t.Errorf("high -> %q / %v", applied, body["reasoning_effort"])
	}
}

// TestApplyThinkingOpenAI_DeepSeekChatUnsupported locks in the fix for a
// gateway audit finding: deepseek-chat has no thinking mode and used to get
// "thinking"/"reasoning_effort" injected anyway (a 400 risk, and request-body
// shape variance for a model that could never act on it). Only
// deepseek-reasoner should receive the parameter.
func TestApplyThinkingOpenAI_DeepSeekChatUnsupported(t *testing.T) {
	body, applied := applyOpenAI(t, "https://api.deepseek.com", "deepseek-chat", "max", nil)
	if applied != "unsupported" {
		t.Fatalf("deepseek-chat applied = %q, want unsupported", applied)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Errorf("deepseek-chat must not receive reasoning_effort, got %v", body["reasoning_effort"])
	}
}

func TestApplyThinkingOpenAI_GLM(t *testing.T) {
	body, applied := applyOpenAI(t, "https://open.bigmodel.cn/api/paas/v4", "glm-4.6", "high", nil)
	if applied != "enabled" {
		t.Fatalf("applied = %q", applied)
	}
	if _, hasEffort := body["reasoning_effort"]; hasEffort {
		t.Error("glm-4.x must not receive reasoning_effort")
	}
	// GLM-5 ladder is low|medium|high: max clamps to high, low rides low.
	body, applied = applyOpenAI(t, "https://open.bigmodel.cn/api/paas/v4", "glm-5.2", "max", nil)
	if applied != "high" || body["reasoning_effort"] != "high" {
		t.Errorf("glm-5 max -> %q / %v", applied, body["reasoning_effort"])
	}
	body, applied = applyOpenAI(t, "https://open.bigmodel.cn/api/paas/v4", "glm-5.2", "low", nil)
	if applied != "low" || body["reasoning_effort"] != "low" {
		t.Errorf("glm-5 low -> %q / %v", applied, body["reasoning_effort"])
	}
	// Never disabled, even at the lowest requested level.
	body, _ = applyOpenAI(t, "https://open.bigmodel.cn/api/paas/v4", "glm-4.5-air", "minimal", nil)
	if body["thinking"].(map[string]interface{})["type"] != "enabled" {
		t.Errorf("minimal must keep thinking enabled, got %v", body["thinking"])
	}
}

func TestApplyThinkingOpenAI_MiniMaxAlwaysOn(t *testing.T) {
	body, applied := applyOpenAI(t, "https://api.minimax.io/v1", "MiniMax-M3", "low", nil)
	if applied != "always-on" {
		t.Fatalf("applied = %q", applied)
	}
	if _, hasThinking := body["thinking"]; hasThinking {
		t.Error("minimax must not receive a thinking object")
	}
	if body["reasoning_split"] != true {
		t.Error("minimax should get reasoning_split")
	}
	body, applied = applyOpenAI(t, "https://api.minimax.io/v1", "MiniMax-M2.7", "", map[string]interface{}{"reasoning_split": false})
	if applied != "always-on" || body["reasoning_split"] != true {
		t.Fatalf("unset/raw MiniMax reasoning control was not normalized: applied=%q body=%v", applied, body)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Fatal("raw reasoning_effort leaked to MiniMax")
	}
}

func TestApplyThinkingOpenAI_OpenAIStrict(t *testing.T) {
	// Non-reasoning model: everything stripped, nothing injected (400 guard).
	body, applied := applyOpenAI(t, "https://api.openai.com/v1", "gpt-4o", "high", nil)
	if applied != "unsupported" {
		t.Fatalf("applied = %q", applied)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Error("gpt-4o must not receive reasoning_effort")
	}
	if _, has := body["thinking"]; has {
		t.Error("thinking must always be stripped for OpenAI upstreams")
	}
	// Reasoning model: clamped standard values.
	body, applied = applyOpenAI(t, "https://api.openai.com/v1", "gpt-5.1", "max", nil)
	if applied != "high" || body["reasoning_effort"] != "high" {
		t.Errorf("gpt-5.1 max -> %q / %v", applied, body["reasoning_effort"])
	}
	// o-series predates "minimal": clamp to low instead of a guaranteed 400.
	body, applied = applyOpenAI(t, "https://api.openai.com/v1", "o3-mini", "minimal", nil)
	if applied != "low" || body["reasoning_effort"] != "low" {
		t.Errorf("o3-mini minimal -> %q", applied)
	}
	_, applied = applyOpenAI(t, "https://api.openai.com/v1", "gpt-5.1", "minimal", nil)
	if applied != "minimal" {
		t.Errorf("gpt-5.1 minimal -> %q", applied)
	}
}

func TestApplyThinkingOpenAI_GrokQwenKimiGemini(t *testing.T) {
	// grok-4 errors on reasoning_effort: must strip only.
	body, applied := applyOpenAI(t, "https://api.x.ai/v1", "grok-4", "high", nil)
	if applied != "unsupported" {
		t.Fatalf("grok-4 applied = %q", applied)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Error("grok-4 must not receive reasoning_effort")
	}
	body, applied = applyOpenAI(t, "https://api.x.ai/v1", "grok-3-mini", "medium", nil)
	if applied != "low" || body["reasoning_effort"] != "low" {
		t.Errorf("grok-3-mini medium -> %q", applied)
	}
	body, applied = applyOpenAI(t, "https://api.x.ai/v1", "grok-4.5", "max", nil)
	if applied != "high" || body["reasoning_effort"] != "high" {
		t.Errorf("grok-4.5 max -> %q", applied)
	}

	// Qwen only has a toggle - never disabled, regardless of level.
	body, applied = applyOpenAI(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3-max", "low", nil)
	if applied != "enabled" || body["enable_thinking"] != true {
		t.Errorf("qwen low -> %q / %v", applied, body["enable_thinking"])
	}
	body, applied = applyOpenAI(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3-max", "high", nil)
	if applied != "enabled" || body["enable_thinking"] != true {
		t.Errorf("qwen high -> %q", applied)
	}

	body, applied = applyOpenAI(t, "https://api.moonshot.ai/v1", "kimi-k2.6", "medium", nil)
	if applied != "enabled" || body["thinking"].(map[string]interface{})["type"] != "enabled" {
		t.Errorf("kimi medium -> %q", applied)
	}
	body, applied = applyOpenAI(t, "https://api.moonshot.ai/v1", "kimi-k2.7-code", "high", nil)
	if applied != "always-on" {
		t.Errorf("kimi k2.7-code -> %q", applied)
	}
	if _, has := body["thinking"]; has {
		t.Error("k2.7-code must not receive the thinking param at all")
	}

	body, applied = applyOpenAI(t, "https://generativelanguage.googleapis.com/v1beta/openai", "gemini-3.1-pro", "minimal", nil)
	if applied != "low" || body["reasoning_effort"] != "low" {
		t.Errorf("gemini minimal -> %q (must never send none)", applied)
	}
}

func TestApplyThinkingOpenAI_UnknownStripsEverything(t *testing.T) {
	body, applied := applyOpenAI(t, "https://some-random-upstream.example/v1", "mystery-model", "high", nil)
	if applied != "unsupported" {
		t.Fatalf("applied = %q", applied)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Error("unknown upstream must not receive reasoning_effort")
	}
	if _, has := body["thinking"]; has {
		t.Error("unknown upstream must not receive thinking")
	}
}

func TestApplyThinkingOpenAI_UnsetLeavesBodyUntouched(t *testing.T) {
	// No level requested: a client speaking the provider's own dialect keeps
	// full control (pre-existing pass-through behavior) for every provider
	// EXCEPT DeepSeek, which is normalized unconditionally (see below).
	body, applied := applyOpenAI(t, "https://open.bigmodel.cn/api/paas/v4", "glm-4.6", "", nil)
	if applied != "" {
		t.Fatalf("applied = %q", applied)
	}
	if body["reasoning_effort"] != "high" {
		t.Error("client reasoning_effort must pass through when no level is requested")
	}
	if body["thinking"].(map[string]interface{})["type"] != "enabled" {
		t.Error("client thinking must pass through when no level is requested")
	}

	// DeepSeek is the documented exception: even with no level requested, the
	// raw client reasoning_effort/thinking are ALWAYS stripped and re-emitted in
	// DeepSeek's own form so an undocumented value can never reach it. A
	// non-reasoning target has no thinking mode, so nothing is injected.
	dsBody, dsApplied := applyOpenAI(t, "https://api.deepseek.com", "deepseek-chat", "", nil)
	if dsApplied != "unsupported" {
		t.Fatalf("deepseek unset applied = %q, want unsupported", dsApplied)
	}
	if _, has := dsBody["reasoning_effort"]; has {
		t.Error("raw client reasoning_effort must never survive to DeepSeek, even when no level is requested")
	}
	if _, has := dsBody["thinking"]; has {
		t.Error("raw client thinking must never survive to DeepSeek, even when no level is requested")
	}
}

func TestApplyThinkingAnthropic_Budget(t *testing.T) {
	body := map[string]interface{}{
		"model":       "claude-sonnet-4-5",
		"max_tokens":  float64(4096),
		"temperature": float64(0.2),
	}
	applied := ApplyThinkingAnthropic(body, "https://api.anthropic.com", "claude-sonnet-4-5", "max")
	if applied != "budget-24576" {
		t.Fatalf("applied = %q", applied)
	}
	thinking := body["thinking"].(map[string]interface{})
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != 24576 {
		t.Errorf("thinking = %v", thinking)
	}
	// max_tokens must exceed the budget.
	if body["max_tokens"].(int) <= 24576 {
		t.Errorf("max_tokens = %v, must exceed budget", body["max_tokens"])
	}
	// Extended thinking rejects non-default sampling params.
	if _, has := body["temperature"]; has {
		t.Error("temperature must be removed when thinking is injected")
	}

	// Models without extended thinking must not receive the parameter.
	old := map[string]interface{}{"model": "claude-3-5-sonnet-20241022", "temperature": float64(0.2)}
	if applied := ApplyThinkingAnthropic(old, "https://api.anthropic.com", "claude-3-5-sonnet-20241022", "high"); applied != "unsupported" {
		t.Fatalf("claude-3-5 applied = %q", applied)
	}
	if _, has := old["thinking"]; has {
		t.Error("claude-3-5 must not receive a thinking object")
	}
	if old["temperature"] != float64(0.2) {
		t.Error("unsupported models keep their sampling params untouched")
	}

	// Thinking is never disabled: low levels get the smallest real budget.
	body = map[string]interface{}{"model": "claude-sonnet-4-5", "thinking": map[string]interface{}{"type": "enabled", "budget_tokens": 1024}}
	applied = ApplyThinkingAnthropic(body, "https://api.anthropic.com", "claude-sonnet-4-5", "low")
	if applied != "budget-4096" {
		t.Fatalf("low applied = %q", applied)
	}
	if body["thinking"].(map[string]interface{})["budget_tokens"] != 4096 {
		t.Errorf("low budget = %v", body["thinking"])
	}
}

func TestApplyThinkingAnthropic_AdaptiveAndDeepseek(t *testing.T) {
	body := map[string]interface{}{"model": "claude-opus-4-8", "temperature": float64(0.7)}
	applied := ApplyThinkingAnthropic(body, "https://api.anthropic.com", "claude-opus-4-8", "max")
	if applied != "adaptive-max" {
		t.Fatalf("applied = %q", applied)
	}
	if body["thinking"].(map[string]interface{})["type"] != "adaptive" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if body["output_config"].(map[string]interface{})["effort"] != "max" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	if _, has := body["temperature"]; has {
		t.Error("adaptive thinking must drop temperature")
	}

	// Adaptive models take the full ladder: medium maps to medium.
	body = map[string]interface{}{"model": "claude-sonnet-5"}
	applied = ApplyThinkingAnthropic(body, "https://api.anthropic.com", "claude-sonnet-5", "medium")
	if applied != "adaptive-medium" {
		t.Fatalf("adaptive medium applied = %q", applied)
	}

	// DeepSeek-Anthropic supports high|max only: high effort rides "max",
	// low rides the "high" floor - never disabled. Real target model name
	// only ("deepseek-reasoner"); see TestApplyThinkingOpenAI_DeepSeek for
	// why an alias like deepseek-v4-pro would not reflect a real call.
	body = map[string]interface{}{"model": "deepseek-reasoner"}
	applied = ApplyThinkingAnthropic(body, "https://api.deepseek.com/anthropic", "deepseek-reasoner", "high")
	if applied != "max" {
		t.Fatalf("deepseek anthropic high applied = %q", applied)
	}
	if body["output_config"].(map[string]interface{})["effort"] != "max" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	body = map[string]interface{}{"model": "deepseek-reasoner"}
	applied = ApplyThinkingAnthropic(body, "https://api.deepseek.com/anthropic", "deepseek-reasoner", "low")
	if applied != "high" || body["thinking"].(map[string]interface{})["type"] != "enabled" {
		t.Fatalf("deepseek anthropic low applied = %q / %v", applied, body["thinking"])
	}

	// deepseek-chat has no reasoning mode: must fall through to unsupported,
	// not have output_config/thinking injected (audit fix, mirrors the
	// OpenAI-path test).
	body = map[string]interface{}{"model": "deepseek-chat"}
	applied = ApplyThinkingAnthropic(body, "https://api.deepseek.com/anthropic", "deepseek-chat", "high")
	if applied != "unsupported" {
		t.Fatalf("deepseek-chat anthropic applied = %q, want unsupported", applied)
	}
	if _, has := body["output_config"]; has {
		t.Errorf("deepseek-chat must not receive output_config, got %v", body["output_config"])
	}

	// Unset level: client thinking object passes through untouched.
	passthrough := map[string]interface{}{"thinking": map[string]interface{}{"type": "enabled", "budget_tokens": float64(2048)}}
	if applied := ApplyThinkingAnthropic(passthrough, "https://api.anthropic.com", "claude-sonnet-4-5", ""); applied != "" {
		t.Fatalf("unset applied = %q", applied)
	}
	if passthrough["thinking"].(map[string]interface{})["budget_tokens"] != float64(2048) {
		t.Error("client thinking must pass through when no level requested")
	}
}

func TestClassifyUpstreamModelWinsOverHost(t *testing.T) {
	// Aggregator host, DeepSeek model: model prefix must win.
	if classifyUpstream("https://aggregator.example/v1", "deepseek-v4-pro") != famDeepseek {
		t.Error("model prefix should classify deepseek")
	}
	if classifyUpstream("https://api.deepseek.com", "some-custom-model") != famDeepseek {
		t.Error("host should classify deepseek when model is unknown")
	}
	if classifyUpstream("https://unknown.example", "unknown-model") != famUnknown {
		t.Error("unknown/unknown should be famUnknown")
	}
}
