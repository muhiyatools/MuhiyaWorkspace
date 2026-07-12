package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gateway/db"
)

// TestMetaChunkAllowlist (003 T004) verifies MuhiyaCode and MuhiyaChat receive
// the muhiya_log cost chunk (with usage_estimated), and other client apps do not.
func TestMetaChunkAllowlist(t *testing.T) {
	cases := []struct {
		app  string
		want bool
	}{
		{"MuhiyaCode", true},
		{"MuhiyaChat", true},
		{"SomeOtherApp", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.app, func(t *testing.T) {
			rec := httptest.NewRecorder()
			log := &db.RequestLog{ID: "log-1", ClientApp: tc.app, InputTokens: 10, OutputTokens: 5, Cost: 0.0242, UsageEstimated: true}
			sendMuhiyaMetaChunk(rec, log, "test-model")
			body := rec.Body.String()
			got := strings.Contains(body, "muhiya_log")
			if got != tc.want {
				t.Fatalf("app %q: chunk emitted = %v, want %v (body=%q)", tc.app, got, tc.want, body)
			}
			if tc.want {
				if !strings.Contains(body, `"usage_estimated":true`) {
					t.Fatalf("app %q: chunk missing usage_estimated: %q", tc.app, body)
				}
				if !strings.Contains(body, `"cost":0.0242`) {
					t.Fatalf("app %q: chunk missing cost: %q", tc.app, body)
				}
			}
		})
	}
}
