package proxy

import (
	"fmt"
	"net/http"
	"strings"
)

// ------------------------------------------------------------------
// Thinking-level mapping
//
// Clients (MuhiyaCode, MuhiyaChat, third parties) express desired reasoning
// depth ONCE, in gateway terms: an `X-Muhiya-Effort` header or a standard
// `reasoning_effort` body field, normalized to the canonical levels below.
// The gateway then speaks each provider's native dialect:
//
//	DeepSeek  : thinking {type} + reasoning_effort high|max
//	GLM       : thinking {type} (+ reasoning_effort on GLM-5+)
//	MiniMax   : always-on; only reasoning_split is useful
//	OpenAI    : reasoning_effort minimal|low|medium|high (reasoning models ONLY - others 400)
//	Qwen      : enable_thinking bool
//	Kimi      : thinking {type} (k2.7-code: always-on, never send the param)
//	Grok      : reasoning_effort low|high (3-mini) / low|medium|high (4.5+); grok-4 ERRORS on it
//	Gemini    : reasoning_effort low|medium|high (never "none")
//	Anthropic : thinking {enabled,budget_tokens} or {adaptive} + output_config effort
//	Unknown   : strip everything - a silent no-op beats an upstream 400
//
// The client-facing fields are ALWAYS stripped from the forwarded body first:
// what reaches a provider must be exactly its own dialect, never ours.
// ------------------------------------------------------------------

// Canonical thinking levels, ordered.
const (
	ThinkingMinimal = "minimal"
	ThinkingLow     = "low"
	ThinkingMedium  = "medium"
	ThinkingHigh    = "high"
	ThinkingMax     = "max"
)

// EffortHeader is the full-fidelity effort channel used by Muhiya clients.
const EffortHeader = "X-Muhiya-Effort"

// NormalizeThinkingLevel maps arbitrary client-supplied values onto canonical
// levels. Unknown values normalize to "" (treated as unset) so attacker- or
// typo-supplied garbage never reaches upstream bodies or the request log.
func NormalizeThinkingLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "off", "min", "minimal":
		return ThinkingMinimal
	case "low":
		return ThinkingLow
	case "medium", "mid":
		return ThinkingMedium
	case "high", "ultra":
		return ThinkingHigh
	case "max", "xhigh":
		return ThinkingMax
	default:
		return ""
	}
}

func thinkingRank(level string) int {
	switch level {
	case ThinkingMinimal:
		return 0
	case ThinkingLow:
		return 1
	case ThinkingMedium:
		return 2
	case ThinkingHigh:
		return 3
	case ThinkingMax:
		return 4
	default:
		return -1
	}
}

// ResolveThinkingLevel decides the canonical level for a request.
// Precedence: X-Muhiya-Effort header > body reasoning_effort.
// The header wins because it is the gateway's own full-fidelity channel:
// Muhiya clients also send a CLAMPED standard reasoning_effort in the body
// for plain OpenAI-compatible endpoints (e.g. header "max" + body "high") -
// body-first precedence would silently downgrade those requests.
//
// An explicit Anthropic-style `thinking` object deliberately does NOT set a
// level: it is the client's own precise provider control (type + budget) and
// must pass through untouched instead of being rewritten by our mapping.
// Returns "" when the client expressed no preference.
func ResolveThinkingLevel(r *http.Request, bodyEffort *string) string {
	if r != nil {
		if value := r.Header.Get(EffortHeader); value != "" {
			if level := NormalizeThinkingLevel(value); level != "" {
				return level
			}
		}
	}
	if bodyEffort != nil {
		if level := NormalizeThinkingLevel(*bodyEffort); level != "" {
			return level
		}
	}
	return ""
}

// clientRequestsThinking reports whether an explicit client thinking object
// asks for reasoning - used only as a router signal, never for mapping.
func clientRequestsThinking(thinking *AnthropicThinking) bool {
	return thinking != nil && !strings.EqualFold(thinking.Type, "disabled")
}

// ThinkingRequestsReasoning reports whether a level should count as "the
// client wants a thinking model" for the AI router. minimal/low explicitly do
// NOT: a request to think less must never route onto a pricier thinking tier.
func ThinkingRequestsReasoning(level string) bool {
	return thinkingRank(level) >= 2
}

// ThinkingLogValue is what lands in request_logs.thinking_level: the
// requested canonical level plus what was actually applied for the concrete
// provider/model, e.g. "high>max", "low>disabled", "medium>unsupported".
func ThinkingLogValue(requested, applied string) string {
	if requested == "" {
		return ""
	}
	if applied == "" || applied == requested {
		return requested
	}
	return fmt.Sprintf("%s>%s", requested, applied)
}

type upstreamFamily int

const (
	famUnknown upstreamFamily = iota
	famDeepseek
	famGLM
	famMiniMax
	famOpenAI
	famQwen
	famKimi
	famGrok
	famGemini
	famAnthropic
)

// classifyUpstream infers the provider dialect from the upstream base URL and
// the real target model. Model prefixes win over hostnames so aggregators
// that host many families (one base URL, many models) still map correctly.
func classifyUpstream(baseURL, targetModel string) upstreamFamily {
	host := strings.ToLower(baseURL)
	model := strings.ToLower(targetModel)

	switch {
	case strings.HasPrefix(model, "deepseek"):
		return famDeepseek
	case strings.HasPrefix(model, "glm"):
		return famGLM
	case strings.HasPrefix(model, "minimax") || strings.HasPrefix(model, "abab"):
		return famMiniMax
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") || strings.HasPrefix(model, "codex"):
		return famOpenAI
	case strings.HasPrefix(model, "qwen") || strings.HasPrefix(model, "qwq"):
		return famQwen
	case strings.HasPrefix(model, "kimi") || strings.HasPrefix(model, "moonshot"):
		return famKimi
	case strings.HasPrefix(model, "grok"):
		return famGrok
	case strings.HasPrefix(model, "gemini"):
		return famGemini
	case strings.HasPrefix(model, "claude"):
		return famAnthropic
	}

	switch {
	case strings.Contains(host, "deepseek"):
		return famDeepseek
	case strings.Contains(host, "bigmodel") || strings.Contains(host, "z.ai"):
		return famGLM
	case strings.Contains(host, "minimax"):
		return famMiniMax
	case strings.Contains(host, "api.openai.com"):
		return famOpenAI
	case strings.Contains(host, "dashscope") || strings.Contains(host, "aliyuncs"):
		return famQwen
	case strings.Contains(host, "moonshot") || strings.Contains(host, "kimi"):
		return famKimi
	case strings.Contains(host, "x.ai"):
		return famGrok
	case strings.Contains(host, "googleapis") || strings.Contains(host, "generativelanguage"):
		return famGemini
	case strings.Contains(host, "anthropic"):
		return famAnthropic
	default:
		return famUnknown
	}
}

// ApplyThinkingOpenAI rewrites an OpenAI-format wire body for the target
// provider. When a level is requested it strips the gateway-level fields and
// injects the provider's native parameter. When NO level is requested the
// body passes through untouched (pre-existing behavior for clients that speak
// a provider's dialect directly). The return value is the setting that was
// actually applied ("" when nothing was requested; "unsupported" when the
// model has no controllable thinking).
func ApplyThinkingOpenAI(bodyMap map[string]interface{}, baseURL, targetModel, level string) string {
	if level == "" {
		return ""
	}
	// Gateway-level control fields never reach a provider verbatim.
	delete(bodyMap, "reasoning_effort")
	delete(bodyMap, "thinking")

	family := classifyUpstream(baseURL, targetModel)
	model := strings.ToLower(targetModel)
	rank := thinkingRank(level)

	switch family {
	case famDeepseek:
		// V3.2+/V4: thinking toggle plus effort high|max (lower values are
		// remapped upstream anyway, so only send what is meaningful).
		if rank <= 1 {
			bodyMap["thinking"] = map[string]interface{}{"type": "disabled"}
			return "disabled"
		}
		bodyMap["thinking"] = map[string]interface{}{"type": "enabled"}
		if level == ThinkingMax {
			bodyMap["reasoning_effort"] = "max"
			return "max"
		}
		bodyMap["reasoning_effort"] = "high"
		return "high"

	case famGLM:
		// GLM-4.5+ honors the thinking toggle; GLM-5+ adds effort levels.
		if rank <= 1 {
			bodyMap["thinking"] = map[string]interface{}{"type": "disabled"}
			return "disabled"
		}
		bodyMap["thinking"] = map[string]interface{}{"type": "enabled"}
		if strings.HasPrefix(model, "glm-5") {
			bodyMap["reasoning_effort"] = level
			return level
		}
		return "enabled"

	case famMiniMax:
		// M2+ thinking is always on and cannot be disabled. The only useful
		// request is a separated reasoning channel instead of <think> tags.
		bodyMap["reasoning_split"] = true
		return "always-on"

	case famOpenAI:
		// Strict provider: non-reasoning models 400 on reasoning_effort.
		if !openAISupportsReasoningEffort(model) {
			return "unsupported"
		}
		applied := level
		if level == ThinkingMax {
			applied = "high" // xhigh is model-gated; high is universally safe
		}
		// o-series models predate "minimal" (gpt-5+) and 400 on it.
		if applied == ThinkingMinimal && !strings.HasPrefix(model, "gpt-5") && !strings.HasPrefix(model, "gpt-6") && !strings.HasPrefix(model, "codex") {
			applied = "low"
		}
		bodyMap["reasoning_effort"] = applied
		return applied

	case famQwen:
		if rank <= 1 {
			bodyMap["enable_thinking"] = false
			return "disabled"
		}
		bodyMap["enable_thinking"] = true
		return "enabled"

	case famKimi:
		if strings.Contains(model, "k2.7-code") {
			// Always-on thinking; docs say do not pass the parameter at all.
			return "always-on"
		}
		if rank <= 1 {
			bodyMap["thinking"] = map[string]interface{}{"type": "disabled"}
			return "disabled"
		}
		bodyMap["thinking"] = map[string]interface{}{"type": "enabled"}
		return "enabled"

	case famGrok:
		// grok-4 family ERRORS on reasoning_effort - strip only.
		if strings.HasPrefix(model, "grok-3-mini") {
			if rank >= 3 {
				bodyMap["reasoning_effort"] = "high"
				return "high"
			}
			bodyMap["reasoning_effort"] = "low"
			return "low"
		}
		if strings.HasPrefix(model, "grok-4.5") || strings.HasPrefix(model, "grok-5") {
			applied := "medium"
			if rank <= 1 {
				applied = "low"
			} else if rank >= 3 {
				applied = "high"
			}
			bodyMap["reasoning_effort"] = applied
			return applied
		}
		return "unsupported"

	case famGemini:
		// "none" 400s on models that cannot disable thinking; low is the floor.
		applied := "medium"
		if rank <= 1 {
			applied = "low"
		} else if rank >= 3 {
			applied = "high"
		}
		bodyMap["reasoning_effort"] = applied
		return applied

	case famAnthropic:
		// Anthropic models behind an OpenAI-format URL (rare aggregator case):
		// no portable parameter; strip rather than risk a 400.
		return "unsupported"

	default:
		return "unsupported"
	}
}

// openAISupportsReasoningEffort gates the strict OpenAI parameter to model
// families documented to accept it.
func openAISupportsReasoningEffort(model string) bool {
	return strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4") ||
		strings.HasPrefix(model, "gpt-5") ||
		strings.HasPrefix(model, "gpt-6") ||
		strings.HasPrefix(model, "codex")
}

// ApplyThinkingAnthropic rewrites an Anthropic-format wire body for the
// target provider. Same contract as ApplyThinkingOpenAI. When level is ""
// an explicit client thinking object passes through untouched (back-compat
// for native Anthropic clients).
func ApplyThinkingAnthropic(bodyMap map[string]interface{}, baseURL, targetModel, level string) string {
	// Never an Anthropic wire field; it round-trips via our request struct.
	delete(bodyMap, "reasoning_effort")
	if level == "" {
		return ""
	}

	model := strings.ToLower(targetModel)
	rank := thinkingRank(level)

	if classifyUpstream(baseURL, targetModel) == famDeepseek {
		// DeepSeek's Anthropic-compatible endpoint uses output_config.effort.
		if rank <= 1 {
			bodyMap["thinking"] = map[string]interface{}{"type": "disabled"}
			delete(bodyMap, "output_config")
			return "disabled"
		}
		effort := "high"
		if level == ThinkingMax {
			effort = "max"
		}
		bodyMap["thinking"] = map[string]interface{}{"type": "enabled"}
		bodyMap["output_config"] = map[string]interface{}{"effort": effort}
		return effort
	}

	// Anthropic proper (and compatible endpoints for claude models).
	if rank <= 1 {
		// Provider default is off for budget-style models; explicitly sending
		// {"type":"disabled"} is also rejected by some versions - omit instead.
		delete(bodyMap, "thinking")
		delete(bodyMap, "output_config")
		return "default"
	}

	// Models without extended thinking (3.5 and older) 400 on the parameter.
	if !anthropicSupportsThinking(model) {
		return "unsupported"
	}

	if anthropicAdaptiveOnly(model) {
		effort := "high"
		if level == ThinkingMax {
			effort = "max"
		}
		bodyMap["thinking"] = map[string]interface{}{"type": "adaptive"}
		bodyMap["output_config"] = map[string]interface{}{"effort": effort}
		// Adaptive-thinking models reject non-default sampling params.
		delete(bodyMap, "temperature")
		delete(bodyMap, "top_p")
		delete(bodyMap, "top_k")
		return "adaptive-" + effort
	}

	budget := 8192
	if rank >= 4 {
		budget = 24576
	} else if rank >= 3 {
		budget = 16384
	}
	bodyMap["thinking"] = map[string]interface{}{"type": "enabled", "budget_tokens": budget}
	// Extended thinking requires max_tokens > budget_tokens and default
	// sampling (temperature/top_p/top_k with thinking are a hard 400).
	delete(bodyMap, "temperature")
	delete(bodyMap, "top_p")
	delete(bodyMap, "top_k")
	maxTokens := 0
	if raw, ok := bodyMap["max_tokens"].(float64); ok {
		maxTokens = int(raw)
	}
	if maxTokens <= budget {
		bodyMap["max_tokens"] = budget + 4096
	}
	return fmt.Sprintf("budget-%d", budget)
}

// anthropicSupportsThinking marks Claude generations with extended thinking
// (3.7 and every 4/5-generation model); older models reject the parameter.
func anthropicSupportsThinking(model string) bool {
	if strings.Contains(model, "claude-3-7") {
		return true
	}
	for _, marker := range []string{"sonnet-4", "opus-4", "haiku-4", "sonnet-5", "opus-5", "haiku-5", "claude-4", "claude-5"} {
		if strings.Contains(model, marker) {
			return true
		}
	}
	return false
}

// anthropicAdaptiveOnly marks Claude generations where manual budget thinking
// is deprecated or rejected and adaptive + output_config.effort is the API.
func anthropicAdaptiveOnly(model string) bool {
	return strings.Contains(model, "-4-6") ||
		strings.Contains(model, "-4-7") ||
		strings.Contains(model, "-4-8") ||
		strings.Contains(model, "sonnet-5") ||
		strings.Contains(model, "opus-5") ||
		strings.Contains(model, "haiku-5")
}
