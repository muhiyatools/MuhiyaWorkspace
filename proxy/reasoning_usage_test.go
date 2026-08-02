package proxy

import (
	"encoding/json"
	"testing"
)

// Reasoning tokens are reported as a SUBSET of completion_tokens, never an
// addition to them. Treating them as additive would double-count output on
// every thinking request and inflate both the token figures and the cost.
func TestReasoningTokensAreASubsetOfCompletionTokens(t *testing.T) {
	var usage OpenAIUsage
	// The exact shape DeepSeek and the OpenAI reasoning models send.
	raw := `{"prompt_tokens":7295,"completion_tokens":27255,"total_tokens":34550,
	         "completion_tokens_details":{"reasoning_tokens":19840}}`
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := usage.ReasoningTokensReported(); got != 19840 {
		t.Fatalf("reasoning tokens = %d, want 19840", got)
	}
	if usage.CompletionTokens != 27255 {
		t.Fatalf("completion tokens must stay as reported, got %d", usage.CompletionTokens)
	}
	// The visible share is what the user actually waited to read.
	if visible := usage.CompletionTokens - usage.ReasoningTokensReported(); visible != 7415 {
		t.Fatalf("visible tokens = %d, want 7415", visible)
	}
}

// A non-thinking model reports no split. That must read as zero, not as an
// error and not as "unknown", because it costs exactly the same as a genuine
// zero and no caller should have to special-case it.
func TestReasoningTokensAbsentReadsAsZero(t *testing.T) {
	var usage OpenAIUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":50}`), &usage); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := usage.ReasoningTokensReported(); got != 0 {
		t.Fatalf("absent details must read 0, got %d", got)
	}
	var nilUsage *OpenAIUsage
	if got := nilUsage.ReasoningTokensReported(); got != 0 {
		t.Fatalf("nil usage must read 0, got %d", got)
	}
}

// An upstream that reports more reasoning than completion is contradicting its
// own contract. Clamp rather than propagate, so a provider bug cannot produce a
// negative visible-token count downstream.
func TestReasoningTokensClampToCompletion(t *testing.T) {
	usage := OpenAIUsage{
		CompletionTokens:        100,
		CompletionTokensDetails: &CompletionTokensDetail{ReasoningTokens: 250},
	}
	if got := usage.ReasoningTokensReported(); got != 100 {
		t.Fatalf("reasoning must clamp to completion_tokens, got %d", got)
	}
	negative := OpenAIUsage{
		CompletionTokens:        100,
		CompletionTokensDetails: &CompletionTokensDetail{ReasoningTokens: -5},
	}
	if got := negative.ReasoningTokensReported(); got != 0 {
		t.Fatalf("negative reasoning must floor at 0, got %d", got)
	}
}
