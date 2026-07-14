package proxy

import "testing"

// TestDeepSeekThinkingNormalizationAndStripList is the feature-007 regression
// matrix for the DeepSeek upstream body conditioning (thinking.go
// ApplyThinkingOpenAI + handler.go sanitizeUpstreamIdentity). It sweeps
// {effort present, effort absent} x {reasoner, non-reasoner} and asserts, for
// every cell, that:
//
//   - NO raw client reasoning_effort survives verbatim (the client sends the
//     undocumented "sneaky" value; it must never reach DeepSeek).
//   - thinking is emitted only in DeepSeek's documented form
//     ({"type":"enabled"|"disabled"}) with reasoning_effort in {high, max}, and
//     ONLY for the reasoning-capable model.
//   - the strip-list fields (frequency_penalty, presence_penalty, user) and any
//     caller-supplied identity are always removed, and a server-derived user_id
//     is stamped when an identity secret is configured.
func TestDeepSeekThinkingNormalizationAndStripList(t *testing.T) {
	const baseURL = "https://api.deepseek.com"

	cases := []struct {
		name          string
		model         string
		level         string // resolved gateway level ("" = client expressed nothing)
		wantApplied   string
		wantThinking  string // expected thinking.type, "" = no thinking object
		wantEffort    string // expected reasoning_effort, "" = must be absent
	}{
		{"reasoner+max", "deepseek-reasoner", ThinkingMax, "max", "enabled", "max"},
		{"reasoner+high", "deepseek-reasoner", ThinkingHigh, "max", "enabled", "max"},
		{"reasoner+medium", "deepseek-reasoner", ThinkingMedium, "high", "enabled", "high"},
		{"reasoner+low", "deepseek-reasoner", ThinkingLow, "high", "enabled", "high"},
		// Effort absent on the reasoner: thinking is turned OFF in the documented
		// form rather than leaking the raw client value, and NO reasoning_effort.
		{"reasoner+absent", "deepseek-reasoner", "", "disabled", "disabled", ""},
		// deepseek-chat has no reasoning mode: nothing injected, everything stripped.
		{"chat+max", "deepseek-chat", ThinkingMax, "unsupported", "", ""},
		{"chat+absent", "deepseek-chat", "", "unsupported", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A hostile/naive client body: undocumented reasoning_effort, an
			// arbitrary thinking object, sampling knobs DeepSeek rejects, and
			// caller-supplied identity fields it must never trust.
			body := map[string]interface{}{
				"model":             tc.model,
				"reasoning_effort":  "sneaky",
				"thinking":          map[string]interface{}{"type": "whatever"},
				"frequency_penalty": 0.5,
				"presence_penalty":  0.3,
				"user":              "client-supplied",
				"user_id":           "forged-id",
			}

			applied := ApplyThinkingOpenAI(body, baseURL, tc.model, tc.level)
			if applied != tc.wantApplied {
				t.Fatalf("applied = %q, want %q", applied, tc.wantApplied)
			}

			// The raw client value must never survive under any cell.
			if body["reasoning_effort"] == "sneaky" {
				t.Fatal("raw client reasoning_effort leaked to the DeepSeek body")
			}

			switch tc.wantEffort {
			case "":
				if _, has := body["reasoning_effort"]; has {
					t.Errorf("reasoning_effort must be absent, got %v", body["reasoning_effort"])
				}
			default:
				if body["reasoning_effort"] != tc.wantEffort {
					t.Errorf("reasoning_effort = %v, want %q", body["reasoning_effort"], tc.wantEffort)
				}
			}

			switch tc.wantThinking {
			case "":
				if _, has := body["thinking"]; has {
					t.Errorf("thinking must be absent for %s, got %v", tc.model, body["thinking"])
				}
			default:
				thinking, ok := body["thinking"].(map[string]interface{})
				if !ok {
					t.Fatalf("thinking not a documented object: %v", body["thinking"])
				}
				if thinking["type"] != tc.wantThinking {
					t.Errorf("thinking.type = %v, want %q", thinking["type"], tc.wantThinking)
				}
			}

			// Now condition identity the same way the handler does before send.
			sanitizeUpstreamIdentity(body, "identity-secret", "caller-99")

			for _, stripped := range []string{"frequency_penalty", "presence_penalty", "user"} {
				if _, has := body[stripped]; has {
					t.Errorf("%s must be stripped for DeepSeek, got %v", stripped, body[stripped])
				}
			}
			// The forged caller user_id is replaced with the server-derived value.
			got, _ := body["user_id"].(string)
			if got == "forged-id" {
				t.Fatal("caller-forged user_id survived sanitization")
			}
			if want := DeriveUserID("identity-secret", "caller-99"); got != want {
				t.Errorf("user_id = %q, want derived %q", got, want)
			}
		})
	}
}

// TestSanitizeUpstreamIdentityNoSecretDropsIdentity verifies that with identity
// injection disabled (empty secret) the caller's own user/user_id are still
// stripped and NO user_id is invented - the body simply carries none.
func TestSanitizeUpstreamIdentityNoSecretDropsIdentity(t *testing.T) {
	body := map[string]interface{}{
		"frequency_penalty": 1.0,
		"presence_penalty":  1.0,
		"user":              "u",
		"user_id":           "forged",
	}
	sanitizeUpstreamIdentity(body, "", "caller-1")
	for _, k := range []string{"frequency_penalty", "presence_penalty", "user", "user_id"} {
		if _, has := body[k]; has {
			t.Errorf("%s must be absent when identity injection is disabled, got %v", k, body[k])
		}
	}
}
