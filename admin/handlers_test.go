package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gateway/db"
)

// TestValidationHelpers pins the input predicates that back the /api/users and
// /api/keys hardening: the accepted forms must pass and every stored-XSS shape
// must be rejected.
func TestValidationHelpers(t *testing.T) {
	// IDs are opaque slugs; the admin panel drops them into an onclick JS-string
	// context, so anything outside the slug charset must be refused.
	if !validID("user-1a2b3c4d") || !validID("plan-dev") || !validID("topup_9zx") {
		t.Fatal("legitimate slug IDs must pass validID")
	}
	for _, bad := range []string{"", "x'); alert(1); //", "id with space", "a/b", strings.Repeat("a", 65)} {
		if validID(bad) {
			t.Fatalf("validID must reject %q", bad)
		}
	}

	if !validName("Alice Smith") || !validName("O'Brien-Núñez") {
		t.Fatal("legitimate names must pass validName")
	}
	for _, bad := range []string{"", "<img src=x onerror=alert(1)>", "a<b", "line\nbreak", "null\x00byte"} {
		if validName(bad) {
			t.Fatalf("validName must reject %q", bad)
		}
	}

	if !validEmail("alice@company.com") || !validEmail("a.b+tag@sub.example.co.uk") {
		t.Fatal("legitimate emails must pass validEmail")
	}
	for _, bad := range []string{"", "notanemail", "a@b.c<script>", "Alice <a@b.c>", "spaced @b.c"} {
		if validEmail(bad) {
			t.Fatalf("validEmail must reject %q", bad)
		}
	}

	if !validStatus("active", "active", "suspended") || !validStatus("revoked", "active", "revoked") {
		t.Fatal("allowed statuses must pass validStatus")
	}
	if validStatus("hacked", "active", "suspended") {
		t.Fatal("validStatus must reject an out-of-enum value")
	}
}

// TestHandleUsersRejectsMaliciousInput drives the POST handler with the payloads
// a stored-XSS -> admin-privesc attacker would send. Every row must be refused
// at 400 before any DB call, so the handler runs with a nil db (a write would
// nil-panic; a clean 400 proves validation short-circuited first).
func TestHandleUsersRejectsMaliciousInput(t *testing.T) {
	api := &AdminAPI{db: nil}

	cases := []struct {
		name string
		body string
	}{
		{"script name", `{"name":"<img src=x onerror=alert(1)>","email":"a@b.c","plan_id":"plan-dev"}`},
		{"control char name", `{"name":"a\u0000b","email":"a@b.c","plan_id":"plan-dev"}`},
		{"angle bracket email", `{"name":"Alice","email":"a@b.c<script>","plan_id":"plan-dev"}`},
		{"malformed email", `{"name":"Alice","email":"notanemail","plan_id":"plan-dev"}`},
		{"js-context id", `{"id":"x'); alert(document.cookie); //","name":"Alice","email":"a@b.c","plan_id":"plan-dev"}`},
		{"bad status", `{"name":"Alice","email":"a@b.c","plan_id":"plan-dev","status":"evil"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			api.handleUsers(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (body should have been rejected pre-DB)", rec.Code)
			}
		})
	}
}

// TestHandleKeysRejectsMaliciousInput mirrors the above for the key handler.
func TestHandleKeysRejectsMaliciousInput(t *testing.T) {
	api := &AdminAPI{db: nil}

	cases := []struct {
		name string
		body string
	}{
		{"script name", `{"name":"<b>x</b>","user_id":"user-1a2b3c4d"}`},
		{"bad user id", `{"name":"Dev Key","user_id":"x'); alert(1); //"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			api.handleKeys(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}
}

// TestRedactSettingsNeverLeaksSecret asserts the GW-2 read path: secret-valued
// settings are stripped from the serialized response while a has_value flag
// still tells the UI one is configured; non-secret keys round-trip untouched.
// Shaped after main.TestRedactCredentialNeverLeaksFullKey.
func TestRedactSettingsNeverLeaksSecret(t *testing.T) {
	const tavily = "tvly-supersecret-abcdef123456"
	const serper = "serper-topsecret-987654"

	out := redactSettings([]db.SystemSetting{
		{Key: "gateway_name", Value: "Muhiya Gateway"},
		{Key: "tavily_api_key", Value: tavily},
		{Key: "serper_api_key", Value: serper},
	})

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), tavily) {
		t.Fatalf("redactSettings leaked the tavily secret: %s", body)
	}
	if strings.Contains(string(body), serper) {
		t.Fatalf("redactSettings leaked the serper secret: %s", body)
	}

	byKey := map[string]settingView{}
	for _, v := range out {
		byKey[v.Key] = v
	}
	if byKey["tavily_api_key"].Value != "" || !byKey["tavily_api_key"].HasValue {
		t.Fatalf("configured secret must be blanked but flagged present, got %+v", byKey["tavily_api_key"])
	}
	if byKey["gateway_name"].Value != "Muhiya Gateway" {
		t.Fatalf("non-secret key must pass through, got %q", byKey["gateway_name"].Value)
	}

	empty := redactSettings([]db.SystemSetting{{Key: "tavily_api_key", Value: ""}})
	if empty[0].HasValue {
		t.Fatal("an unset secret must report has_value=false")
	}
}

// TestHandleSettingsBlankSecretIsKept proves the write-path half of blank-means-
// keep without a DB: a blank secret submit short-circuits to 200 before
// SetSetting, so it never wipes the stored key (a write would nil-panic here).
func TestHandleSettingsBlankSecretIsKept(t *testing.T) {
	api := &AdminAPI{db: nil}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"key":"tavily_api_key","value":"   "}`))
	rec := httptest.NewRecorder()
	api.handleSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("blank secret POST must return 200 without writing, got %d", rec.Code)
	}
}

func openSettingsTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("Skipping settings round-trip: TEST_DATABASE_URL or DATABASE_URL not set")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	return d
}

// TestHandleSettingsBlankMeansKeepRoundTrip is the full GW-2 regression proof
// against a live DB: a blank Tavily submit preserves the stored secret, a real
// value overwrites it, and a non-secret key still accepts a blank write. Skips
// when no test database is configured.
func TestHandleSettingsBlankMeansKeepRoundTrip(t *testing.T) {
	d := openSettingsTestDB(t)
	api := &AdminAPI{db: d}

	origTavily, _ := d.GetSetting("tavily_api_key")
	t.Cleanup(func() { _ = d.SetSetting("tavily_api_key", origTavily) })
	origName, _ := d.GetSetting("gateway_name")
	t.Cleanup(func() { _ = d.SetSetting("gateway_name", origName) })

	if err := d.SetSetting("tavily_api_key", "tvly-REAL-secret"); err != nil {
		t.Fatalf("seed tavily: %v", err)
	}

	post := func(payload string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		api.handleSettings(rec, req)
		return rec.Code
	}

	if code := post(`{"key":"tavily_api_key","value":""}`); code != http.StatusOK {
		t.Fatalf("blank secret POST status=%d", code)
	}
	if got, _ := d.GetSetting("tavily_api_key"); got != "tvly-REAL-secret" {
		t.Fatalf("blank-means-keep failed: stored secret became %q", got)
	}

	if code := post(`{"key":"tavily_api_key","value":"tvly-NEW"}`); code != http.StatusOK {
		t.Fatalf("value POST status=%d", code)
	}
	if got, _ := d.GetSetting("tavily_api_key"); got != "tvly-NEW" {
		t.Fatalf("real value must overwrite, got %q", got)
	}

	if code := post(`{"key":"gateway_name","value":""}`); code != http.StatusOK {
		t.Fatalf("gateway_name blank status=%d", code)
	}
	if got, _ := d.GetSetting("gateway_name"); got != "" {
		t.Fatalf("non-secret blank must write empty, got %q", got)
	}
}

// TestValidateModelShape pins the DB-free model invariants.
func TestValidateModelShape(t *testing.T) {
	base := func() db.Model {
		return db.Model{
			Name:                 "deepseek-v4-flash",
			TargetModel:          "deepseek/deepseek-chat",
			RoutingTier:          "none",
			InputCostPerMillion:  0.14,
			OutputCostPerMillion: 0.28,
		}
	}

	cases := []struct {
		name     string
		mutate   func(*db.Model)
		wantCode int
	}{
		{"valid", func(m *db.Model) {}, 0},
		{"empty name", func(m *db.Model) { m.Name = "" }, http.StatusBadRequest},
		{"uppercase name", func(m *db.Model) { m.Name = "DeepSeek" }, http.StatusBadRequest},
		{"space in name", func(m *db.Model) { m.Name = "deep seek" }, http.StatusBadRequest},
		{"leading dash", func(m *db.Model) { m.Name = "-x" }, http.StatusBadRequest},
		{"slug ok", func(m *db.Model) { m.Name = "qwen3.5-9b:free" }, 0},
		{"missing target", func(m *db.Model) { m.TargetModel = "" }, http.StatusBadRequest},
		{"empty tier ok", func(m *db.Model) { m.RoutingTier = "" }, 0},
		{"negative input price", func(m *db.Model) { m.InputCostPerMillion = -1 }, http.StatusBadRequest},
		{"negative per-minute", func(m *db.Model) { m.PricePerMinute = -0.01 }, http.StatusBadRequest},
		{"negative context", func(m *db.Model) { m.ContextWindow = -5 }, http.StatusBadRequest},
		{"negative attachment cap", func(m *db.Model) { m.MaxAttachmentMB = -1 }, http.StatusBadRequest},
		{"attachment cap ok", func(m *db.Model) { m.MaxAttachmentMB = 25 }, 0},
		{"mime list ok", func(m *db.Model) { m.AcceptedMimeTypes = "image/png, application/pdf, audio/*" }, 0},
		{"mime list trailing comma ok", func(m *db.Model) { m.AcceptedMimeTypes = "image/png," }, 0},
		{"mime list garbage", func(m *db.Model) { m.AcceptedMimeTypes = "not a mime" }, http.StatusBadRequest},
		{"mime list missing subtype", func(m *db.Model) { m.AcceptedMimeTypes = "image" }, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(&m)
			code, msg := validateModelShape(&m)
			if code != tc.wantCode {
				t.Errorf("code = %d (%q), want %d", code, msg, tc.wantCode)
			}
			if tc.wantCode != 0 && msg == "" {
				t.Error("rejection must carry a message")
			}
		})
	}
}

func TestAdminModelVisionCapable(t *testing.T) {
	if !adminModelVisionCapable(&db.Model{Name: "x", SupportsThinking: false, SupportsVision: true}) {
		t.Error("flag must make a model vision-capable")
	}
	if !adminModelVisionCapable(&db.Model{Name: "qwen3-vl-8b"}) {
		t.Error("-vl heuristic should match")
	}
	if adminModelVisionCapable(&db.Model{Name: "deepseek-chat", TargetModel: "deepseek/deepseek-chat"}) {
		t.Error("plain text model must not be vision-capable")
	}
}

func TestComputeCoverage(t *testing.T) {
	providers := []db.Provider{
		{ID: "deepseek", Status: "active"},
		{ID: "openrouter", Status: "inactive"},
	}

	// No active vision model on an active provider → top warning present.
	got := computeCoverage([]db.Model{
		{Name: "deepseek-chat", ProviderID: "deepseek", Status: "active", TargetModel: "deepseek-chat"},
		{Name: "gemma-4-vision", ProviderID: "openrouter", Status: "active", SupportsVision: true}, // provider inactive
	}, providers)
	if got.ActiveVision != 0 {
		t.Errorf("ActiveVision = %d, want 0 (vision model is on an inactive provider)", got.ActiveVision)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "No active vision-capable model") {
		t.Errorf("expected the no-vision warning first, got %v", got.Warnings)
	}
	foundStranded := false
	for _, wn := range got.Warnings {
		if strings.Contains(wn, "gemma-4-vision") && strings.Contains(wn, "inactive") {
			foundStranded = true
		}
	}
	if !foundStranded {
		t.Errorf("expected a stranded-model warning for gemma-4-vision, got %v", got.Warnings)
	}

	// A flagged vision model on an active provider clears the top warning.
	ok := computeCoverage([]db.Model{
		{Name: "vis", ProviderID: "deepseek", Status: "active", SupportsVision: true, InputCostPerMillion: 1, OutputCostPerMillion: 1},
	}, providers)
	if ok.ActiveVision != 1 {
		t.Errorf("ActiveVision = %d, want 1", ok.ActiveVision)
	}
	for _, wn := range ok.Warnings {
		if strings.Contains(wn, "No active vision-capable model") {
			t.Error("no-vision warning must be gone when a vision model is active")
		}
	}
}

// TestComputeCoverage_MediaCounts pins the audio/video/documents capability
// counters: flagged + active + on an active provider counts; anything else not.
func TestComputeCoverage_MediaCounts(t *testing.T) {
	providers := []db.Provider{
		{ID: "deepseek", Status: "active"},
		{ID: "openrouter", Status: "inactive"},
	}
	got := computeCoverage([]db.Model{
		{Name: "a", ProviderID: "deepseek", Status: "active", SupportsAudio: true, SupportsDocuments: true, InputCostPerMillion: 1, OutputCostPerMillion: 1},
		{Name: "b", ProviderID: "deepseek", Status: "active", SupportsVideo: true, InputCostPerMillion: 1, OutputCostPerMillion: 1},
		{Name: "c", ProviderID: "openrouter", Status: "active", SupportsDocuments: true}, // stranded provider → not counted
		{Name: "d", ProviderID: "deepseek", Status: "inactive", SupportsAudio: true},     // inactive model → not counted
	}, providers)
	if got.ActiveAudio != 1 || got.ActiveVideo != 1 || got.ActiveDocuments != 1 {
		t.Fatalf("media coverage counts wrong: audio=%d video=%d documents=%d", got.ActiveAudio, got.ActiveVideo, got.ActiveDocuments)
	}
}

// TestHandleModelsRejectsBadShapePreDB proves the POST handler refuses a
// malformed model at 400 before any DB call (nil db would panic on a write), so
// shape validation short-circuits first — same guarantee as the user/key tests.
func TestHandleModelsRejectsBadShapePreDB(t *testing.T) {
	api := &AdminAPI{db: nil}
	cases := []struct {
		name string
		body string
	}{
		{"empty name", `{"target_model":"x","routing_tier":"simple"}`},
		{"uppercase name", `{"name":"BadName","target_model":"x"}`},
		{"missing target", `{"name":"good-name"}`},
		{"negative price", `{"name":"good-name","target_model":"x","input_cost_per_million":-1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/models", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			api.handleModels(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 pre-DB, got %d", rec.Code)
			}
		})
	}
}

// TestHandleModelsMergePatchVisibility proves the merge-patch PUT preserves the
// muhiyacode_visible flag when a payload omits it and applies it when present —
// so a stale admin client that never sends the field cannot silently clear it,
// while an explicit toggle takes effect. Needs a live DB; skips otherwise.
func TestHandleModelsMergePatchVisibility(t *testing.T) {
	d := openSettingsTestDB(t)
	api := &AdminAPI{db: d}

	provID := "prov-vis-test"
	_ = d.DeleteProvider(provID)
	if err := d.CreateProvider(db.Provider{ID: provID, Name: provID, BaseURL: "https://example.com", APIKey: "x", Status: "active"}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteProvider(provID) })

	id := "model-vis-test"
	_ = d.DeleteModel(id)
	create := `{"id":"` + id + `","name":"` + id + `","provider_id":"` + provID + `","target_model":"deepseek-chat","status":"active","muhiyacode_visible":true,"input_cost_per_million":1,"output_cost_per_million":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/models", strings.NewReader(create))
	rec := httptest.NewRecorder()
	api.handleModels(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { _ = d.DeleteModel(id) })

	// PUT that OMITS muhiyacode_visible must preserve the stored true.
	put := `{"id":"` + id + `","display_name":"Renamed"}`
	req = httptest.NewRequest(http.MethodPut, "/api/models", strings.NewReader(put))
	rec = httptest.NewRecorder()
	api.handleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge-patch PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := d.GetModel(id)
	if got == nil || !got.MuhiyaCodeVisible {
		t.Fatal("omitted muhiyacode_visible must be preserved as true")
	}

	// PUT that SETS it false must apply.
	put = `{"id":"` + id + `","muhiyacode_visible":false}`
	req = httptest.NewRequest(http.MethodPut, "/api/models", strings.NewReader(put))
	rec = httptest.NewRecorder()
	api.handleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ = d.GetModel(id)
	if got == nil || got.MuhiyaCodeVisible {
		t.Fatal("explicit muhiyacode_visible=false must be applied")
	}
}
