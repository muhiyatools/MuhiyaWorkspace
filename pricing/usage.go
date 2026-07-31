package pricing

// Prompt-token accounting differs by provider, and getting it wrong is a
// silent money bug rather than a visible failure.
//
//   - OpenAI, DeepSeek and the OpenRouter OpenAI dialect report prompt_tokens
//     INCLUDING tokens served from or written to cache.
//   - Anthropic reports usage.input_tokens EXCLUDING both
//     cache_read_input_tokens and cache_creation_input_tokens.
//
// Pricing that assumes one convention will, for the other, subtract tokens
// that were never in the total and bill genuinely fresh input at zero. Every
// upstream number is therefore normalized into the canonical Usage below
// before any rate is applied, so the pricing engine never sees a dialect.
type PromptAccounting string

const (
	PromptInclusive PromptAccounting = "inclusive"
	PromptExclusive PromptAccounting = "exclusive"
)

// ParsePromptAccounting maps a stored value onto a mode. Unknown and empty
// values fall back to inclusive, which is the convention of every provider
// the gateway speaks to except Anthropic.
func ParsePromptAccounting(raw string) PromptAccounting {
	if PromptAccounting(raw) == PromptExclusive {
		return PromptExclusive
	}
	return PromptInclusive
}

func (a PromptAccounting) Valid() bool {
	return a == PromptInclusive || a == PromptExclusive
}

// TokenClass identifies one independently priced component of a request.
type TokenClass string

const (
	ClassInputFresh   TokenClass = "input_fresh"
	ClassOutput       TokenClass = "output"
	ClassCacheRead    TokenClass = "cache_read"
	ClassCacheRead5m  TokenClass = "cache_read_5m"
	ClassCacheWrite   TokenClass = "cache_write"
	ClassCacheWrite5m TokenClass = "cache_write_5m"
	ClassCacheWrite1h TokenClass = "cache_write_1h"
)

// TokenClasses is the canonical ordering used for receipts so that a stored
// breakdown is stable and diffable.
var TokenClasses = []TokenClass{
	ClassInputFresh, ClassOutput,
	ClassCacheRead, ClassCacheRead5m,
	ClassCacheWrite, ClassCacheWrite5m, ClassCacheWrite1h,
}

// Anomaly records that an upstream's own usage numbers were internally
// inconsistent. Pricing still proceeds on clamped values, but the request is
// marked so the inconsistency is visible instead of silently absorbed.
type Anomaly string

const (
	AnomalyNone Anomaly = ""
	// AnomalyPromptBelowCache means an inclusive-accounting provider reported
	// fewer prompt tokens than it simultaneously claimed to have served from
	// or written to cache.
	AnomalyPromptBelowCache Anomaly = "prompt_tokens_below_cache_tokens"
	// AnomalyNegativeTokens means a provider reported a negative token count.
	AnomalyNegativeTokens Anomaly = "negative_token_count"
)

// ReportedUsage is an upstream's usage payload in its own convention, before
// normalization. PromptTokens carries whichever number that provider calls the
// prompt total; its meaning is resolved by the PromptAccounting mode.
type ReportedUsage struct {
	PromptTokens       int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheRead5mTokens  int64
	CacheWriteTokens   int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
}

func (r ReportedUsage) cacheReadTotal() int64 {
	return r.CacheReadTokens + r.CacheRead5mTokens
}

func (r ReportedUsage) cacheWriteTotal() int64 {
	return r.CacheWriteTokens + r.CacheWrite5mTokens + r.CacheWrite1hTokens
}

func (r ReportedUsage) hasNegative() bool {
	return r.PromptTokens < 0 || r.OutputTokens < 0 ||
		r.CacheReadTokens < 0 || r.CacheRead5mTokens < 0 ||
		r.CacheWriteTokens < 0 || r.CacheWrite5mTokens < 0 || r.CacheWrite1hTokens < 0
}

// Usage is the canonical, provider-agnostic shape every rate is applied to.
//
// PromptTotalTokens and FreshInputTokens are deliberately separate fields
// rather than one number the engine subtracts from: the tier threshold keys
// off the total prompt size (that is what a "≤272K" context price means) while
// the input rate applies only to the fresh remainder.
type Usage struct {
	PromptTotalTokens int64
	FreshInputTokens  int64
	OutputTokens      int64

	CacheReadTokens    int64
	CacheRead5mTokens  int64
	CacheWriteTokens   int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
}

// TokensFor returns the token count billed under one class.
func (u Usage) TokensFor(class TokenClass) int64 {
	switch class {
	case ClassInputFresh:
		return u.FreshInputTokens
	case ClassOutput:
		return u.OutputTokens
	case ClassCacheRead:
		return u.CacheReadTokens
	case ClassCacheRead5m:
		return u.CacheRead5mTokens
	case ClassCacheWrite:
		return u.CacheWriteTokens
	case ClassCacheWrite5m:
		return u.CacheWrite5mTokens
	case ClassCacheWrite1h:
		return u.CacheWrite1hTokens
	default:
		return 0
	}
}

func (u Usage) negative() bool {
	for _, class := range TokenClasses {
		if u.TokensFor(class) < 0 {
			return true
		}
	}
	return u.PromptTotalTokens < 0
}

// NormalizeUsage converts one upstream's reported usage into the canonical
// shape, reporting any inconsistency it had to clamp.
func NormalizeUsage(reported ReportedUsage, mode PromptAccounting) (Usage, Anomaly) {
	anomaly := AnomalyNone
	if reported.hasNegative() {
		anomaly = AnomalyNegativeTokens
		reported = clampReported(reported)
	}

	cacheRead := reported.cacheReadTotal()
	cacheWrite := reported.cacheWriteTotal()
	cached := cacheRead + cacheWrite

	var promptTotal, fresh int64
	if mode == PromptExclusive {
		// Anthropic: the reported number is already the fresh remainder.
		fresh = reported.PromptTokens
		promptTotal = fresh + cached
	} else {
		// OpenAI-family: the reported number is the whole prompt.
		promptTotal = reported.PromptTokens
		fresh = promptTotal - cached
		if fresh < 0 {
			// The provider contradicted itself. Clamp, but do not hide it:
			// silently absorbing this is exactly how the original
			// undercharge stayed invisible.
			if anomaly == AnomalyNone {
				anomaly = AnomalyPromptBelowCache
			}
			fresh = 0
			promptTotal = cached
		}
	}

	return Usage{
		PromptTotalTokens:  promptTotal,
		FreshInputTokens:   fresh,
		OutputTokens:       reported.OutputTokens,
		CacheReadTokens:    reported.CacheReadTokens,
		CacheRead5mTokens:  reported.CacheRead5mTokens,
		CacheWriteTokens:   reported.CacheWriteTokens,
		CacheWrite5mTokens: reported.CacheWrite5mTokens,
		CacheWrite1hTokens: reported.CacheWrite1hTokens,
	}, anomaly
}

func clampReported(r ReportedUsage) ReportedUsage {
	clamp := func(value int64) int64 {
		if value < 0 {
			return 0
		}
		return value
	}
	return ReportedUsage{
		PromptTokens:       clamp(r.PromptTokens),
		OutputTokens:       clamp(r.OutputTokens),
		CacheReadTokens:    clamp(r.CacheReadTokens),
		CacheRead5mTokens:  clamp(r.CacheRead5mTokens),
		CacheWriteTokens:   clamp(r.CacheWriteTokens),
		CacheWrite5mTokens: clamp(r.CacheWrite5mTokens),
		CacheWrite1hTokens: clamp(r.CacheWrite1hTokens),
	}
}

// InputUsage builds the canonical usage for an admission-time estimate, where
// only a prompt-size upper bound is known and no cache activity has happened.
func InputUsage(promptTokens int64) Usage {
	return Usage{PromptTotalTokens: promptTokens, FreshInputTokens: promptTokens}
}
