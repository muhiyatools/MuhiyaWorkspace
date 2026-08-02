package proxy

import "testing"

// TestDeepSeekThinkingNormalizationAndStripList is the regression matrix for
// the DeepSeek upstream body conditioning (thinking.go ApplyThinkingOpenAI +
// handler.go sanitizeUpstreamIdentity). It sweeps {effort present, explicit
// off, effort absent} x {reasoner, non-reasoner} and asserts, for every cell:
//
//   - When a level IS resolved, no raw client reasoning_effort survives
//     verbatim and thinking is emitted only in DeepSeek's documented form
//     ({"type":"enabled"|"disabled"}) with reasoning_effort in {high, max},
//     ONLY for the reasoning-capable model.
//   - When NO level is resolved the body passes through UNTOUCHED — the
//     deployed pre-009 behavior: a bare reasoner request keeps DeepSeek's own
//     default (reasoning ON) and the forwarded bytes match the
//     pre-normalization gateway exactly (audit fix: unset effort must never
//     force thinking:{type:"disabled"}).
//   - The DeepSeek strip-list fields (frequency_penalty, presence_penalty,
//     user) and any caller-supplied identity are always removed by
//     sanitizeUpstreamIdentity, and a server-derived user_id is stamped when
//     an identity secret is configured.
func TestDeepSeekThinkingNormalizationAndStripList(t *testing.T) {
	const baseURL = "https://api.deepseek.com"

	cases := []struct {
		name         string
		model        string
		level        string // resolved gateway level ("" = client expressed nothing)
		wantApplied  string
		wantThinking string // expected thinking.type, "" = no thinking object
		wantEffort   string // expected reasoning_effort, "" = must be absent
	}{
		{"reasoner+max", "deepseek-reasoner", ThinkingMax, "max", "enabled", "max"},
		// high stays high. This row said "max" until 2026-08-02 — an escalation
		// inherited from the reasoner-era high|max ladder, which turned every
		// High-effort client request into DeepSeek's longest reasoning mode.
		{"reasoner+high", "deepseek-reasoner", ThinkingHigh, "high", "enabled", "high"},
		{"reasoner+medium", "deepseek-reasoner", ThinkingMedium, "high", "enabled", "high"},
		// deepseek-reasoner is not v4-flash, so low rides the high floor.
		{"reasoner+low", "deepseek-reasoner", ThinkingLow, "high", "enabled", "high"},
		// ONLY an explicit none/off/minimal preference disables thinking.
		{"reasoner+explicit-off", "deepseek-reasoner", ThinkingMinimal, "disabled", "disabled", ""},
		// Effort absent: untouched passthrough — the raw client fields survive
		// exactly as sent (the client speaks DeepSeek's dialect at its own
		// risk) and DeepSeek's reasoner default stays ON.
		{"reasoner+absent", "deepseek-reasoner", "", "", "whatever", "sneaky"},
		// deepseek-chat has no reasoning mode: with a level, everything is
		// stripped and nothing injected; with no level, untouched passthrough.
		{"chat+max", "deepseek-chat", ThinkingMax, "unsupported", "", ""},
		{"chat+explicit-off", "deepseek-chat", ThinkingMinimal, "unsupported", "", ""},
		{"chat+absent", "deepseek-chat", "", "", "whatever", "sneaky"},
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

			applied := ApplyThinkingOpenAI(body, baseURL, tc.model, tc.level, false)
			if applied != tc.wantApplied {
				t.Fatalf("applied = %q, want %q", applied, tc.wantApplied)
			}

			// With a resolved level the raw client value must never survive;
			// with NO level the body must pass through untouched.
			if tc.level != "" && body["reasoning_effort"] == "sneaky" {
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
					t.Fatalf("thinking not an object: %v", body["thinking"])
				}
				if thinking["type"] != tc.wantThinking {
					t.Errorf("thinking.type = %v, want %q", thinking["type"], tc.wantThinking)
				}
			}

			// Now condition identity the same way the handler does before send.
			sanitizeUpstreamIdentity(body, famDeepseek, "identity-secret", "caller-99")

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
	sanitizeUpstreamIdentity(body, famDeepseek, "", "caller-1")
	for _, k := range []string{"frequency_penalty", "presence_penalty", "user", "user_id"} {
		if _, has := body[k]; has {
			t.Errorf("%s must be absent when identity injection is disabled, got %v", k, body[k])
		}
	}
}

// TestSanitizeUpstreamIdentityKeepsPenaltiesForNonDeepSeek locks in the audit
// fix for the over-broad strip: frequency_penalty/presence_penalty are a
// DeepSeek-only removal (DeepSeek rejects them), while GLM/OpenAI/other
// upstreams support them legitimately and must receive them untouched.
// Identity conditioning (user/user_id) remains global for every upstream.
func TestSanitizeUpstreamIdentityKeepsPenaltiesForNonDeepSeek(t *testing.T) {
	for _, family := range []upstreamFamily{famGLM, famOpenAI, famMiniMax, famUnknown} {
		body := map[string]interface{}{
			"frequency_penalty": 0.5,
			"presence_penalty":  0.3,
			"user":              "client-supplied",
			"user_id":           "forged",
		}
		sanitizeUpstreamIdentity(body, family, "identity-secret", "caller-7")
		if body["frequency_penalty"] != 0.5 || body["presence_penalty"] != 0.3 {
			t.Errorf("family %d: sampling penalties must survive for non-DeepSeek upstreams, got %v/%v",
				family, body["frequency_penalty"], body["presence_penalty"])
		}
		for _, k := range []string{"user"} {
			if _, has := body[k]; has {
				t.Errorf("family %d: %s must be stripped for every upstream", family, k)
			}
		}
		if got, _ := body["user_id"].(string); got != DeriveUserID("identity-secret", "caller-7") {
			t.Errorf("family %d: user_id = %q, want the server-derived value", family, got)
		}
	}
}
