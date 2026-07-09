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
		if len(system) > 0 {
			if block, ok := system[len(system)-1].(map[string]interface{}); ok && blockAcceptsCacheControl(block) {
				block["cache_control"] = ephemeral()
				injected = true
			}
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
				if len(content) > 0 {
					if block, ok := content[len(content)-1].(map[string]interface{}); ok && blockAcceptsCacheControl(block) {
						block["cache_control"] = ephemeral()
						injected = true
					}
				}
			}
		}
	}
	return injected
}
