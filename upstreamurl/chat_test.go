package upstreamurl

import "testing"

func TestChatCompletions(t *testing.T) {
	tests := map[string]string{
		"https://api.deepseek.com":                      "https://api.deepseek.com/chat/completions",
		"https://api.deepseek.com/":                     "https://api.deepseek.com/chat/completions",
		"https://api.deepseek.com/v1":                   "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/chat/completions":     "https://api.deepseek.com/chat/completions",
		"https://api.deepseek.com/v1/chat/completions/": "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/completions":          "https://api.deepseek.com/chat/completions",
	}
	for baseURL, want := range tests {
		if got := ChatCompletions(baseURL); got != want {
			t.Errorf("ChatCompletions(%q) = %q, want %q", baseURL, got, want)
		}
	}
}
