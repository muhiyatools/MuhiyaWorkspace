package proxy

import (
	"bytes"
	"fmt"
	"gateway/db"
	"log"
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

// modelSupportsThinking reports whether a model supports reasoning/thinking
// effort. The operator-set SupportsThinking flag is authoritative; the target-
// name heuristic is only a fallback for unflagged rows. This steers
// muhiya-ai-router MODEL SELECTION with strict equality matching, so the flag
// lets an operator include/exclude a model from the thinking tier explicitly
// rather than relying on a substring guess. Thinking-level MAPPING is
// independent (proxy/thinking.go).
func modelSupportsThinking(m *db.Model) bool {
	if m.SupportsThinking {
		return true
	}
	target := strings.ToLower(m.TargetModel)
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
// remains and the request does not need a free-only capability. Media requests
// (image/audio/video/document) keep free models (that is how the $0 Gemma
// vision model is intended to be reached). If every candidate is free, the set
// is returned unchanged so routing never dead-ends.
func preferPaid(candidates []*db.Model, keepFree bool) []*db.Model {
	if keepFree {
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

func (h *ProxyHandler) RouteToModel(complexity string, needs mediaNeeds, thinkingRequested bool) (*db.Model, error) {
	models, err := h.db.ListModels()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve models: %w", err)
	}
	providerActive, err := h.activeProviderSet()
	if err != nil {
		return nil, err
	}
	return selectRoute(models, providerActive, complexity, needs, thinkingRequested)
}

// activeProviderSet returns a map of provider ID -> whether that provider is
// active. Routing must never select a model whose provider is inactive/unkeyed:
// such a model would pass every model-level filter and then hard-fail at the
// upstream call. Loading providers once per route is cheap relative to the
// upstream request that follows.
func (h *ProxyHandler) activeProviderSet() (map[string]bool, error) {
	providers, err := h.db.ListProviders()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve providers: %w", err)
	}
	set := make(map[string]bool, len(providers))
	for i := range providers {
		set[providers[i].ID] = providers[i].Status == "active"
	}
	return set, nil
}

// routingDiag captures why a route did or did not resolve, so a failure can be
// reported with actionable numbers instead of the opaque "no active models".
type routingDiag struct {
	total, active, eligible                                        int
	visionCapable, audioCapable, videoCapable, documentCapable int
}

// computeRoutingDiag counts the model pool at each narrowing stage: total rows,
// active rows, rows that are active AND on an active provider AND not a
// transcription model (the genuinely routable pool), and how many of those can
// accept each media kind. eligible + the per-capability counts are what a
// routing failure hinges on.
func computeRoutingDiag(models []db.Model, providerActive map[string]bool) routingDiag {
	var d routingDiag
	d.total = len(models)
	for i := range models {
		m := &models[i]
		if m.Status == "active" {
			d.active++
		}
		if m.Status != "active" || m.Transcribe || !providerActive[m.ProviderID] {
			continue
		}
		d.eligible++
		if modelMatchesVision(m) {
			d.visionCapable++
		}
		if m.SupportsAudio {
			d.audioCapable++
		}
		if m.SupportsVideo {
			d.videoCapable++
		}
		if m.SupportsDocuments {
			d.documentCapable++
		}
	}
	return d
}

// capabilityCount maps a media kind to how many eligible models support it.
func (d routingDiag) capabilityCount(kind string) int {
	switch kind {
	case "image":
		return d.visionCapable
	case "audio":
		return d.audioCapable
	case "video":
		return d.videoCapable
	case "document":
		return d.documentCapable
	}
	return 0
}

// capabilityFlagLabel names the admin flag that unblocks a media kind.
func capabilityFlagLabel(kind string) string {
	switch kind {
	case "image":
		return "Vision"
	case "audio":
		return "Audio"
	case "video":
		return "Video"
	case "document":
		return "Documents"
	}
	return kind
}

// routingError builds a human-actionable failure from the pool diagnostics. It
// names the exact remediation (activate a model / flag the missing capability)
// so the operator never has to guess which filter emptied the candidate set.
// "vision required" is kept verbatim for image-only requests so existing log
// scrapers and client error matching keep working.
func routingError(d routingDiag, needs mediaNeeds) error {
	if needs.Vision && !needs.Audio && !needs.Video && !needs.Documents {
		return fmt.Errorf("no routable model (vision required): %d models, %d active, %d on active providers, %d vision-capable — activate a vision-capable model (or tick its Vision flag) in Admin → Models",
			d.total, d.active, d.eligible, d.visionCapable)
	}
	if needs.Any() {
		// Name each needed modality with its eligible-pool count; the sparsest
		// one is what the operator needs to flag/activate.
		var kinds, counts []string
		for _, kind := range []string{"image", "audio", "video", "document"} {
			var needed bool
			switch kind {
			case "image":
				needed = needs.Vision
			case "audio":
				needed = needs.Audio
			case "video":
				needed = needs.Video
			case "document":
				needed = needs.Documents
			}
			if !needed {
				continue
			}
			kinds = append(kinds, kind)
			counts = append(counts, fmt.Sprintf("%d %s-capable", d.capabilityCount(kind), kind))
		}
		var flags []string
		for _, k := range kinds {
			flags = append(flags, capabilityFlagLabel(k))
		}
		return fmt.Errorf("no routable model (%s input required): %d models, %d active, %d on active providers, %s — activate a model with the %s flag(s) in Admin → Models",
			strings.Join(kinds, "+"), d.total, d.active, d.eligible, strings.Join(counts, ", "), strings.Join(flags, "+"))
	}
	return fmt.Errorf("no routable model: %d models, %d active, %d on active providers — activate a model on an active provider in Admin → Models",
		d.total, d.active, d.eligible)
}

// selectRoute is the pure routing core: given the model list, a provider-active
// map, and the request shape, it returns the cheapest eligible model. It has no
// DB dependency so the routing ladder is unit-testable. The four fallback tiers
// (exact tier+thinking, exact tier, thinking-only, anything) are expressed as
// data; every tier additionally requires the model's provider to be active and
// the model to support every media modality the request carries.
func selectRoute(models []db.Model, providerActive map[string]bool, complexity string, needs mediaNeeds, thinkingRequested bool) (*db.Model, error) {
	matchFilter := func(m *db.Model, tierCheck, thinkingCheck bool) bool {
		if m.Status != "active" || m.Transcribe {
			return false
		}
		if !providerActive[m.ProviderID] {
			return false
		}
		if tierCheck && m.RoutingTier != complexity {
			return false
		}
		if !modelSupportsMedia(m, needs) {
			return false
		}
		if thinkingCheck && modelSupportsThinking(m) != thinkingRequested {
			return false
		}
		return true
	}

	// The ladder, loosest-last. Byte-equivalent to the previous four inlined
	// blocks, now with the provider gate folded into matchFilter.
	tiers := []struct{ tierCheck, thinkingCheck bool }{
		{true, true},
		{true, false},
		{false, true},
		{false, false},
	}

	var candidates []*db.Model
	for _, t := range tiers {
		for i := range models {
			m := &models[i]
			if matchFilter(m, t.tierCheck, t.thinkingCheck) {
				candidates = append(candidates, m)
			}
		}
		if len(candidates) > 0 {
			break
		}
	}

	if len(candidates) == 0 {
		d := computeRoutingDiag(models, providerActive)
		log.Printf("[ROUTING-FAIL] media=%q thinking=%v complexity=%s :: total=%d active=%d eligible=%d vision=%d audio=%d video=%d document=%d",
			needs.describe(), thinkingRequested, complexity, d.total, d.active, d.eligible, d.visionCapable, d.audioCapable, d.videoCapable, d.documentCapable)
		return nil, routingError(d, needs)
	}

	// Keep free models out of general (text-only) routing when a paid model is
	// available, so a $0 rate-limited model never becomes the default route.
	// Media requests keep free candidates (the free vision model is reached
	// exactly this way).
	candidates = preferPaid(candidates, needs.Any())

	sortByPrice(candidates)
	return candidates[0], nil
}

// GetFallbackModels returns a list of alternative active models for load-balancing / failover,
// excluding the primary model already tried.
func (h *ProxyHandler) GetFallbackModels(excludeModelID string, needs mediaNeeds) ([]*db.Model, error) {
	models, err := h.db.ListModels()
	if err != nil {
		return nil, err
	}
	providerActive, err := h.activeProviderSet()
	if err != nil {
		return nil, err
	}
	return filterFallbacks(models, providerActive, excludeModelID, needs), nil
}

// filterFallbacks is the pure failover-candidate builder: active, non-excluded,
// non-transcribe models on an active provider, media-capability-filtered when
// required, free-safe, cheapest-first. DB-free for testability.
func filterFallbacks(models []db.Model, providerActive map[string]bool, excludeModelID string, needs mediaNeeds) []*db.Model {
	var fallbacks []*db.Model
	for i := range models {
		m := &models[i]
		if m.Status == "active" && m.ID != excludeModelID && !m.Transcribe && providerActive[m.ProviderID] {
			if !modelSupportsMedia(m, needs) {
				continue
			}
			fallbacks = append(fallbacks, m)
		}
	}

	// A text request must never fail over INTO a free (rate-limited) model when a
	// paid alternative exists; a media request keeps free capable models.
	fallbacks = preferPaid(fallbacks, needs.Any())
	sortByPrice(fallbacks)
	return fallbacks
}

// sortByPrice orders models cheapest-first by summed input+output per-million
// cost (insertion-stable bubble, tiny N). Shared by routing and fallback so
// credit optimization is identical on both paths.
func sortByPrice(models []*db.Model) {
	for i := 0; i < len(models); i++ {
		for j := i + 1; j < len(models); j++ {
			costI := models[i].InputCostPerMillion + models[i].OutputCostPerMillion
			costJ := models[j].InputCostPerMillion + models[j].OutputCostPerMillion
			if costJ < costI {
				models[i], models[j] = models[j], models[i]
			}
		}
	}
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
