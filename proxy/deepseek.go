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
	stripUnsupportedDeepSeekResponseFormat(body)
	if !deepSeekThinkingDisabled(body) {
		delete(body, "temperature")
		delete(body, "top_p")
	}
	if stream, _ := body["stream"].(bool); !stream {
		delete(body, "stream_options")
	}
}

// stripUnsupportedDeepSeekResponseFormat removes response_format values that
// DeepSeek does not accept. DeepSeek supports only {"type":"json_object"} and
// {"type":"text"}; OpenAI's Structured-Output type "json_schema" (and any
// other future types) cause an invalid_request_error and must be stripped so
// the request degrades gracefully rather than failing outright.
func stripUnsupportedDeepSeekResponseFormat(body map[string]interface{}) {
	rf, ok := body["response_format"].(map[string]interface{})
	if !ok {
		return
	}
	typeName, _ := rf["type"].(string)
	switch typeName {
	case "json_object", "text":
		// Supported by DeepSeek — keep as-is.
	default:
		// Unsupported type (e.g. "json_schema") — remove to avoid upstream
		// rejection. The model will still follow any JSON instructions in the
		// prompt; the caller loses strict schema enforcement but the request
		// succeeds.
		delete(body, "response_format")
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
