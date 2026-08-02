package proxy

// conditionDeepSeekChatCompletion removes unsupported OpenAI fields and
// normalizes the output-limit alias DeepSeek's Chat Completions API lacks.
func conditionDeepSeekChatCompletion(body map[string]interface{}) {
	if limit, ok := body["max_completion_tokens"]; ok {
		if _, hasMaxTokens := body["max_tokens"]; !hasMaxTokens {
			body["max_tokens"] = limit
		}
		delete(body, "max_completion_tokens")
	}
	for _, field := range deepSeekUnsupportedFields {
		delete(body, field)
	}
	if !deepSeekThinkingDisabled(body) {
		delete(body, "temperature")
		delete(body, "top_p")
	}
	if stream, _ := body["stream"].(bool); !stream {
		delete(body, "stream_options")
	}
}

var deepSeekUnsupportedFields = []string{
	"function_call", "functions", "include", "input", "instructions",
	"logit_bias", "max_output_tokens", "metadata", "modalities", "n",
	"output_config", "parallel_tool_calls", "previous_response_id", "reasoning",
	"seed", "service_tier", "store", "truncation",
}

func deepSeekThinkingDisabled(body map[string]interface{}) bool {
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok {
		return false
	}
	typeName, _ := thinking["type"].(string)
	return typeName == "disabled"
}
