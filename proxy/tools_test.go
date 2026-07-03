package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolArgs(t *testing.T) {
	got := parseToolArgs(`{"query":"messi goals","recency":"week"}`)
	if got["query"] != "messi goals" {
		t.Fatalf("expected query parsed, got %v", got["query"])
	}
	// Malformed / empty must not panic and must return an empty map.
	if len(parseToolArgs("")) != 0 {
		t.Fatalf("expected empty map for empty args")
	}
	if len(parseToolArgs("not json")) != 0 {
		t.Fatalf("expected empty map for malformed args")
	}
}

func TestArgHelpers(t *testing.T) {
	args := map[string]interface{}{"season": float64(2025), "league": "  La Liga  "}
	if v, ok := argInt(args, "season"); !ok || v != 2025 {
		t.Fatalf("argInt failed: %v %v", v, ok)
	}
	if s := argString(args, "league"); s != "La Liga" {
		t.Fatalf("argString should trim, got %q", s)
	}
}

func TestBuildToolSchemasGating(t *testing.T) {
	// No keys -> no tools.
	if got := BuildToolSchemas(ToolSettings{}); len(got) != 0 {
		t.Fatalf("expected 0 tools with no keys, got %d", len(got))
	}
	// Only search key -> only web_search.
	search := BuildToolSchemas(ToolSettings{SerperAPIKey: "x"})
	if len(search) != 1 || search[0].Function.Name != "web_search" {
		t.Fatalf("expected only web_search, got %+v", search)
	}
}

func TestToolsEnabled(t *testing.T) {
	if (ToolSettings{}).ToolsEnabled() {
		t.Fatalf("no keys should mean tools disabled")
	}
	if !(ToolSettings{SerperAPIKey: "x"}).ToolsEnabled() {
		t.Fatalf("a search key should enable tools")
	}
}

func TestParseSerperOrganic(t *testing.T) {
	raw := `{
		"answerBox": {"answer": "2-1", "snippet": "Arsenal beat Chelsea"},
		"organic": [
			{"title": "BBC Sport", "link": "https://bbc.co.uk/a", "snippet": "Report", "date": "1 Jul 2026"},
			{"title": "No Link", "link": "", "snippet": "skip me"},
			{"title": "Sky", "link": "https://sky.com/b", "snippet": "Analysis"}
		]
	}`
	var sr serperResponse
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	sources := parseSerperOrganic(sr)
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources (empty link skipped), got %d", len(sources))
	}
	if sources[0].Index != 1 || sources[1].Index != 2 {
		t.Fatalf("indices should be sequential, got %d,%d", sources[0].Index, sources[1].Index)
	}
	if !strings.Contains(sources[0].Snippet, "1 Jul 2026") {
		t.Fatalf("expected date prefixed in snippet, got %q", sources[0].Snippet)
	}
}
