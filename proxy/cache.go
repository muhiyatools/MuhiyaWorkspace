package proxy

import "strings"

// Anthropic prompt caching is OPT-IN: without explicit cache_control
// breakpoints the API caches nothing, so every request the gateway forwarded
// to an Anthropic upstream was re-billed at full input price. The gateway now
// injects two ephemeral breakpoints - one on the system prompt (covers the
// tools+system prefix) and one on the last message (covers the conversation
// so far) - the standard "cache everything stable" layout for agent loops.
//
// Injection never risks a 400: it is skipped when the client already manages
// its own cache_control anywhere in the body, when the target model predates
// caching, or when a shape is not recognized. A silently uncached request
// beats a broken one. OpenAI-protocol upstreams (DeepSeek, GLM, Kimi, ...)
// cache automatically and need nothing here.

// anthropicSupportsCaching: cache_control is accepted by Claude 3-era models
// and everything newer; claude-2/instant (and non-Claude Anthropic-protocol
// upstreams like DeepSeek-Anthropic) reject or ignore it.
func anthropicSupportsCaching(targetModel string) bool {
	model := strings.ToLower(targetModel)
	if !strings.Contains(model, "claude") {
		return false
	}
	return !strings.Contains(model, "claude-2") && !strings.Contains(model, "claude-instant")
}

// Block types that accept cache_control (thinking blocks notably do NOT).
func blockAcceptsCacheControl(block map[string]interface{}) bool {
	blockType, _ := block["type"].(string)
	switch blockType {
	case "text", "image", "tool_use", "tool_result", "document", "search_result":
		return true
	}
	return false
}

// lastCacheableBlock returns the last block in a content array that accepts a
// cache_control breakpoint, walking backwards past any trailing blocks that do
// not (notably thinking blocks, which reject cache_control). Returns nil when no
// block qualifies. Without this, a message or system array ending in a thinking
// block would get no breakpoint at all and cache nothing for that turn.
func lastCacheableBlock(blocks []interface{}) map[string]interface{} {
	for i := len(blocks) - 1; i >= 0; i-- {
		if block, ok := blocks[i].(map[string]interface{}); ok && blockAcceptsCacheControl(block) {
			return block
		}
	}
	return nil
}

func containsCacheControl(value interface{}) bool {
	switch v := value.(type) {
	case map[string]interface{}:
		if _, ok := v["cache_control"]; ok {
			return true
		}
		for _, child := range v {
			if containsCacheControl(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range v {
			if containsCacheControl(child) {
				return true
			}
		}
	}
	return false
}

// anthropicFamilyTarget reports whether an OpenRouter target id routes to an
// Anthropic (Claude) upstream, which - unlike DeepSeek/Qwen/GLM et al. - caches
// nothing without explicit cache_control breakpoints.
func anthropicFamilyTarget(targetModel string) bool {
	t := strings.ToLower(targetModel)
	return strings.Contains(t, "claude") || strings.HasPrefix(t, "anthropic/")
}

// setOpenAIContentCacheControl tags the last text part of an OpenAI-format
// message's content with cache_control, normalizing a plain string to the array
// form first. Returns true when a breakpoint was placed.
func setOpenAIContentCacheControl(msg map[string]interface{}, cc map[string]interface{}) bool {
	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return false
		}
		msg["content"] = []interface{}{map[string]interface{}{
			"type": "text", "text": content, "cache_control": cc,
		}}
		return true
	case []interface{}:
		for i := len(content) - 1; i >= 0; i-- {
			if part, ok := content[i].(map[string]interface{}); ok {
				if t, _ := part["type"].(string); t == "text" {
					part["cache_control"] = cc
					return true
				}
			}
		}
	}
	return false
}

// InjectOpenRouterAnthropicCache adds ephemeral cache_control breakpoints to an
// OpenAI-format body (map form) when the request routes to a Claude model
// through OpenRouter, which honors Anthropic-style cache_control on OpenAI
// content parts. Every auto-caching OpenRouter target (DeepSeek, Qwen, ...) is
// left byte-for-byte untouched, so the prefix-cache stability those providers
// rely on is preserved. Deterministic and no-op-safe: skipped unless the caller
// is OpenRouter with a Claude target, and skipped when the client already set
// its own breakpoints. Returns true when anything was injected.
func InjectOpenRouterAnthropicCache(body map[string]interface{}, isOpenRouter bool, targetModel string) bool {
	if body == nil || !isOpenRouter || !anthropicFamilyTarget(targetModel) {
		return false
	}
	if containsCacheControl(body) {
		return false
	}
	messages, ok := body["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return false
	}
	ephemeral := func() map[string]interface{} {
		return map[string]interface{}{"type": "ephemeral"}
	}
	injected := false
	// Breakpoint 1: the last system message (the stable tools+system prefix).
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "system" {
			continue
		}
		if setOpenAIContentCacheControl(msg, ephemeral()) {
			injected = true
		}
		break
	}
	// Breakpoint 2: the last message (caches the conversation prefix so the next
	// agent turn reads this turn from cache).
	if msg, ok := messages[len(messages)-1].(map[string]interface{}); ok {
		if setOpenAIContentCacheControl(msg, ephemeral()) {
			injected = true
		}
	}
	return injected
}

// InjectAnthropicCacheControl adds ephemeral cache breakpoints to an
// Anthropic-format request body (map form). Returns true when anything was
// injected.
func InjectAnthropicCacheControl(body map[string]interface{}, targetModel string) bool {
	if body == nil || !anthropicSupportsCaching(targetModel) {
		return false
	}
	// A client that sets its own breakpoints knows its cache layout better
	// than the gateway does - adding more could evict its entries.
	if containsCacheControl(body) {
		return false
	}
	ephemeral := func() map[string]interface{} {
		return map[string]interface{}{"type": "ephemeral"}
	}
	injected := false

	// Breakpoint 1: system prompt (caches the tools+system prefix).
	switch system := body["system"].(type) {
	case string:
		if system != "" {
			body["system"] = []interface{}{map[string]interface{}{
				"type": "text", "text": system, "cache_control": ephemeral(),
			}}
			injected = true
		}
	case []interface{}:
		if block := lastCacheableBlock(system); block != nil {
			block["cache_control"] = ephemeral()
			injected = true
		}
	}

	// Breakpoint 2: last message (caches the whole conversation prefix, so
	// the next turn of an agent loop reads this turn from cache).
	if messages, ok := body["messages"].([]interface{}); ok && len(messages) > 0 {
		if message, ok := messages[len(messages)-1].(map[string]interface{}); ok {
			switch content := message["content"].(type) {
			case string:
				if content != "" {
					message["content"] = []interface{}{map[string]interface{}{
						"type": "text", "text": content, "cache_control": ephemeral(),
					}}
					injected = true
				}
			case []interface{}:
				if block := lastCacheableBlock(content); block != nil {
					block["cache_control"] = ephemeral()
					injected = true
				}
			}
		}
	}
	return injected
}
