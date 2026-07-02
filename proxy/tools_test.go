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
	// Football key -> the five football tools.
	fb := BuildToolSchemas(ToolSettings{FootballAPIKey: "x"})
	if len(fb) != 5 {
		t.Fatalf("expected 5 football tools, got %d", len(fb))
	}
	for _, tool := range fb {
		if !IsNativeTool(tool.Function.Name) {
			t.Fatalf("schema advertised non-native tool %q", tool.Function.Name)
		}
	}
	// Both -> six tools total.
	if both := BuildToolSchemas(ToolSettings{FootballAPIKey: "x", SerperAPIKey: "y"}); len(both) != 6 {
		t.Fatalf("expected 6 tools with both keys, got %d", len(both))
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

func TestFormatMatchLineLive(t *testing.T) {
	h, a := 2, 1
	var f afFixture
	f.League.Name = "Premier League"
	f.Teams.Home.Name = "Arsenal"
	f.Teams.Away.Name = "Chelsea"
	f.Goals.Home = &h
	f.Goals.Away = &a
	f.Fixture.Status.Short = "2H"
	f.Fixture.Status.Elapsed = 78

	line := formatMatchLine(f)
	if !strings.Contains(line, "Arsenal 2-1 Chelsea") || !strings.Contains(line, "78'") {
		t.Fatalf("unexpected live line: %q", line)
	}
}

func TestFormatMatchLineScheduled(t *testing.T) {
	var f afFixture
	f.League.Name = "La Liga"
	f.Teams.Home.Name = "Real Madrid"
	f.Teams.Away.Name = "Barcelona"
	f.Fixture.Status.Short = "NS" // not started
	f.Fixture.Date = "2026-07-05T19:00:00+00:00"

	line := formatMatchLine(f)
	if !strings.Contains(line, "Real Madrid vs Barcelona") {
		t.Fatalf("expected scheduled line, got %q", line)
	}
	if strings.Contains(line, "-0") || strings.Contains(line, "0-0") {
		t.Fatalf("scheduled match should not show a score: %q", line)
	}
}

func TestFixtureCardShape(t *testing.T) {
	h := 3
	var f afFixture
	f.Fixture.ID = 42
	f.Teams.Home.Name = "Liverpool"
	f.Teams.Away.Name = "Everton"
	f.Goals.Home = &h
	card := fixtureCard(f)
	if card["id"] != 42 {
		t.Fatalf("expected id 42 in card")
	}
	home, ok := card["home"].(map[string]interface{})
	if !ok || home["name"] != "Liverpool" {
		t.Fatalf("expected home team name in card, got %+v", card["home"])
	}
}

func TestCurrentFootballSeasonSane(t *testing.T) {
	s := currentFootballSeason()
	if s < 2020 || s > 2100 {
		t.Fatalf("season out of sane range: %d", s)
	}
}

func TestIsLiveStatus(t *testing.T) {
	for _, s := range []string{"1H", "2H", "HT", "ET"} {
		if !isLiveStatus(s) {
			t.Fatalf("%s should be live", s)
		}
	}
	for _, s := range []string{"FT", "NS", "PST"} {
		if isLiveStatus(s) {
			t.Fatalf("%s should not be live", s)
		}
	}
}
