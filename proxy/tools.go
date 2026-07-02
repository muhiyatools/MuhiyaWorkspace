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
	DB       *db.DB
	Settings ToolSettings
	// cache for name->id resolutions within a single request (saves API quota)
	teamIDCache   map[string]int
	leagueIDCache map[string]int
}

// ToolSettings holds the external API keys, loaded once per request from the
// gateway's system_settings table.
type ToolSettings struct {
	FootballAPIKey  string // API-Football (api-sports.io) key
	FootballBaseURL string // defaults to https://v3.football.api-sports.io
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
	base := get("football_api_base_url")
	if base == "" {
		base = "https://v3.football.api-sports.io"
	}
	return ToolSettings{
		FootballAPIKey:  get("football_api_key"),
		FootballBaseURL: strings.TrimRight(base, "/"),
		SerperAPIKey:    get("serper_api_key"),
	}
}

// NewToolContext builds a ToolContext with freshly-loaded settings.
func NewToolContext(database *db.DB) *ToolContext {
	return &ToolContext{
		DB:            database,
		Settings:      LoadToolSettings(database),
		teamIDCache:   map[string]int{},
		leagueIDCache: map[string]int{},
	}
}

// ToolsEnabled reports whether at least one tool can run given current config.
// If no keys are configured the agent loop is skipped entirely.
func (s ToolSettings) ToolsEnabled() bool {
	return s.FootballAPIKey != "" || s.SerperAPIKey != ""
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

	if s.FootballAPIKey != "" {
		tools = append(tools,
			OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDef{
					Name:        "football_live_scores",
					Description: "Get football (soccer) matches currently being played, with live scores and elapsed minutes. Optionally filter by league name (e.g. 'Premier League').",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"league": map[string]interface{}{
								"type":        "string",
								"description": "Optional league name to filter by, e.g. 'Premier League', 'La Liga', 'Champions League'.",
							},
						},
					},
				},
			},
			OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDef{
					Name:        "football_fixtures",
					Description: "Get football fixtures/results for a team, league, or specific date. Use for 'when does X play next', 'X last result', or 'matches on <date>'.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"team":   map[string]interface{}{"type": "string", "description": "Team name, e.g. 'Real Madrid'."},
							"league": map[string]interface{}{"type": "string", "description": "League name, e.g. 'Serie A'."},
							"date":   map[string]interface{}{"type": "string", "description": "A specific date in YYYY-MM-DD format."},
							"when":   map[string]interface{}{"type": "string", "enum": []string{"next", "last"}, "description": "For a team: 'next' upcoming fixtures or 'last' recent results. Defaults to 'next'."},
						},
					},
				},
			},
			OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDef{
					Name:        "football_standings",
					Description: "Get the league table / standings for a competition and season. Use for 'who is top of X', 'X table', 'league position'.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"league": map[string]interface{}{"type": "string", "description": "League name, e.g. 'Premier League'."},
							"season": map[string]interface{}{"type": "integer", "description": "Season start year, e.g. 2025. Defaults to the current season."},
						},
						"required": []string{"league"},
					},
				},
			},
			OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDef{
					Name:        "football_team",
					Description: "Get basic information and the recent/next fixtures for a single football team. Use for 'tell me about <team>'.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"name": map[string]interface{}{"type": "string", "description": "Team name, e.g. 'Arsenal'."},
						},
						"required": []string{"name"},
					},
				},
			},
			OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDef{
					Name:        "football_head_to_head",
					Description: "Get recent head-to-head results between two football teams. Use for 'X vs Y history'.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"team1": map[string]interface{}{"type": "string", "description": "First team name."},
							"team2": map[string]interface{}{"type": "string", "description": "Second team name."},
						},
						"required": []string{"team1", "team2"},
					},
				},
			},
		)
	}

	return tools
}

// IsNativeTool reports whether the given function name is handled by the
// gateway (as opposed to a client-supplied passthrough tool).
func IsNativeTool(name string) bool {
	switch name {
	case "web_search",
		"football_live_scores",
		"football_fixtures",
		"football_standings",
		"football_team",
		"football_head_to_head":
		return true
	}
	return false
}

// ExecuteTool dispatches a single tool call to its executor.
func (tc *ToolContext) ExecuteTool(name string, rawArgs string) ToolExecution {
	args := parseToolArgs(rawArgs)
	switch name {
	case "web_search":
		return tc.execWebSearch(args)
	case "football_live_scores":
		return tc.execFootballLiveScores(args)
	case "football_fixtures":
		return tc.execFootballFixtures(args)
	case "football_standings":
		return tc.execFootballStandings(args)
	case "football_team":
		return tc.execFootballTeam(args)
	case "football_head_to_head":
		return tc.execFootballHeadToHead(args)
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
