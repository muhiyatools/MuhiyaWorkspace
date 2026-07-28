package proxy

import (
	"strings"

	"gateway/db"
)

// ============================================================================
// Attachment / media capability core (feature 010).
// ----------------------------------------------------------------------------
// Every input modality a request can carry is detected here, matched against
// the operator-set per-model capability flags (db.Model.Supports*), and either
// routed to a capable model (router requests) or stripped with an honest note
// (explicit-model requests). Text-only requests never enter any of these paths
// with a non-zero need, so their routing behavior is unchanged.
//
// OpenAI-dialect content part types recognized:
//   image_url   → vision      input_audio → audio
//   video_url   → video       file        → documents (e.g. PDF)
// Anthropic-dialect content block types recognized:
//   image → vision            document → documents
// ============================================================================

// mediaNeeds captures which input modalities a request actually carries. The
// zero value means text-only.
type mediaNeeds struct {
	Vision    bool
	Audio     bool
	Video     bool
	Documents bool
}

// Any reports whether the request carries any non-text modality.
func (n mediaNeeds) Any() bool {
	return n.Vision || n.Audio || n.Video || n.Documents
}

// describe renders the needed modalities for logs and routing errors, e.g.
// "image+audio". Empty for text-only.
func (n mediaNeeds) describe() string {
	var parts []string
	if n.Vision {
		parts = append(parts, "image")
	}
	if n.Audio {
		parts = append(parts, "audio")
	}
	if n.Video {
		parts = append(parts, "video")
	}
	if n.Documents {
		parts = append(parts, "document")
	}
	return strings.Join(parts, "+")
}

// contentPartType extracts the "type" of one OpenAI-dialect content part
// (a map inside a message's content array). "" for anything else.
func contentPartType(item interface{}) string {
	m, ok := item.(map[string]interface{})
	if !ok {
		return ""
	}
	t, _ := m["type"].(string)
	return t
}

// mediaKindForPartType maps an OpenAI-dialect content part type to its
// capability kind ("image", "audio", "video", "document"); "" for text or
// unknown parts, which are never stripped or routed on.
func mediaKindForPartType(partType string) string {
	switch partType {
	case "image_url":
		return "image"
	case "input_audio":
		return "audio"
	case "video_url":
		return "video"
	case "file":
		return "document"
	}
	return ""
}

// noteNeed records a needed modality by kind.
func (n *mediaNeeds) noteKind(kind string) {
	switch kind {
	case "image":
		n.Vision = true
	case "audio":
		n.Audio = true
	case "video":
		n.Video = true
	case "document":
		n.Documents = true
	}
}

// detectMediaNeedsOpenAI scans OpenAI-dialect messages for media content parts.
// String content (the overwhelmingly common case) is text-only by definition.
func detectMediaNeedsOpenAI(messages []OpenAIMessage) mediaNeeds {
	var needs mediaNeeds
	for _, msg := range messages {
		arr, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, item := range arr {
			needs.noteKind(mediaKindForPartType(contentPartType(item)))
		}
	}
	return needs
}

// detectMediaNeedsAnthropic scans Anthropic-dialect messages for media blocks.
func detectMediaNeedsAnthropic(messages []AnthropicMessage) mediaNeeds {
	var needs mediaNeeds
	for _, msg := range messages {
		for _, block := range msg.Content {
			switch block.Type {
			case "image":
				needs.Vision = true
			case "document":
				needs.Documents = true
			}
		}
	}
	return needs
}

// modelMatchesVision reports whether a model can accept image input. It keys on
// the virtual name OR the provider target model containing a known vision
// keyword (gpt-4o, claude-3-5-sonnet, vision, gemini, gemma). The operator-set
// SupportsVision flag is authoritative; the name heuristic is a fallback for
// pre-flag rows. This is a capability predicate only — it never selects a model.
func modelMatchesVision(m *db.Model) bool {
	if m.SupportsVision {
		return true
	}
	nameLower := strings.ToLower(m.Name)
	targetLower := strings.ToLower(m.TargetModel)
	for _, kw := range []string{"gpt-4o", "claude-3-5-sonnet", "vision", "-vl", "gemini", "gemma", "pixtral", "llava"} {
		if strings.Contains(nameLower, kw) || strings.Contains(targetLower, kw) {
			return true
		}
	}
	return false
}

// modelSupportsMediaKind reports whether a model accepts one media kind.
// Vision keeps its flag-first-with-name-heuristic predicate (modelMatchesVision)
// for backward compatibility with pre-flag rows; the newer modalities are
// FLAG-ONLY — there is no reliable name heuristic for audio/video/documents,
// so the operator flag is the single source of truth.
func modelSupportsMediaKind(m *db.Model, kind string) bool {
	switch kind {
	case "image":
		return modelMatchesVision(m)
	case "audio":
		return m.SupportsAudio
	case "video":
		return m.SupportsVideo
	case "document":
		return m.SupportsDocuments
	}
	return true // text and unknown parts are universally acceptable
}

// modelSupportsMedia reports whether a model accepts EVERY modality the
// request needs. Text-only needs (zero value) match every model.
func modelSupportsMedia(m *db.Model, needs mediaNeeds) bool {
	if needs.Vision && !modelMatchesVision(m) {
		return false
	}
	if needs.Audio && !m.SupportsAudio {
		return false
	}
	if needs.Video && !m.SupportsVideo {
		return false
	}
	if needs.Documents && !m.SupportsDocuments {
		return false
	}
	return true
}

// mediaOmittedNote is the stable per-kind text substituted for a stripped
// part, so the model (and the cached prefix) see a deterministic note instead
// of a payload the upstream would 400 on. The image note predates feature 010
// and is kept byte-identical.
func mediaOmittedNote(kind string) string {
	switch kind {
	case "image":
		return "[image omitted — this model reads text only]"
	case "audio":
		return "[audio omitted — this model does not accept audio input]"
	case "video":
		return "[video omitted — this model does not accept video input]"
	case "document":
		return "[document omitted — this model does not accept document input]"
	}
	return "[attachment omitted]"
}

// stripUnsupportedMediaParts removes media content parts the target model
// cannot accept from every message, replacing each dropped kind with a short
// text note so the model knows something was omitted. Providers reject
// unsupported parts outright with a 400 (DeepSeek does for image_url), which
// would otherwise permanently break any conversation that ever contained one.
// Models that support everything present get the messages back untouched.
// (Router-selected requests keep their media — routing already guarantees the
// chosen model supports every needed modality.)
func stripUnsupportedMediaParts(messages []OpenAIMessage, model *db.Model) []OpenAIMessage {
	if model == nil || modelSupportsMedia(model, detectMediaNeedsOpenAI(messages)) {
		return messages
	}
	out := make([]OpenAIMessage, len(messages))
	copy(out, messages)
	for i := range out {
		arr, ok := out[i].Content.([]interface{})
		if !ok {
			continue
		}
		kept, notes := stripMediaFromParts(arr, model)
		if notes == nil {
			continue
		}
		out[i].Content = appendNotesToParts(kept, notes)
	}
	return out
}

// stripMediaFromParts filters one content array, returning the kept parts and
// the ordered, de-duplicated omission notes for dropped kinds (nil when
// nothing was dropped).
func stripMediaFromParts(arr []interface{}, model *db.Model) (kept []interface{}, notes []string) {
	seenNote := map[string]bool{}
	kept = make([]interface{}, 0, len(arr))
	for _, item := range arr {
		kind := mediaKindForPartType(contentPartType(item))
		if kind != "" && !modelSupportsMediaKind(model, kind) {
			if !seenNote[kind] {
				seenNote[kind] = true
				notes = append(notes, mediaOmittedNote(kind))
			}
			continue
		}
		kept = append(kept, item)
	}
	return kept, notes
}

// appendNotesToParts attaches omission notes to the first text part (or adds
// one) so the note rides inside the message the model actually reads.
func appendNotesToParts(kept []interface{}, notes []string) []interface{} {
	noteText := strings.Join(notes, "\n")
	for _, item := range kept {
		if m, isMap := item.(map[string]interface{}); isMap && m["type"] == "text" {
			if txt, isStr := m["text"].(string); isStr {
				m["text"] = txt + "\n" + noteText
				return kept
			}
		}
	}
	return append(kept, map[string]interface{}{"type": "text", "text": noteText})
}

// stripUnsupportedMediaInBodyMap applies the same per-model media strip to the
// generic JSON body used by proxyOpenAIToOpenAI (which forwards the client's
// original bytes rather than the typed struct). Mutates bodyMap["messages"] in
// place only when something must be dropped; a fully-capable model (or a body
// with no media parts) leaves the map untouched.
func stripUnsupportedMediaInBodyMap(bodyMap map[string]interface{}, model *db.Model) {
	if model == nil {
		return
	}
	msgs, ok := bodyMap["messages"].([]interface{})
	if !ok {
		return
	}
	for _, raw := range msgs {
		msg, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		arr, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		kept, notes := stripMediaFromParts(arr, model)
		if notes == nil {
			continue
		}
		msg["content"] = appendNotesToParts(kept, notes)
	}
}

// bodyMapHasFilePart reports whether any message content part is a document
// (`file`) part — the trigger for OpenRouter's PDF parser plugin.
func bodyMapHasFilePart(bodyMap map[string]interface{}) bool {
	msgs, ok := bodyMap["messages"].([]interface{})
	if !ok {
		return false
	}
	for _, raw := range msgs {
		msg, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		arr, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, item := range arr {
			if contentPartType(item) == "file" {
				return true
			}
		}
	}
	return false
}

// maybeInjectOpenRouterPDFParser pins OpenRouter's FREE `pdf-text` parsing
// engine whenever a document (file) part is forwarded to an OpenRouter
// upstream and the client did not configure its own plugins. Without an
// explicit engine OpenRouter may fall back to its paid OCR engine ($2/1k
// pages) — a silent, abusable cost. Text-based PDFs parse fine for free;
// operators who need OCR for scanned documents can override via the
// `openrouter_pdf_engine` system setting. No-op for every other provider.
func maybeInjectOpenRouterPDFParser(bodyMap map[string]interface{}, provider *db.Provider, engine string) {
	if provider == nil || (!isOpenRouterBase(provider.BaseURL) && provider.ID != "openrouter") {
		return
	}
	if _, exists := bodyMap["plugins"]; exists {
		return // never override client-configured plugins
	}
	if !bodyMapHasFilePart(bodyMap) {
		return
	}
	if strings.TrimSpace(engine) == "" {
		engine = "pdf-text"
	}
	bodyMap["plugins"] = []interface{}{
		map[string]interface{}{
			"id":  "file-parser",
			"pdf": map[string]interface{}{"engine": engine},
		},
	}
}

// openRouterPDFEngine resolves the operator-configurable PDF parsing engine
// (system setting `openrouter_pdf_engine`), defaulting to the free `pdf-text`.
func (h *ProxyHandler) openRouterPDFEngine() string {
	val, _ := h.db.GetSetting("openrouter_pdf_engine")
	if strings.TrimSpace(val) == "" {
		return "pdf-text"
	}
	return strings.TrimSpace(val)
}

// splitMimeList parses the comma-separated accepted_mime_types column into a
// clean slice. Empty column → empty (non-nil) slice so /v1/models always
// publishes an array, never null.
func splitMimeList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// modelInputModalities lists the input modalities a model accepts, always
// starting with "text". Drives the /v1/models `input_modalities` field so
// clients can render honest capability lists without knowing flag names.
func modelInputModalities(m *db.Model) []string {
	mods := []string{"text"}
	if modelMatchesVision(m) {
		mods = append(mods, "image")
	}
	if m.SupportsAudio {
		mods = append(mods, "audio")
	}
	if m.SupportsVideo {
		mods = append(mods, "video")
	}
	if m.SupportsDocuments {
		mods = append(mods, "document")
	}
	return mods
}

// modelCapabilityFields is the single builder for the capability tags every
// /v1/models payload publishes (list + detail, OpenAI + Anthropic dialects),
// so the four response shapes can never drift apart.
func modelCapabilityFields(m *db.Model) map[string]interface{} {
	return map[string]interface{}{
		"supports_vision":              m.SupportsVision,
		"supports_thinking":            m.SupportsThinking,
		"supports_audio":               m.SupportsAudio,
		"supports_video":               m.SupportsVideo,
		"supports_documents":           m.SupportsDocuments,
		"max_attachment_mb":            m.MaxAttachmentMB,
		"accepted_mime_types":          splitMimeList(m.AcceptedMimeTypes),
		"input_modalities":             modelInputModalities(m),
		"input_cost_per_million":       m.InputCostPerMillion,
		"output_cost_per_million":      m.OutputCostPerMillion,
		"cache_read_cost_per_million":  m.CacheReadCostPerMillion,
		"cache_write_cost_per_million": m.CacheWriteCostPerMillion,
		"pricing": map[string]interface{}{
			"currency":                         "USD",
			"unit":                             "per_million_tokens",
			"input_nano_usd_per_million":       m.InputCostNanoPerMillion,
			"output_nano_usd_per_million":      m.OutputCostNanoPerMillion,
			"cache_read_nano_usd_per_million":  m.CacheReadCostNanoPerMillion,
			"cache_write_nano_usd_per_million": m.CacheWriteCostNanoPerMillion,
			"price_per_minute_nano_usd":        m.PricePerMinuteNano,
			"tiers":                            m.PricingTiers,
		},
	}
}
