package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type simulatedMiniMaxObservation struct {
	CallID, Arguments  string
	Prompt, Completion int
	Cached             int
	UsageReported      bool
}

// TestMiniMaxConformanceSimulated exercises 15 complete OpenAI-dialect
// requests against the authored fixture. No live MiniMax traffic is possible.
func TestMiniMaxConformanceSimulated(t *testing.T) {
	fixture := newSimulatedMiniMaxFixture(t)
	var observations []simulatedMiniMaxObservation
	for index := 0; index < 15; index++ {
		probe := index
		if index >= 13 {
			probe = 13 // final pair is byte-identical: cold then warm prefix probe
		}
		body := simulatedMiniMaxRequestBody(t, probe)
		observation := postSimulatedMiniMax(t, fixture.Server.URL, body)
		observations = append(observations, observation)
		if observation.CallID == "" || observation.Arguments == "" {
			t.Fatalf("request %d lost tool-call identity/arguments: %+v", index+1, observation)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(observation.Arguments), &decoded); err != nil {
			t.Fatalf("request %d arguments not preserved as JSON string: %v", index+1, err)
		}
		if !observation.UsageReported || observation.Prompt == 0 || observation.Completion == 0 {
			t.Fatalf("request %d missing usage: %+v", index+1, observation)
		}
	}
	requests, rejections := fixture.stats()
	if requests != 15 || rejections != 0 {
		t.Fatalf("SIMULATED conformance requests=%d rejections=%d", requests, rejections)
	}
	warm := observations[len(observations)-1]
	if float64(warm.Cached)/float64(warm.Prompt) < 0.50 {
		t.Fatalf("SIMULATED warm cached share = %.1f%%, want >=50%%", 100*float64(warm.Cached)/float64(warm.Prompt))
	}
}

func simulatedMiniMaxRequestBody(t *testing.T, index int) []byte {
	t.Helper()
	callID := fmt.Sprintf("prior-call-%02d", index)
	arguments := fmt.Sprintf(`{"path":"prior-%02d.go"}`, index)
	body := map[string]interface{}{
		"model": "MiniMax-M3", "stream": true,
		"reasoning_effort": "max", "thinking": map[string]interface{}{"type": "enabled"}, "reasoning_split": false,
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": strings.Repeat("stable simulated prefix ", 40)},
			map[string]interface{}{"role": "user", "content": fmt.Sprintf("inspect fixture %02d", index)},
			map[string]interface{}{"role": "assistant", "content": "", "tool_calls": []interface{}{map[string]interface{}{"id": callID, "type": "function", "function": map[string]interface{}{"name": "read_file", "arguments": arguments}}}},
			map[string]interface{}{"role": "tool", "tool_call_id": callID, "content": fmt.Sprintf("simulated result %02d", index)},
		},
		"tools": []interface{}{map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "read_file", "description": "Read one file", "parameters": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []interface{}{"path"}}}}},
	}
	if applied := ApplyThinkingOpenAI(body, "https://api.minimax.io/v1", "MiniMax-M3", "max"); applied != "always-on" {
		t.Fatalf("MiniMax reasoning applied = %q", applied)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func postSimulatedMiniMax(t *testing.T, baseURL string, body []byte) simulatedMiniMaxObservation {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer simulated-fixture-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SIMULATED MiniMax status = %d", resp.StatusCode)
	}
	var observation simulatedMiniMaxObservation
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") || strings.HasSuffix(line, "[DONE]") {
			continue
		}
		var frame struct {
			Choices []struct {
				Delta struct {
					ToolCalls []OpenAIToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *OpenAIUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame); err != nil {
			t.Fatal(err)
		}
		if len(frame.Choices) > 0 && len(frame.Choices[0].Delta.ToolCalls) > 0 {
			call := frame.Choices[0].Delta.ToolCalls[0]
			observation.CallID = call.ID
			observation.Arguments = call.Function.Arguments
		}
		if frame.Usage != nil {
			observation.UsageReported = true
			observation.Prompt = frame.Usage.PromptTokens
			observation.Completion = frame.Usage.CompletionTokens
			observation.Cached = frame.Usage.CacheReadTokensFor("https://api.minimax.io/v1", "MiniMax-M3")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return observation
}
