package proxy

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// P0-W6 (UMI-26): Freeze the current MiniMax and DeepSeek request/response wire
// shapes so a future adapter refactor cannot silently change the bytes the
// provider's prefix cache keys on. The test builds the canonical request body
// each provider would receive today, compares it byte-for-byte to a checked-in
// golden fixture, and fails loudly on any drift.
//
// Run with `-update-goldens` to regenerate the fixtures after an intentional
// wire change. The diff of the golden files is the reviewable record.

var updateWireGoldens = flag.Bool("update-goldens", false, "regenerate the MiniMax/DeepSeek wire goldens")

// wireGoldenTestdataDir returns the testdata directory for the wire goldens.
// Pinned at proxy/testdata/ so the fixtures live next to the proxy package.
func wireGoldenTestdataDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata")
}

// TestMiniMaxWireRequestIsFrozen pins the exact JSON body the gateway sends
// upstream for a canonical MiniMax-M3 request (always-on thinking, 4-message
// conversation, one tool). DeepSeek's prefix cache keys on these bytes; a
// non-reviewable drift here silently invalidates the cache.
func TestMiniMaxWireRequestIsFrozen(t *testing.T) {
	body := miniMaxCanonicalBody()
	ApplyThinkingOpenAI(body, "https://api.minimax.io/v1", "MiniMax-M3", "max", false)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	assertWireGolden(t, filepath.Join(wireGoldenTestdataDir(t), "minimax_wire_request.golden.json"), encoded)
}

// TestDeepSeekWireRequestIsFrozen pins the exact JSON body the gateway sends
// upstream for a canonical DeepSeek request (high thinking, 4-message
// conversation, one tool, identity-sanitized). The transform is the
// byte-affecting pipeline documented in cache_regression_test.go.
func TestDeepSeekWireRequestIsFrozen(t *testing.T) {
	body := feature009DeepSeekBody()
	encoded := deepSeekTransform(t, cloneBody(t, body), "high")
	assertWireGolden(t, filepath.Join(wireGoldenTestdataDir(t), "deepseek_wire_request.golden.json"), encoded)
}

func miniMaxCanonicalBody() map[string]interface{} {
	return map[string]interface{}{
		"model": "MiniMax-M3", "stream": true,
		"reasoning_effort": "max",
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": "stable canonical prefix"},
			map[string]interface{}{"role": "user", "content": "inspect a file"},
			map[string]interface{}{"role": "assistant", "content": "", "tool_calls": []interface{}{map[string]interface{}{"id": "call-0", "type": "function", "function": map[string]interface{}{"name": "read_file", "arguments": `{"path":"README.md"}`}}}},
			map[string]interface{}{"role": "tool", "tool_call_id": "call-0", "content": "file contents"},
		},
		"tools": []interface{}{map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "read_file", "description": "Read a file", "parameters": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []interface{}{"path"}}}}},
	}
}

func assertWireGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateWireGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("wire golden %s is missing; run with -update-goldens to create it", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Fatalf("wire golden %s drifted:\n want=%s\n  got=%s\nrun with -update-goldens if this change is intentional",
			path, want, got)
	}
}
