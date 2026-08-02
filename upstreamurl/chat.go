package upstreamurl

import "strings"

const chatCompletionsPath = "/chat/completions"

// ChatCompletions resolves an OpenAI-compatible base URL to the Chat
// Completions endpoint. A mistakenly configured legacy /completions endpoint
// is corrected because this gateway never sends FIM requests on its chat path.
func ChatCompletions(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(trimmed, chatCompletionsPath) {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/completions") {
		return strings.TrimSuffix(trimmed, "/completions") + chatCompletionsPath
	}
	return trimmed + chatCompletionsPath
}
