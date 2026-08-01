package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFeature009DeepSeekCaptureMatchesPinnedBefore(t *testing.T) {
	goldenDir := os.Getenv("DEEPSEEK_GOLDEN_DIR")
	if goldenDir == "" {
		t.Skip("DEEPSEEK_GOLDEN_DIR not set")
	}
	scenarios := []struct {
		name, model, level, applied string
		identity                    bool
	}{
		{"reasoner-max", "deepseek-reasoner", "max", "max", true},
		// Absent effort passes the body through untouched (the deployed
		// pre-009 gateway's behavior): applied is "" and the client's raw
		// reasoning_effort/thinking survive verbatim. Locks in the audit fix
		// for the force-disabled regression.
		{"chat-absent", "deepseek-chat", "", "", true},
		{"reasoner-absent", "deepseek-reasoner", "", "", true},
		{"reasoner-no-identity", "deepseek-reasoner", "medium", "high", false},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			body := feature009DeepSeekBody()
			body["model"] = scenario.model
			delete(body, "web_search")
			applied := ApplyThinkingOpenAI(body, "https://api.deepseek.com", scenario.model, scenario.level, false)
			body["stream_options"] = map[string]interface{}{"include_usage": true}
			sanitizeUpstreamIdentity(body, famDeepseek, "", "")
			if scenario.identity {
				// Pinned output of the server-side HMAC identity fixture captured
				// before feature 009. identity_test.go independently freezes the
				// derivation algorithm and no secret/raw user value enters this file.
				body["user_id"] = "mu-fdf36c947986f08f6560f0cbe762da5190a6b344"
			}
			gotBody, _ := json.Marshal(body)
			assertFeature009Golden(t, filepath.Join(goldenDir, scenario.name+".json"), gotBody)
			meta, _ := json.Marshal(map[string]interface{}{
				"applied_thinking": applied, "authorization": "Bearer <redacted>",
				"content_type": "application/json", "method": "POST", "path": "/chat/completions",
			})
			assertFeature009Golden(t, filepath.Join(goldenDir, scenario.name+".meta.json"), meta)
			if applied != scenario.applied {
				t.Fatalf("applied thinking = %q, want %q", applied, scenario.applied)
			}
		})
	}
}

func feature009DeepSeekBody() map[string]interface{} {
	return map[string]interface{}{
		"model": "client-alias", "stream": true, "web_search": true,
		"reasoning_effort": "raw", "thinking": map[string]interface{}{"type": "raw"},
		"frequency_penalty": 0.5, "presence_penalty": 0.5, "user": "raw", "user_id": "raw",
		"messages": []interface{}{
			map[string]interface{}{"content": "Inspect the workspace.", "role": "user"},
			map[string]interface{}{"content": "I will inspect it.", "role": "assistant"},
			map[string]interface{}{"content": "", "reasoning_content": "", "role": "assistant", "tool_calls": []interface{}{map[string]interface{}{"function": map[string]interface{}{"arguments": `{"path":"README.md"}`, "name": "read_file"}, "id": "call-1", "type": "function"}}},
			map[string]interface{}{"content": "workspace contents", "name": "read_file", "role": "tool", "tool_call_id": "call-1"},
		},
		"tool_choice": "auto",
		"tools":       []interface{}{map[string]interface{}{"function": map[string]interface{}{"description": "Read a workspace file.", "name": "read_file", "parameters": map[string]interface{}{"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []interface{}{"path"}, "type": "object"}}, "type": "function"}},
	}
}

func assertFeature009Golden(t *testing.T, path string, got []byte) {
	t.Helper()
	// DEEPSEEK_GOLDEN_UPDATE=1 rewrites the goldens instead of comparing —
	// use ONLY when a behavior change is intentional and reviewed; the diff
	// of the golden files is the reviewable record of the wire change.
	if os.Getenv("DEEPSEEK_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for len(want) > 0 && (want[len(want)-1] == '\n' || want[len(want)-1] == '\r') {
		want = want[:len(want)-1]
	}
	if string(got) != string(want) {
		t.Fatalf("byte diff for %s\ngot:  %s\nwant: %s", filepath.Base(path), got, want)
	}
}
