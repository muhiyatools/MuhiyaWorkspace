package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gateway/db"
)

func TestConditionDeepSeekChatCompletion(t *testing.T) {
	body := map[string]interface{}{
		"model": "deepseek-v4-flash", "messages": []interface{}{
			map[string]interface{}{
				"role": "assistant", "content": "", "reasoning_content": "inspect first",
				"tool_calls": []interface{}{map[string]interface{}{"id": "call-1"}},
			},
		},
		"max_completion_tokens": float64(4096),
		"response_format":       map[string]interface{}{"type": "json_object"},
		"thinking":              map[string]interface{}{"type": "enabled"},
		"temperature":           float64(0.2),
		"top_p":                 float64(0.8),
		"stream":                true,
		"stream_options":        map[string]interface{}{"include_usage": true},
		"seed":                  float64(42),
		"reasoning":             map[string]interface{}{"effort": "high"},
		"output_config":         map[string]interface{}{"effort": "high"},
	}

	conditionDeepSeekChatCompletion(body)

	if body["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want 4096", body["max_tokens"])
	}
	for _, absent := range []string{"max_completion_tokens", "temperature", "top_p", "seed", "reasoning", "output_config"} {
		if _, ok := body[absent]; ok {
			t.Errorf("DeepSeek Chat Completions body retained %q", absent)
		}
	}
	messages := body["messages"].([]interface{})
	assistant := messages[0].(map[string]interface{})
	if assistant["reasoning_content"] != "inspect first" {
		t.Error("assistant reasoning_content was not preserved for tool replay")
	}
	if _, ok := body["response_format"]; !ok {
		t.Error("JSON response_format was removed")
	}
	if _, ok := body["stream_options"]; !ok {
		t.Error("stream usage options were removed")
	}
}

func TestConditionDeepSeekNonThinkingRequest(t *testing.T) {
	body := map[string]interface{}{
		"thinking":       map[string]interface{}{"type": "disabled"},
		"temperature":    float64(0.2),
		"top_p":          float64(0.8),
		"stream_options": map[string]interface{}{"include_usage": true},
	}
	conditionDeepSeekChatCompletion(body)

	if _, ok := body["temperature"]; !ok {
		t.Error("non-thinking temperature was removed")
	}
	if _, ok := body["top_p"]; !ok {
		t.Error("non-thinking top_p was removed")
	}
	if _, ok := body["stream_options"]; ok {
		t.Error("stream_options must be absent when stream is false")
	}
}

func TestDeepSeekAgentTurnReplaysReasoningContent(t *testing.T) {
	upstream := newCaptureUpstream(http.StatusOK, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"inspect first\"},\"finish_reason\":null}]}\n\n"+
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"+
		"data: [DONE]\n\n")
	defer upstream.Close()

	handler := &ProxyHandler{}
	model := &db.Model{TargetModel: "deepseek-v4-flash", SupportsThinking: true}
	provider := &db.Provider{ID: "deepseek", BaseURL: upstream.Server.URL, APIKey: "secret"}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	turn, err := handler.streamOpenAITurn(request, recorder, recorder, "chatcmpl-test", model, provider,
		[]OpenAIMessage{{Role: "user", Content: "inspect"}},
		[]OpenAITool{{Type: "function", Function: OpenAIFunctionDef{Name: "read_file"}}}, ThinkingHigh)
	if err != nil {
		t.Fatal(err)
	}
	if turn.reasoningContent != "inspect first" {
		t.Errorf("reasoning content = %q", turn.reasoningContent)
	}
	if len(turn.toolCalls) != 1 || turn.toolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool calls = %#v", turn.toolCalls)
	}

	captured := upstream.Last()
	if captured.Path != "/chat/completions" {
		t.Errorf("upstream path = %q, want /chat/completions", captured.Path)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal(captured.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["max_tokens"]; ok {
		t.Error("native DeepSeek agent invented a max_tokens limit")
	}
	if _, ok := sent["temperature"]; ok {
		t.Error("native DeepSeek thinking request retained temperature")
	}
	if sent["model"] != "deepseek-v4-flash" {
		t.Errorf("upstream model = %v", sent["model"])
	}
}
