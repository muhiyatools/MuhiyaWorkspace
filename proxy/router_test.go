package proxy

import (
	"strings"
	"testing"

	"gateway/db"
)

// mdl is a terse model builder for routing tests.
func mdl(id, provider, target, tier string, in, out float64, opts ...func(*db.Model)) db.Model {
	m := db.Model{
		ID:                   id,
		Name:                 id,
		ProviderID:           provider,
		TargetModel:          target,
		RoutingTier:          tier,
		Status:               "active",
		InputCostPerMillion:  in,
		OutputCostPerMillion: out,
	}
	for _, o := range opts {
		o(&m)
	}
	return m
}

func vision(m *db.Model)     { m.SupportsVision = true }
func inactive(m *db.Model)   { m.Status = "inactive" }
func transcribe(m *db.Model) { m.Transcribe = true }

// allActive marks every referenced provider active.
func allActive(ids ...string) map[string]bool {
	s := map[string]bool{}
	for _, id := range ids {
		s[id] = true
	}
	return s
}

func TestSelectRoute_PrefersPaidForText(t *testing.T) {
	models := []db.Model{
		mdl("free-gemma", "openrouter", "google/gemma:free", "none", 0, 0, vision),
		mdl("deepseek", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
	}
	got, err := selectRoute(models, allActive("openrouter", "deepseek"), "simple", mediaNeeds{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "deepseek" {
		t.Fatalf("text request should route to the paid model, got %q", got.ID)
	}
}

func TestSelectRoute_VisionKeepsFreeModel(t *testing.T) {
	models := []db.Model{
		mdl("free-gemma", "openrouter", "google/gemma:free", "none", 0, 0, vision),
		mdl("deepseek", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
	}
	got, err := selectRoute(models, allActive("openrouter", "deepseek"), "simple", mediaNeeds{Vision: true}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "free-gemma" {
		t.Fatalf("vision request should reach the free vision model, got %q", got.ID)
	}
}

func TestSelectRoute_VisionNoActiveVisionModel_Diagnostic(t *testing.T) {
	models := []db.Model{
		mdl("deepseek", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
		mdl("gemma", "openrouter", "google/gemma:free", "none", 0, 0, vision, inactive), // vision but inactive
	}
	_, err := selectRoute(models, allActive("deepseek", "openrouter"), "simple", mediaNeeds{Vision: true}, false)
	if err == nil {
		t.Fatal("expected a routing error when no active vision model exists")
	}
	msg := err.Error()
	for _, want := range []string{"vision required", "0 vision-capable", "Admin → Models"} {
		if !strings.Contains(msg, want) {
			t.Errorf("routing error %q missing %q", msg, want)
		}
	}
}

func TestSelectRoute_ExcludesInactiveProvider(t *testing.T) {
	models := []db.Model{
		mdl("deepseek", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
	}
	// Model is active but its provider is not.
	_, err := selectRoute(models, map[string]bool{"deepseek": false}, "simple", mediaNeeds{}, false)
	if err == nil {
		t.Fatal("a model on an inactive provider must not be routable")
	}
	if !strings.Contains(err.Error(), "0 on active providers") {
		t.Errorf("diagnostic should report 0 on active providers, got %q", err.Error())
	}
}

func TestSelectRoute_ThinkingFilterRelaxesAtTier2(t *testing.T) {
	// Requesting NO thinking at 'hard'. The only 'hard' model is a reasoner
	// (thinking-capable), so tier-1 (tier+thinking) finds nothing; tier-2 (tier,
	// ignore thinking) must still pick it rather than dropping to a wrong tier.
	models := []db.Model{
		mdl("reasoner", "deepseek", "deepseek-reasoner", "hard", 0.55, 2.19),
		mdl("chat", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
	}
	got, err := selectRoute(models, allActive("deepseek"), "hard", mediaNeeds{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "reasoner" {
		t.Fatalf("tier-2 should keep the exact-tier model despite thinking mismatch, got %q", got.ID)
	}
}

func TestSelectRoute_CheapestWinsWithinTier(t *testing.T) {
	models := []db.Model{
		mdl("pricey", "p", "big", "simple", 3.0, 6.0),
		mdl("cheap", "p", "small", "simple", 0.1, 0.2),
	}
	got, err := selectRoute(models, allActive("p"), "simple", mediaNeeds{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "cheap" {
		t.Fatalf("cheapest model should win the price sort, got %q", got.ID)
	}
}

func TestFilterFallbacks_ExcludesPrimaryInactiveProviderAndTranscribe(t *testing.T) {
	models := []db.Model{
		mdl("primary", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
		mdl("alt-cheap", "deepseek", "deepseek-lite", "simple", 0.05, 0.10),
		mdl("alt-dead", "deadprov", "x", "simple", 0.01, 0.01),
		mdl("whisper", "openai", "whisper-1", "none", 0, 0, transcribe),
	}
	providerActive := map[string]bool{"deepseek": true, "deadprov": false, "openai": true}
	fb := filterFallbacks(models, providerActive, "primary", mediaNeeds{})

	if len(fb) != 1 {
		t.Fatalf("expected exactly 1 fallback (alt-cheap), got %d: %+v", len(fb), fb)
	}
	if fb[0].ID != "alt-cheap" {
		t.Errorf("expected alt-cheap, got %q", fb[0].ID)
	}
}

func TestModelSupportsThinking_FlagFirst(t *testing.T) {
	// Flag wins even when the target name has no reasoning keyword.
	flagged := &db.Model{TargetModel: "some-fast-model", SupportsThinking: true}
	if !modelSupportsThinking(flagged) {
		t.Error("SupportsThinking flag must make a model thinking-capable regardless of name")
	}
	// Heuristic fallback still works for unflagged reasoning models.
	heuristic := &db.Model{TargetModel: "deepseek-reasoner"}
	if !modelSupportsThinking(heuristic) {
		t.Error("reasoner target should match the heuristic fallback")
	}
	// Plain text model: neither flag nor heuristic.
	plain := &db.Model{TargetModel: "deepseek-chat"}
	if modelSupportsThinking(plain) {
		t.Error("a plain chat model must not be thinking-capable")
	}
}

func TestComputeRoutingDiag_Counts(t *testing.T) {
	models := []db.Model{
		mdl("a", "p1", "x", "simple", 1, 1),                   // active, provider active → eligible
		mdl("b", "p1", "y", "simple", 1, 1, vision),           // active, eligible, vision
		mdl("c", "p2", "z", "simple", 1, 1),                   // active but provider inactive
		mdl("d", "p1", "w", "simple", 1, 1, inactive),         // inactive model
		mdl("e", "p1", "whisper-1", "none", 0, 0, transcribe), // transcribe → not eligible
	}
	d := computeRoutingDiag(models, map[string]bool{"p1": true, "p2": false})
	if d.total != 5 {
		t.Errorf("total = %d, want 5", d.total)
	}
	if d.active != 4 { // a, b, c, e (d is inactive)
		t.Errorf("active = %d, want 4", d.active)
	}
	if d.eligible != 2 { // a, b only (c dead-provider, e transcribe, d inactive)
		t.Errorf("eligible = %d, want 2", d.eligible)
	}
	if d.visionCapable != 1 {
		t.Errorf("visionCapable = %d, want 1", d.visionCapable)
	}
}
