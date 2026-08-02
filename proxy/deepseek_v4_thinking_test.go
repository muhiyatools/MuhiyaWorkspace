package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Reasoner detection used to be a name substring: contains("reasoner") ||
// contains("r1"). deepseek-v4-flash matches neither, and neither does the
// deepseek-chat target it maps to, so its thinking configuration was stripped
// on every request — the model ran with whatever default the upstream picked,
// regardless of the effort the user selected.
//
// The operator flag (models.supports_thinking) is now the source of truth, the
// same way supports_vision is for vision routing. A model works by its own
// catalog row, not by whether someone named it well.

const deepseekBase = "https://api.deepseek.com"

func TestDeepSeekV4FlashHonoursTheOperatorFlag(t *testing.T) {
	for _, target := range []string{"deepseek-v4-flash", "deepseek-chat"} {
		body := map[string]interface{}{"model": target}
		applied := ApplyThinkingOpenAI(body, deepseekBase, target, "high", true)
		if applied != "max" && applied != "high" {
			t.Fatalf("%s with supports_thinking=true: applied %q, want thinking enabled", target, applied)
		}
		thinking, ok := body["thinking"].(map[string]interface{})
		if !ok || thinking["type"] != "enabled" {
			t.Fatalf("%s: thinking block = %v, want type=enabled", target, body["thinking"])
		}
		if _, ok := body["reasoning_effort"]; !ok {
			t.Fatalf("%s: reasoning_effort was not emitted", target)
		}
	}
}

// Without the flag, a name that does not look like a reasoner stays
// unconfigured — the fallback heuristic is unchanged.
func TestNonReasonerWithoutFlagStaysUnconfigured(t *testing.T) {
	body := map[string]interface{}{"model": "deepseek-chat", "reasoning_effort": "high"}
	if applied := ApplyThinkingOpenAI(body, deepseekBase, "deepseek-chat", "high", false); applied != "unsupported" {
		t.Fatalf("applied = %q, want unsupported", applied)
	}
	if _, present := body["thinking"]; present {
		t.Error("a non-reasoning model received a thinking block")
	}
	// The raw client field must still be stripped so nothing leaks upstream.
	if _, present := body["reasoning_effort"]; present {
		t.Error("the client's raw reasoning_effort leaked to a model that rejects it")
	}
}

// The name heuristic still covers callers with no catalog row.
func TestNameHeuristicRemainsTheFallback(t *testing.T) {
	for _, target := range []string{"deepseek-reasoner", "deepseek-r1"} {
		body := map[string]interface{}{"model": target}
		if applied := ApplyThinkingOpenAI(body, deepseekBase, target, "high", false); applied == "unsupported" {
			t.Errorf("%s was treated as non-reasoning without a catalog flag", target)
		}
	}
}

// An explicit off must still disable reasoning, flag or not.
func TestExplicitOffDisablesThinkingEvenWithTheFlag(t *testing.T) {
	body := map[string]interface{}{"model": "deepseek-v4-flash"}
	if applied := ApplyThinkingOpenAI(body, deepseekBase, "deepseek-v4-flash", "minimal", true); applied != "disabled" {
		t.Fatalf("applied = %q, want disabled", applied)
	}
	thinking, _ := body["thinking"].(map[string]interface{})
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %v, want type=disabled", body["thinking"])
	}
}

// MuhiyaChat's thinking pill is the only control a chat user has over how long
// a turn spends before it starts answering, so the header values it sends are a
// contract this file has to keep.
//
// The failure that motivated it: the chat client's "Auto" mode sent NO effort
// header, and a level-less body is forwarded untouched — which on DeepSeek means
// its own default, reasoning ON at high effort. A turn then spent its entire
// wall-clock budget inside reasoning_content, was cut off before it emitted a
// single visible token, and the user was told "no response received". Every rung
// the client can select must therefore resolve to a real, asserted wire setting.
func TestMuhiyaChatEffortHeaderContract(t *testing.T) {
	for _, testCase := range []struct {
		header       string
		wantApplied  string
		wantThinking string
		wantEffort   string // "" when no reasoning_effort key is expected
	}{
		{header: "off", wantApplied: "disabled", wantThinking: "disabled"},
		{header: "low", wantApplied: "low", wantThinking: "enabled", wantEffort: "low"},
		{header: "high", wantApplied: "high", wantThinking: "enabled", wantEffort: "high"},
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Header.Set(EffortHeader, testCase.header)

		level := ResolveThinkingLevel(request, nil)
		if level == "" {
			t.Fatalf("%s: %s resolved to no level, so the body would reach DeepSeek untouched at its own default",
				EffortHeader, testCase.header)
		}

		body := map[string]interface{}{"model": "deepseek-v4-flash"}
		applied := ApplyThinkingOpenAI(body, deepseekBase, "deepseek-v4-flash", level, true)
		if applied != testCase.wantApplied {
			t.Errorf("%s=%s: applied %q, want %q", EffortHeader, testCase.header, applied, testCase.wantApplied)
		}

		thinking, _ := body["thinking"].(map[string]interface{})
		if thinking["type"] != testCase.wantThinking {
			t.Errorf("%s=%s: thinking = %v, want type=%s", EffortHeader, testCase.header, body["thinking"], testCase.wantThinking)
		}

		effort, present := body["reasoning_effort"]
		switch {
		case testCase.wantEffort == "" && present:
			t.Errorf("%s=%s: reasoning_effort=%v sent alongside disabled thinking", EffortHeader, testCase.header, effort)
		case testCase.wantEffort != "" && effort != testCase.wantEffort:
			t.Errorf("%s=%s: reasoning_effort = %v, want %q", EffortHeader, testCase.header, effort, testCase.wantEffort)
		}
	}
}

// DeepSeek rejects these; they must never reach it regardless of the flag.
func TestDeepSeekPenaltiesStrippedRegardlessOfFlag(t *testing.T) {
	for _, flag := range []bool{true, false} {
		body := map[string]interface{}{
			"model": "deepseek-v4-flash", "frequency_penalty": 0.5, "presence_penalty": 0.5,
		}
		sanitizeUpstreamIdentity(body, classifyUpstream(deepseekBase, "deepseek-v4-flash"), "", "")
		ApplyThinkingOpenAI(body, deepseekBase, "deepseek-v4-flash", "high", flag)
		for _, banned := range []string{"frequency_penalty", "presence_penalty"} {
			if _, present := body[banned]; present {
				t.Errorf("flag=%v: %s reached DeepSeek, which rejects it", flag, banned)
			}
		}
	}
}
