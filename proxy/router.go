package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"gateway/db"
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

	for _, msg := range messages {
		content := strings.ToLower(GetMessageContentString(msg.Content))
		totalLen += len(content)

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

// RouteToModel resolves a complexity level and optional vision requirement to the cheapest active model
func (h *ProxyHandler) RouteToModel(complexity string, needsVision bool) (*db.Model, error) {
	models, err := h.db.ListModels()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve models: %w", err)
	}

	// 1. Filter active models assigned to the exact requested routing tier
	var candidates []*db.Model
	for i := range models {
		m := &models[i]
		if m.Status == "active" && m.RoutingTier == complexity && !m.Transcribe {
			if needsVision {
				nameLower := strings.ToLower(m.Name)
				if !strings.Contains(nameLower, "gpt-4o") && 
				   !strings.Contains(nameLower, "claude-3-5-sonnet") && 
				   !strings.Contains(nameLower, "vision") && 
				   !strings.Contains(nameLower, "gemini") {
					continue
				}
			}
			candidates = append(candidates, m)
		}
	}

	// 2. If no candidate matches the exact tier, fall back to any active model
	if len(candidates) == 0 {
		for i := range models {
			m := &models[i]
			if m.Status == "active" && !m.Transcribe {
				if needsVision {
					nameLower := strings.ToLower(m.Name)
					if !strings.Contains(nameLower, "gpt-4o") && 
					   !strings.Contains(nameLower, "claude-3-5-sonnet") && 
					   !strings.Contains(nameLower, "vision") && 
					   !strings.Contains(nameLower, "gemini") {
						continue
					}
				}
				candidates = append(candidates, m)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no active models available for routing")
	}

	// 3. Sort candidates by price (cheapest first) to guarantee credit optimization!
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
			if needsVision {
				nameLower := strings.ToLower(m.Name)
				if !strings.Contains(nameLower, "gpt-4o") && 
				   !strings.Contains(nameLower, "claude-3-5-sonnet") && 
				   !strings.Contains(nameLower, "vision") && 
				   !strings.Contains(nameLower, "gemini") {
					continue
				}
			}
			fallbacks = append(fallbacks, m)
		}
	}

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
	actual      http.ResponseWriter
	statusCode  int
	headers     http.Header
	buf         bytes.Buffer
	flushed     bool
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
