package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gateway/db"
)

// ============================================================================
// Native Tool Layer
// ----------------------------------------------------------------------------
// This file defines the gateway's built-in agentic tools (web search + live
// football data). Tools are executed INSIDE the gateway during the MuhiyaChat
// agent loop (see agent.go). Each executor returns:
//   - LLMContent: compact text fed back to the model as the tool result
//   - ClientEvent: an optional structured payload streamed to the MuhiyaChat
//     UI (rendered as source citations or live football cards)
//
// These files are additive and are only invoked when a request opts into the
// agent loop (X-Client-App: MuhiyaChat). All other proxy paths are untouched.
// ============================================================================

// toolHTTPClient is a short-timeout client for external tool APIs (search,
// football). It is intentionally separate from the long-lived LLM httpClient.
var toolHTTPClient = &http.Client{
	Timeout: 12 * time.Second,
}

// ToolContext carries everything a tool executor needs at run time.
type ToolContext struct {
	DB         *db.DB
	Settings   ToolSettings
	Complexity string
}

// ToolSettings holds the external API keys, loaded once per request from the
// gateway's system_settings table.
type ToolSettings struct {
	SerperAPIKey    string // Serper (google search) key — primary web search
}

// ToolExecution is the result of running a single tool call.
type ToolExecution struct {
	LLMContent  string                 // fed back to the model
	ClientEvent map[string]interface{} // optional muhiya_* payload for the UI
}

// LoadToolSettings reads the tool API keys from system_settings. Missing keys
// are returned as empty strings; executors degrade gracefully.
func LoadToolSettings(database *db.DB) ToolSettings {
	get := func(k string) string {
		if database == nil {
			return ""
		}
		v, _ := database.GetSetting(k)
		return strings.TrimSpace(v)
	}
	return ToolSettings{
		SerperAPIKey:    get("serper_api_key"),
	}
}

// NewToolContext builds a ToolContext with freshly-loaded settings.
func NewToolContext(database *db.DB) *ToolContext {
	return &ToolContext{
		DB:       database,
		Settings: LoadToolSettings(database),
	}
}

// ToolsEnabled reports whether at least one tool can run given current config.
// If no keys are configured the agent loop is skipped entirely.
func (s ToolSettings) ToolsEnabled() bool {
	return s.SerperAPIKey != ""
}

// ----------------------------------------------------------------------------
// Tool schema definitions (OpenAI function-calling format)
// ----------------------------------------------------------------------------

// BuildToolSchemas returns the tool definitions to advertise to the model.
// Only tools whose backing API key is configured are included, so the model
// never calls a tool the gateway cannot fulfil.
func BuildToolSchemas(s ToolSettings) []OpenAITool {
	var tools []OpenAITool

	if s.SerperAPIKey != "" {
		tools = append(tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        "web_search",
				Description: "Search the live web for current information: news, prices, weather, general facts, anything after your training cutoff. Returns ranked sources with snippets. Use for anything time-sensitive or that you are not certain about.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query. Be specific and include key entities.",
						},
						"recency": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"day", "week", "month", "year", "any"},
							"description": "Bias results toward this recency window. Use 'day' or 'week' for breaking news.",
						},
					},
					"required": []string{"query"},
				},
			},
		})
	}

	return tools
}

// IsNativeTool reports whether the given function name is handled by the
// gateway (as opposed to a client-supplied passthrough tool).
func IsNativeTool(name string) bool {
	return name == "web_search"
}

// ExecuteTool dispatches a single tool call to its executor.
func (tc *ToolContext) ExecuteTool(name string, rawArgs string) ToolExecution {
	args := parseToolArgs(rawArgs)
	switch name {
	case "web_search":
		return tc.execWebSearch(args)
	default:
		return ToolExecution{LLMContent: fmt.Sprintf("Tool '%s' is not available.", name)}
	}
}

// parseToolArgs decodes the JSON argument string emitted by the model. It
// tolerates empty / malformed args by returning an empty map.
func parseToolArgs(raw string) map[string]interface{} {
	out := map[string]interface{}{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// argString safely extracts a trimmed string argument.
func argString(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// argInt safely extracts an integer argument (JSON numbers decode to float64).
func argInt(args map[string]interface{}, key string) (int, bool) {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n), true
		case int:
			return n, true
		case string:
			var i int
			if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err == nil {
				return i, true
			}
		}
	}
	return 0, false
}
