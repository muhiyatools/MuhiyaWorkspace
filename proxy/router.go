package proxy

import (
	"bytes"
	"fmt"
	"gateway/db"
	"net/http"
	"strings"
)

// AnalyzePromptComplexity evaluates the messages in an OpenAIRequest and returns 'simple', 'medium', or 'hard'
func AnalyzePromptComplexity(messages []OpenAIMessage) string {
	totalLen := 0
	hasCodingKeywords := false
	hasDeepReasoning := false

	// Common terms that indicate technical or complex requests
	codingKeywords := []string{
		"func ", "package ", "import ", "def ", "class ", "struct ", "interface ",
		"select ", "insert ", "update ", "delete ", "react", "typescript", "golang",
		"pointer", "memory", "performance", "bug", "issue", "error", "stacktrace",
		"exception", "c++", "rust", "javascript", "python", "css", "html", "compiler",
		"كود", "برمجة", "خطأ", "تصحيح", "قاعدة بيانات", "مصفوفة", "دالة",
	}

	reasoningKeywords := []string{
		"prove ", "theorem", "mathematical", "equation", "solve", "algorithm",
		"architecture", "complex", "analysis", "optimization", "world cup results",
		"points table", "standings", "financial report", "جدول", "ترتيب", "مقارنة",
		"تحليل", "معادلة", "خوارزمية",
	}

	// Score only the FIRST user turn. Scoring the whole (monotonically
	// growing) transcript let a session's measured complexity creep upward
	// turn by turn and silently cross a tier boundary mid-conversation -
	// and because DeepSeek's prefix cache is per upstream model, a
	// mid-session re-route is a full cache wipe. Session stickiness
	// (stickysession.go) backstops this even if a re-route is attempted
	// anyway, but not scoring the growing transcript avoids manufacturing
	// re-route pressure in the first place.
	var firstUserContent string
	for _, msg := range messages {
		if msg.Role == "user" {
			firstUserContent = GetMessageContentString(msg.Content)
			break
		}
	}
	content := strings.ToLower(firstUserContent)
	totalLen = len(content)

	for _, keyword := range codingKeywords {
		if strings.Contains(content, keyword) {
			hasCodingKeywords = true
			break
		}
	}

	for _, keyword := range reasoningKeywords {
		if strings.Contains(content, keyword) {
			hasDeepReasoning = true
			break
		}
	}

	// 1. Hard: Requires strong logic, coding expertise, or very large context
	if totalLen > 1500 || hasDeepReasoning || (hasCodingKeywords && totalLen > 600) {
		return "hard"
	}
	// 2. Medium: Moderate length, general research, or short coding queries
	if totalLen > 400 || hasCodingKeywords {
		return "medium"
	}
	// 3. Simple: Conversational and quick questions
	return "simple"
}

// NOTE: deliberately unchanged by the thinking-level feature. This heuristic
// steers muhiya-ai-router MODEL SELECTION with strict equality matching, so
// widening it would silently re-route existing non-thinking traffic away from
// newly-matched models. Thinking-level MAPPING is independent (proxy/thinking.go).
func modelSupportsThinking(targetModel string) bool {
	target := strings.ToLower(targetModel)
	return strings.Contains(target, "reasoner") ||
		strings.Contains(target, "r1") ||
		strings.Contains(target, "o1") ||
		strings.Contains(target, "o3") ||
		strings.Contains(target, "claude-3-7")
}

// RouteToModel resolves a complexity level, optional vision requirement, and thinking preference to the cheapest active model
// modelMatchesVision reports whether a model can accept image input. It keys on
// the virtual name OR the provider target model containing a known vision
// keyword (gpt-4o, claude-3-5-sonnet, vision, gemini, gemma). Naming a new
// vision model with one of these substrings makes routing pick it up
// automatically; e.g. gemma-4-vision → google/gemma-4-31b-it:free (OpenRouter).
func modelMatchesVision(m *db.Model) bool {
	// The operator-set flag is authoritative — a vision model with ANY name is
	// routed correctly once flagged.
	if m.SupportsVision {
		return true
	}
	// Fallback heuristic for rows that predate the flag / were never flagged.
	nameLower := strings.ToLower(m.Name)
	targetLower := strings.ToLower(m.TargetModel)
	for _, kw := range []string{"gpt-4o", "claude-3-5-sonnet", "vision", "-vl", "gemini", "gemma", "pixtral", "llava"} {
		if strings.Contains(nameLower, kw) || strings.Contains(targetLower, kw) {
			return true
		}
	}
	return false
}

// modelIsFree reports a $0 model — a rate-limited specialist (e.g. a free
// OpenRouter tier), NOT a general route. Without this guard a $0 model wins the
// cheapest-first price sort and captures ALL traffic (including text), then its
// free-tier rate limit 429s every request. Free models are reached on demand
// (e.g. the free Gemma vision model for image input), never as the default.
func modelIsFree(m *db.Model) bool {
	return m.InputCostPerMillion == 0 && m.OutputCostPerMillion == 0
}

// preferPaid drops free models from a candidate set when at least one paid model
// remains and the request does not need a free-only capability. Vision requests
// keep free models (that is how the $0 Gemma vision model is intended to be
// reached). If every candidate is free, the set is returned unchanged so routing
// never dead-ends.
func preferPaid(candidates []*db.Model, needsVision bool) []*db.Model {
	if needsVision {
		return candidates
	}
	paid := make([]*db.Model, 0, len(candidates))
	for _, m := range candidates {
		if !modelIsFree(m) {
			paid = append(paid, m)
		}
	}
	if len(paid) > 0 {
		return paid
	}
	return candidates
}

func (h *ProxyHandler) RouteToModel(complexity string, needsVision bool, thinkingRequested bool) (*db.Model, error) {
	models, err := h.db.ListModels()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve models: %w", err)
	}

	// Helper to check if model meets all criteria
	matchFilter := func(m *db.Model, tierCheck bool) bool {
		if m.Status != "active" || m.Transcribe {
			return false
		}
		if tierCheck && m.RoutingTier != complexity {
			return false
		}
		if needsVision && !modelMatchesVision(m) {
			return false
		}
		return modelSupportsThinking(m.TargetModel) == thinkingRequested
	}

	// 1. Try exact tier + thinking criteria
	var candidates []*db.Model
	for i := range models {
		m := &models[i]
		if matchFilter(m, true) {
			candidates = append(candidates, m)
		}
	}

	// 2. Fallback: Exact tier but ignore thinking filter
	if len(candidates) == 0 {
		for i := range models {
			m := &models[i]
			if m.Status == "active" && m.RoutingTier == complexity && !m.Transcribe {
				if needsVision && !modelMatchesVision(m) {
					continue
				}
				candidates = append(candidates, m)
			}
		}
	}

	// 3. Fallback: Ignore tier check but keep thinking criteria
	if len(candidates) == 0 {
		for i := range models {
			m := &models[i]
			if matchFilter(m, false) {
				candidates = append(candidates, m)
			}
		}
	}

	// 4. Ultimate fallback: ignore all except active status and vision
	if len(candidates) == 0 {
		for i := range models {
			m := &models[i]
			if m.Status == "active" && !m.Transcribe {
				if needsVision && !modelMatchesVision(m) {
					continue
				}
				candidates = append(candidates, m)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no active models available for routing")
	}

	// Keep free models out of general (non-vision) routing when a paid model is
	// available, so a $0 rate-limited model never becomes the default route.
	candidates = preferPaid(candidates, needsVision)

	// Sort candidates by price (cheapest first) to guarantee credit optimization!
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			costI := candidates[i].InputCostPerMillion + candidates[i].OutputCostPerMillion
			costJ := candidates[j].InputCostPerMillion + candidates[j].OutputCostPerMillion
			if costJ < costI {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	return candidates[0], nil
}

// GetFallbackModels returns a list of alternative active models for load-balancing / failover,
// excluding the primary model already tried.
func (h *ProxyHandler) GetFallbackModels(excludeModelID string, needsVision bool) ([]*db.Model, error) {
	models, err := h.db.ListModels()
	if err != nil {
		return nil, err
	}

	var fallbacks []*db.Model
	for i := range models {
		m := &models[i]
		if m.Status == "active" && m.ID != excludeModelID && !m.Transcribe {
			if needsVision && !modelMatchesVision(m) {
				continue
			}
			fallbacks = append(fallbacks, m)
		}
	}

	// A text request must never fail over INTO a free (rate-limited) model when a
	// paid alternative exists; a vision request keeps free vision models.
	fallbacks = preferPaid(fallbacks, needsVision)

	// Sort fallbacks so that cheaper ones are tried first (smart credit usage!)
	for i := 0; i < len(fallbacks); i++ {
		for j := i + 1; j < len(fallbacks); j++ {
			costI := fallbacks[i].InputCostPerMillion + fallbacks[i].OutputCostPerMillion
			costJ := fallbacks[j].InputCostPerMillion + fallbacks[j].OutputCostPerMillion
			if costJ < costI {
				fallbacks[i], fallbacks[j] = fallbacks[j], fallbacks[i]
			}
		}
	}

	return fallbacks, nil
}

// ====================================================================
// BufferedResponseWriter - Intercepts and buffers errors for failovers
// ====================================================================

type BufferedResponseWriter struct {
	actual     http.ResponseWriter
	statusCode int
	headers    http.Header
	buf        bytes.Buffer
	flushed    bool
}

func NewBufferedResponseWriter(actual http.ResponseWriter) *BufferedResponseWriter {
	return &BufferedResponseWriter{
		actual:  actual,
		headers: make(http.Header),
	}
}

func (b *BufferedResponseWriter) Header() http.Header {
	return b.headers
}

func (b *BufferedResponseWriter) WriteHeader(statusCode int) {
	b.statusCode = statusCode
}

func (b *BufferedResponseWriter) Write(p []byte) (int, error) {
	if b.statusCode == 0 {
		b.statusCode = http.StatusOK
	}

	// If it's a successful response and we haven't flushed headers yet, do it now
	if b.statusCode < 400 {
		if !b.flushed {
			b.FlushToActual()
		}
		return b.actual.Write(p)
	}

	// If it's an error response, buffer it so we can discard it on retry
	return b.buf.Write(p)
}

func (b *BufferedResponseWriter) FlushToActual() {
	if b.flushed {
		return
	}

	// Copy headers to actual writer
	for k, vv := range b.headers {
		for _, v := range vv {
			b.actual.Header().Add(k, v)
		}
	}

	if b.statusCode != 0 {
		b.actual.WriteHeader(b.statusCode)
	}

	if b.buf.Len() > 0 {
		b.actual.Write(b.buf.Bytes())
		b.buf.Reset()
	}

	b.flushed = true
}

func (b *BufferedResponseWriter) Flush() {
	if b.flushed {
		if f, ok := b.actual.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// Ensure BufferedResponseWriter implements http.ResponseWriter and http.Flusher
var _ http.ResponseWriter = (*BufferedResponseWriter)(nil)
var _ http.Flusher = (*BufferedResponseWriter)(nil)
