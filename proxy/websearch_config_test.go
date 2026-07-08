package proxy

import "testing"

// Web search must be available out of the box (via the key-less DuckDuckGo
// fallback) so coding agents like MuhiyaCode see it as enabled, while a paid
// key still takes precedence and an explicit "false" disables it.
func TestWebSearchEnabled(t *testing.T) {
	cases := []struct {
		name string
		s    ToolSettings
		want bool
	}{
		{"ddg fallback only", ToolSettings{DDGFallback: true}, true},
		{"tavily key", ToolSettings{TavilyAPIKey: "tvly-x"}, true},
		{"serper key", ToolSettings{SerperAPIKey: "srp-x"}, true},
		{"all disabled", ToolSettings{DDGFallback: false}, false},
	}
	for _, tc := range cases {
		if got := tc.s.WebSearchEnabled(); got != tc.want {
			t.Errorf("%s: WebSearchEnabled()=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsFalsey(t *testing.T) {
	// Unset/empty must default features ON (not falsey).
	for _, v := range []string{"", "true", "1", "on", "yes", "anything"} {
		if isFalsey(v) {
			t.Errorf("isFalsey(%q) should be false", v)
		}
	}
	for _, v := range []string{"false", "0", "off", "no", "disabled", "FALSE", " Off "} {
		if !isFalsey(v) {
			t.Errorf("isFalsey(%q) should be true", v)
		}
	}
}
