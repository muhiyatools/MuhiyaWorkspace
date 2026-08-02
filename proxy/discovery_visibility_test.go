package proxy

import (
	"net/http"
	"testing"

	"gateway/db"
)

// discoverableModel is the single decision the three /v1/models branches share.
// These tests pin the MuhiyaCode discoverability semantics without a database.

func TestDiscoverableModelVisibilityMatrix(t *testing.T) {
	cases := []struct {
		name                  string
		model                 db.Model
		onlyMuhiyaCodeVisible bool
		want                  bool
	}{
		{"active visible, muhiyacode caller", db.Model{Status: "active", MuhiyaCodeVisible: true}, true, true},
		{"active hidden, muhiyacode caller", db.Model{Status: "active", MuhiyaCodeVisible: false}, true, false},
		{"active hidden, other caller", db.Model{Status: "active", MuhiyaCodeVisible: false}, false, true},
		{"active visible, other caller", db.Model{Status: "active", MuhiyaCodeVisible: true}, false, true},
		{"inactive visible, muhiyacode caller", db.Model{Status: "inactive", MuhiyaCodeVisible: true}, true, false},
		{"inactive visible, other caller", db.Model{Status: "inactive", MuhiyaCodeVisible: true}, false, false},
		{"transcribe visible, muhiyacode caller", db.Model{Status: "active", Transcribe: true, MuhiyaCodeVisible: true}, true, false},
		{"transcribe, other caller", db.Model{Status: "active", Transcribe: true, MuhiyaCodeVisible: true}, false, false},
		// The deprecated router virtual model must never be discoverable by
		// ANY caller — automatic model selection is no longer part of the
		// gateway contract (027) — even if its row is active and visible.
		// Before this, /v1/muhiyacode/models excluded it by an explicit name
		// check that /v1/models' list branches never applied, so the two
		// endpoints could disagree about it for a MuhiyaCode caller.
		{"router model, muhiyacode caller", db.Model{Name: routerModelDeprecated, Status: "active", MuhiyaCodeVisible: true}, true, false},
		{"router model, other caller", db.Model{Name: routerModelDeprecated, Status: "active", MuhiyaCodeVisible: true}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model
			if got := discoverableModel(&m, tc.onlyMuhiyaCodeVisible); got != tc.want {
				t.Fatalf("discoverableModel(%+v, only=%v) = %v, want %v", tc.model, tc.onlyMuhiyaCodeVisible, got, tc.want)
			}
		})
	}
}

func TestDiscoverableModelNilSafe(t *testing.T) {
	if discoverableModel(nil, false) || discoverableModel(nil, true) {
		t.Fatal("nil model must never be discoverable")
	}
}

// The X-Client-App header is the contract that ties the agent's requests to the
// MuhiyaCode filter. Pin that the exact string the agent sends
// (internal/gateway/provider.go sets "MuhiyaCode") is what getClientAppName
// returns, so the discovery gate keys off it correctly.
func TestGetClientAppNameMuhiyaCodeHeader(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("X-Client-App", "MuhiyaCode")
	if got := getClientAppName(r); got != "MuhiyaCode" {
		t.Fatalf("getClientAppName = %q, want MuhiyaCode", got)
	}
	// A MuhiyaChat caller must never resolve to MuhiyaCode: the two apps have
	// independent visibility columns, and crossing them would let one app's
	// picker be silently governed by the other app's flag.
	r2, _ := http.NewRequest(http.MethodGet, "/v1/models", nil)
	r2.Header.Set("X-Client-App", "MuhiyaChat")
	if got := getClientAppName(r2); got != "MuhiyaChat" {
		t.Fatalf("getClientAppName = %q, want MuhiyaChat", got)
	}
}

// The two per-app visibility flags must be genuinely independent: hiding a
// model from one app must not hide it from the other, and neither may leak into
// the catalog every other caller sees. A shared/either-or gate here would mean
// an operator unchecking "MuhiyaCode Discoverable" silently removed the model
// from the chat picker too.
func TestPerAppVisibilityFlagsAreIndependent(t *testing.T) {
	codeOnly := db.Model{Status: "active", MuhiyaCodeVisible: true, MuhiyaChatVisible: false}
	chatOnly := db.Model{Status: "active", MuhiyaCodeVisible: false, MuhiyaChatVisible: true}
	neither := db.Model{Status: "active"}

	cases := []struct {
		name       string
		model      db.Model
		visibility appVisibility
		want       bool
	}{
		{"code-only model, code caller", codeOnly, visibilityMuhiyaCode, true},
		{"code-only model, chat caller", codeOnly, visibilityMuhiyaChat, false},
		{"chat-only model, chat caller", chatOnly, visibilityMuhiyaChat, true},
		{"chat-only model, code caller", chatOnly, visibilityMuhiyaCode, false},
		// Neither flag set: hidden from both pickers, still listed for every
		// other caller (the platform and third-party SDKs are never gated).
		{"unflagged model, code caller", neither, visibilityMuhiyaCode, false},
		{"unflagged model, chat caller", neither, visibilityMuhiyaChat, false},
		{"unflagged model, other caller", neither, visibilityAny, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model
			if got := discoverableModelFor(&m, tc.visibility); got != tc.want {
				t.Fatalf("discoverableModelFor(%+v, %v) = %v, want %v", tc.model, tc.visibility, got, tc.want)
			}
		})
	}

	// The header is what selects the column; pin that mapping too.
	if appVisibilityFor("MuhiyaChat") != visibilityMuhiyaChat {
		t.Error("MuhiyaChat header must select the muhiyachat_visible column")
	}
	if appVisibilityFor("MuhiyaCode") != visibilityMuhiyaCode {
		t.Error("MuhiyaCode header must select the muhiyacode_visible column")
	}
	if appVisibilityFor("") != visibilityAny || appVisibilityFor("SomeSDK") != visibilityAny {
		t.Error("a non first-party caller must not be gated by either flag")
	}
}
