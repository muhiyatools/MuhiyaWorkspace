package proxy

import (
	"reflect"
	"strings"
	"testing"

	"gateway/db"
)

// Model-builder options for the media capability flags.
func audioCap(m *db.Model)    { m.SupportsAudio = true }
func videoCap(m *db.Model)    { m.SupportsVideo = true }
func documentCap(m *db.Model) { m.SupportsDocuments = true }

// part builds one OpenAI-dialect content part map.
func part(kind string) map[string]interface{} {
	return map[string]interface{}{"type": kind}
}

func msgWithParts(kinds ...string) OpenAIMessage {
	arr := make([]interface{}, 0, len(kinds))
	for _, k := range kinds {
		arr = append(arr, part(k))
	}
	return OpenAIMessage{Role: "user", Content: arr}
}

func TestDetectMediaNeedsOpenAI(t *testing.T) {
	cases := []struct {
		name     string
		messages []OpenAIMessage
		want     mediaNeeds
	}{
		{"text string only", []OpenAIMessage{{Role: "user", Content: "hello"}}, mediaNeeds{}},
		{"text part only", []OpenAIMessage{msgWithParts("text")}, mediaNeeds{}},
		{"image", []OpenAIMessage{msgWithParts("text", "image_url")}, mediaNeeds{Vision: true}},
		{"audio", []OpenAIMessage{msgWithParts("input_audio")}, mediaNeeds{Audio: true}},
		{"video", []OpenAIMessage{msgWithParts("video_url")}, mediaNeeds{Video: true}},
		{"document", []OpenAIMessage{msgWithParts("file")}, mediaNeeds{Documents: true}},
		{"mixed across messages", []OpenAIMessage{msgWithParts("image_url"), msgWithParts("file")}, mediaNeeds{Vision: true, Documents: true}},
	}
	for _, tc := range cases {
		if got := detectMediaNeedsOpenAI(tc.messages); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestDetectMediaNeedsAnthropic(t *testing.T) {
	messages := []AnthropicMessage{
		{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}, {Type: "image"}}},
		{Role: "user", Content: []AnthropicContent{{Type: "document"}}},
	}
	got := detectMediaNeedsAnthropic(messages)
	want := mediaNeeds{Vision: true, Documents: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestModelSupportsMedia(t *testing.T) {
	docModel := db.Model{Name: "plain", TargetModel: "plain", SupportsDocuments: true}
	if !modelSupportsMedia(&docModel, mediaNeeds{Documents: true}) {
		t.Error("documents flag must satisfy a documents need")
	}
	if modelSupportsMedia(&docModel, mediaNeeds{Audio: true}) {
		t.Error("a model without the audio flag must not satisfy an audio need")
	}
	if modelSupportsMedia(&docModel, mediaNeeds{Documents: true, Vision: true}) {
		t.Error("a model must support EVERY needed modality, not just one")
	}
	if !modelSupportsMedia(&docModel, mediaNeeds{}) {
		t.Error("text-only needs must match every model")
	}
	// Vision keeps the flag-first-with-heuristic predicate.
	gemma := db.Model{Name: "x", TargetModel: "google/gemma:free"}
	if !modelSupportsMedia(&gemma, mediaNeeds{Vision: true}) {
		t.Error("vision heuristic (gemma target) must still apply")
	}
}

func TestSelectRoute_DocumentRoutesToFlaggedModel(t *testing.T) {
	models := []db.Model{
		mdl("deepseek", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
		mdl("free-gemma", "openrouter", "google/gemma:free", "none", 0, 0, vision, documentCap),
	}
	got, err := selectRoute(models, allActive("openrouter", "deepseek"), "simple", mediaNeeds{Documents: true}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "free-gemma" {
		t.Fatalf("document request should route to the documents-flagged model, got %q", got.ID)
	}
}

func TestSelectRoute_AudioMissing_ErrorNamesCapability(t *testing.T) {
	models := []db.Model{
		mdl("deepseek", "deepseek", "deepseek-chat", "simple", 0.14, 0.28),
		mdl("free-gemma", "openrouter", "google/gemma:free", "none", 0, 0, vision),
	}
	_, err := selectRoute(models, allActive("openrouter", "deepseek"), "simple", mediaNeeds{Audio: true}, false)
	if err == nil {
		t.Fatal("expected a routing error when no audio-capable model exists")
	}
	msg := err.Error()
	for _, want := range []string{"audio input required", "0 audio-capable", "Audio", "Admin → Models"} {
		if !strings.Contains(msg, want) {
			t.Errorf("routing error %q missing %q", msg, want)
		}
	}
}

func TestSelectRoute_ImageAndDocumentNeedBoth(t *testing.T) {
	models := []db.Model{
		mdl("doc-only", "p", "doc", "simple", 1, 1, documentCap),
		mdl("both", "p", "both", "none", 2, 2, vision, documentCap),
	}
	got, err := selectRoute(models, allActive("p"), "simple", mediaNeeds{Vision: true, Documents: true}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "both" {
		t.Fatalf("a request needing image+document must route to the model with both flags, got %q", got.ID)
	}
}

func TestSelectRoute_AudioKeepsFreeCapableModel(t *testing.T) {
	// A media need must keep $0 candidates (that is how free capability models
	// are reached), mirroring the vision behavior.
	models := []db.Model{
		mdl("paid-text", "p", "text", "simple", 0.14, 0.28),
		mdl("free-audio", "p", "aud", "none", 0, 0, audioCap),
	}
	got, err := selectRoute(models, allActive("p"), "simple", mediaNeeds{Audio: true}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "free-audio" {
		t.Fatalf("audio request should reach the free audio model, got %q", got.ID)
	}
}

func TestFilterFallbacks_MediaFiltered(t *testing.T) {
	models := []db.Model{
		mdl("primary", "p", "a", "simple", 1, 1, documentCap),
		mdl("alt-doc", "p", "b", "simple", 2, 2, documentCap),
		mdl("alt-text", "p", "c", "simple", 0.1, 0.1), // cheapest but no documents
	}
	fb := filterFallbacks(models, allActive("p"), "primary", mediaNeeds{Documents: true})
	if len(fb) != 1 || fb[0].ID != "alt-doc" {
		t.Fatalf("document fallbacks must keep only documents-capable models, got %+v", fb)
	}
}

func TestStripUnsupportedMediaParts(t *testing.T) {
	textOnly := &db.Model{Name: "deepseek-chat", TargetModel: "deepseek-chat"}
	messages := []OpenAIMessage{
		{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "text", "text": "look at these"},
			part("image_url"),
			part("file"),
		}},
	}
	out := stripUnsupportedMediaParts(messages, textOnly)
	arr, ok := out[0].Content.([]interface{})
	if !ok || len(arr) != 1 {
		t.Fatalf("expected only the text part to survive, got %+v", out[0].Content)
	}
	txt := arr[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(txt, "[image omitted — this model reads text only]") {
		t.Errorf("image omission note missing: %q", txt)
	}
	if !strings.Contains(txt, "[document omitted — this model does not accept document input]") {
		t.Errorf("document omission note missing: %q", txt)
	}
}

func TestStripUnsupportedMediaParts_CapableModelUntouched(t *testing.T) {
	capable := &db.Model{Name: "m", TargetModel: "m", SupportsVision: true, SupportsDocuments: true}
	messages := []OpenAIMessage{msgWithParts("text", "image_url", "file")}
	out := stripUnsupportedMediaParts(messages, capable)
	if !reflect.DeepEqual(out, messages) {
		t.Fatal("a fully-capable model must receive messages unchanged")
	}
}

func TestStripUnsupportedMediaInBodyMap(t *testing.T) {
	textOnly := &db.Model{Name: "deepseek-chat", TargetModel: "deepseek-chat"}
	bodyMap := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "plain string is untouched"},
			map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "see attachment"},
				part("input_audio"),
			}},
		},
	}
	stripUnsupportedMediaInBodyMap(bodyMap, textOnly)
	msgs := bodyMap["messages"].([]interface{})
	if msgs[0].(map[string]interface{})["content"] != "plain string is untouched" {
		t.Error("string content must never be modified")
	}
	arr := msgs[1].(map[string]interface{})["content"].([]interface{})
	if len(arr) != 1 {
		t.Fatalf("audio part should be stripped, got %d parts", len(arr))
	}
	txt := arr[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(txt, "[audio omitted — this model does not accept audio input]") {
		t.Errorf("audio omission note missing: %q", txt)
	}
}

func TestMaybeInjectOpenRouterPDFParser(t *testing.T) {
	orProvider := &db.Provider{ID: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
	withFile := func() map[string]interface{} {
		return map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": []interface{}{part("file")}},
			},
		}
	}

	// File part + OpenRouter + no client plugins → free engine injected.
	body := withFile()
	maybeInjectOpenRouterPDFParser(body, orProvider, "")
	plugins, ok := body["plugins"].([]interface{})
	if !ok || len(plugins) != 1 {
		t.Fatalf("expected one injected plugin, got %+v", body["plugins"])
	}
	plug := plugins[0].(map[string]interface{})
	if plug["id"] != "file-parser" || plug["pdf"].(map[string]interface{})["engine"] != "pdf-text" {
		t.Fatalf("expected free pdf-text file-parser, got %+v", plug)
	}

	// Operator-configured engine wins.
	body = withFile()
	maybeInjectOpenRouterPDFParser(body, orProvider, "mistral-ocr")
	plug = body["plugins"].([]interface{})[0].(map[string]interface{})
	if plug["pdf"].(map[string]interface{})["engine"] != "mistral-ocr" {
		t.Fatalf("configured engine must be used, got %+v", plug)
	}

	// Client-configured plugins are never overridden.
	body = withFile()
	body["plugins"] = []interface{}{"client-owned"}
	maybeInjectOpenRouterPDFParser(body, orProvider, "")
	if plugs := body["plugins"].([]interface{}); len(plugs) != 1 || plugs[0] != "client-owned" {
		t.Fatal("client plugins must pass through untouched")
	}

	// No file part → no plugin.
	body = map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "text"},
	}}
	maybeInjectOpenRouterPDFParser(body, orProvider, "")
	if _, exists := body["plugins"]; exists {
		t.Fatal("no plugin without a file part")
	}

	// Non-OpenRouter provider → no plugin.
	body = withFile()
	maybeInjectOpenRouterPDFParser(body, &db.Provider{ID: "deepseek", BaseURL: "https://api.deepseek.com"}, "")
	if _, exists := body["plugins"]; exists {
		t.Fatal("no plugin for non-OpenRouter providers")
	}
}

func TestModelCapabilityFields(t *testing.T) {
	m := &db.Model{
		Name: "multi", TargetModel: "multi",
		SupportsVision: true, SupportsAudio: true, SupportsDocuments: true,
		MaxAttachmentMB: 25, AcceptedMimeTypes: " image/png , application/pdf ,",
	}
	fields := modelCapabilityFields(m)
	if fields["supports_audio"] != true || fields["supports_video"] != false || fields["supports_documents"] != true {
		t.Fatalf("capability flags wrong: %+v", fields)
	}
	if fields["max_attachment_mb"] != 25 {
		t.Errorf("max_attachment_mb = %v, want 25", fields["max_attachment_mb"])
	}
	mimes := fields["accepted_mime_types"].([]string)
	if !reflect.DeepEqual(mimes, []string{"image/png", "application/pdf"}) {
		t.Errorf("accepted_mime_types = %v", mimes)
	}
	mods := fields["input_modalities"].([]string)
	if !reflect.DeepEqual(mods, []string{"text", "image", "audio", "document"}) {
		t.Errorf("input_modalities = %v", mods)
	}

	// Empty MIME column must publish an empty array, never null.
	plain := &db.Model{Name: "p", TargetModel: "p"}
	if got := modelCapabilityFields(plain)["accepted_mime_types"].([]string); got == nil || len(got) != 0 {
		t.Errorf("empty mime column must be [] (non-nil), got %v", got)
	}
	if mods := modelCapabilityFields(plain)["input_modalities"].([]string); !reflect.DeepEqual(mods, []string{"text"}) {
		t.Errorf("plain model modalities = %v, want [text]", mods)
	}
}

func TestRoutingDiag_MediaCounts(t *testing.T) {
	models := []db.Model{
		mdl("a", "p", "x", "simple", 1, 1, audioCap, documentCap),
		mdl("b", "p", "y", "simple", 1, 1, videoCap),
		mdl("c", "p", "z", "simple", 1, 1, documentCap, inactive), // inactive → not counted
	}
	d := computeRoutingDiag(models, allActive("p"))
	if d.audioCapable != 1 || d.videoCapable != 1 || d.documentCapable != 1 {
		t.Fatalf("media counts wrong: %+v", d)
	}
}
