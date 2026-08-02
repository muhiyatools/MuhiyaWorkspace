package proxy

import (
	"encoding/json"
	"testing"

	"gateway/db"
)

func deepseekV4Model() *db.Model {
	return &db.Model{
		Name: "deepseek-v4-flash", TargetModel: "deepseek-v4-flash",
		ContextWindow: 1_000_000, MaxOutputTokens: 384_000, SupportsThinking: true,
	}
}

// The gateway used to inject max_tokens into EVERY forwarded body, whether or
// not the caller asked for one. On a thinking model that is not a harmless upper
// bound: reasoning tokens are counted inside completion_tokens, so a ceiling the
// caller never chose is spent on reasoning_content before any visible token is
// produced, and the turn returns finish_reason=length with empty content.
//
// requestedOpenAIOutput must therefore report whether the CLIENT named a limit,
// separately from the figure used for admission.
func TestRequestedOutputDistinguishesClientIntentFromAdmissionFigure(t *testing.T) {
	model := deepseekV4Model()

	limit, clientSpecified := requestedOpenAIOutput(&OpenAIRequest{}, model)
	if clientSpecified {
		t.Error("a request with no max_tokens must not report a client-specified limit")
	}
	if limit != 384_000 {
		t.Errorf("admission limit = %d, want the model ceiling 384000", limit)
	}

	asked := 4096
	limit, clientSpecified = requestedOpenAIOutput(&OpenAIRequest{MaxTokens: &asked}, model)
	if !clientSpecified || limit != 4096 {
		t.Errorf("client limit = %d/%v, want 4096/true", limit, clientSpecified)
	}

	// max_completion_tokens is the same intent under the newer field name.
	limit, clientSpecified = requestedOpenAIOutput(&OpenAIRequest{MaxCompletionTokens: &asked}, model)
	if !clientSpecified || limit != 4096 {
		t.Errorf("max_completion_tokens limit = %d/%v, want 4096/true", limit, clientSpecified)
	}

	// A client asking above the model ceiling is bounded, but still counts as
	// having stated an intent.
	over := 999_999
	limit, clientSpecified = requestedOpenAIOutput(&OpenAIRequest{MaxTokens: &over}, model)
	if !clientSpecified || limit != 384_000 {
		t.Errorf("over-ceiling limit = %d/%v, want 384000/true", limit, clientSpecified)
	}
}

// Reserving the model's full documented ceiling against a token-per-window rate
// limit exhausted any realistic allowance on the first call: one DeepSeek V4
// request debited 384,000 tokens regardless of what it actually generated.
func TestRateLimitEstimateDoesNotReserveTheWholeCeiling(t *testing.T) {
	if got := rateLimitOutputEstimate(384_000, false); got != typicalCompletionTokens {
		t.Errorf("unbounded request reserved %d, want %d", got, typicalCompletionTokens)
	}
	// A client that named its own ceiling is taken at its word.
	if got := rateLimitOutputEstimate(100_000, true); got != 100_000 {
		t.Errorf("client-specified reservation = %d, want 100000", got)
	}
	// A small ceiling is never inflated up to the typical figure.
	if got := rateLimitOutputEstimate(512, false); got != 512 {
		t.Errorf("small ceiling reserved %d, want 512", got)
	}
}

// The reasoning-headroom guard keys off the operator flag and the resolved
// effort, not the model name.
func TestThinkingWillBeEnabledFollowsTheOperatorFlag(t *testing.T) {
	model := deepseekV4Model()
	if !thinkingWillBeEnabled(model, ThinkingHigh) {
		t.Error("a flagged model at high effort runs in thinking mode")
	}
	// No effort resolved: DeepSeek V4 enables thinking by default, so the
	// request still runs in thinking mode.
	if !thinkingWillBeEnabled(model, "") {
		t.Error("a flagged model with no stated effort still defaults to thinking")
	}
	// An explicit none/off is the one true disable.
	if thinkingWillBeEnabled(model, ThinkingMinimal) {
		t.Error("an explicit off must not count as thinking mode")
	}
	unflagged := deepseekV4Model()
	unflagged.SupportsThinking = false
	if thinkingWillBeEnabled(unflagged, ThinkingMax) {
		t.Error("an unflagged model has no thinking mode regardless of effort")
	}
	if thinkingWillBeEnabled(nil, ThinkingMax) {
		t.Error("a nil model must not be treated as thinking-capable")
	}
}

// rewriteOpenAIOutputLimit itself is unchanged; what changed is that it is now
// called conditionally. This pins its two shapes so a future caller cannot
// assume it leaves an absent key absent.
func TestRewriteOutputLimitAlwaysWritesTheKeyItIsGiven(t *testing.T) {
	raw := []byte(`{"model":"deepseek-v4-flash","messages":[]}`)
	out, err := rewriteOpenAIOutputLimit(raw, 1234)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["max_tokens"] != float64(1234) {
		t.Errorf("max_tokens = %v, want 1234", payload["max_tokens"])
	}

	// A body using the newer field keeps using it, and does not end up with both.
	raw = []byte(`{"model":"m","max_completion_tokens":10}`)
	out, err = rewriteOpenAIOutputLimit(raw, 99)
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["max_completion_tokens"] != float64(99) {
		t.Errorf("max_completion_tokens = %v, want 99", payload["max_completion_tokens"])
	}
	if _, has := payload["max_tokens"]; has {
		t.Error("both output-limit fields present; upstreams reject that")
	}
}
