package proxy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"gateway/db"
	"gateway/money"
	"gateway/pricing"
)

const muhiyaCatalogSchemaVersion = 2

type muhiyaCatalogDocument struct {
	SchemaVersion int                  `json:"schema_version"`
	Models        []muhiyaCatalogModel `json:"models"`
}

type muhiyaCatalogModel struct {
	RecordID            string               `json:"record_id"`
	ModelID             string               `json:"model_id"`
	TargetModel         string               `json:"target_model"`
	DisplayName         string               `json:"display_name"`
	Description         string               `json:"description,omitempty"`
	Tags                []string             `json:"tags"`
	ProviderFamily      string               `json:"provider_family"`
	AdapterVersion      string               `json:"adapter_version"`
	CompatibilityEpoch  int                  `json:"compatibility_epoch"`
	ContextWindow       int                  `json:"context_window"`
	MaxOutputTokens     int                  `json:"max_output_tokens"`
	SupportedParameters []string             `json:"supported_parameters"`
	CacheContract       json.RawMessage      `json:"cache_contract"`
	Pricing             muhiyaCatalogPricing `json:"pricing"`
	Capabilities        muhiyaCapabilities   `json:"capabilities"`
	// CodingTier / SpeedScore are the operator's 0..5 capability ranks, drawn as
	// the model picker's Intelligence and Speed bars. 0 means unrated.
	//
	// These belong on the catalog document, not only on the v1-shaped /v1/models
	// response: catalog v2 is what MuhiyaCode actually consumes, so ranks added
	// to the v1 shape alone were saved by the admin panel and then never reached
	// the picker, which drew every rated model as "unrated".
	CodingTier float64 `json:"coding_tier"`
	SpeedScore float64 `json:"speed_score"`
	Health              string               `json:"health"`
	DeprecatedAt        *time.Time           `json:"deprecated_at,omitempty"`
	DeprecationMessage  string               `json:"deprecation_message,omitempty"`
}

type muhiyaCatalogPricing struct {
	RuleSetID                   string         `json:"rule_set_id"`
	InputNanoUSDPerMillion      money.NanoUSD  `json:"input_nano_usd_per_million"`
	OutputNanoUSDPerMillion     money.NanoUSD  `json:"output_nano_usd_per_million"`
	CacheReadNanoUSDPerMillion  money.NanoUSD  `json:"cache_read_nano_usd_per_million"`
	CacheWriteNanoUSDPerMillion money.NanoUSD  `json:"cache_write_nano_usd_per_million"`
	Tiers                       []pricing.Tier `json:"tiers"`
}

type muhiyaCapabilities struct {
	Vision            bool     `json:"vision"`
	Thinking          bool     `json:"thinking"`
	Audio             bool     `json:"audio"`
	Video             bool     `json:"video"`
	Documents         bool     `json:"documents"`
	MaxAttachmentMB   int      `json:"max_attachment_mb"`
	AcceptedMIMETypes []string `json:"accepted_mime_types"`
	InputModalities   []string `json:"input_modalities"`
}

// muhiyaCatalogExplainEntry is one model's discoverability verdict for the
// ?explain=1 diagnostic — the answer to "why isn't my model showing up in
// MuhiyaCode?" that this endpoint previously had no way to give an operator.
type muhiyaCatalogExplainEntry struct {
	ModelID        string `json:"model_id"`
	Included       bool   `json:"included"`
	ExcludedReason string `json:"excluded_reason,omitempty"`
}

func (h *ProxyHandler) handleMuhiyaCodeCatalog(w http.ResponseWriter, r *http.Request) {
	models, err := h.db.ListModels()
	if err != nil {
		h.internalErrorResponse(w, "database error listing MuhiyaCode catalog", err)
		return
	}
	if r.URL.Query().Get("explain") != "" {
		h.writeMuhiyaCatalogExplain(w, models)
		return
	}
	document := muhiyaCatalogDocument{SchemaVersion: muhiyaCatalogSchemaVersion}
	for i := range models {
		model := &models[i]
		// muhiyacode_visible is the single visibility authority (see
		// discoverableModel's doc comment, which now also excludes the
		// deprecated router model universally). The "muhiyacode" TAG used to
		// gate this endpoint instead: it is derived from the column by the
		// admin-API write path and by the models AFTER INSERT trigger, but
		// nothing re-derives it on a direct SQL UPDATE, so a tag could go
		// stale while the column was correct — a model marked visible in the
		// admin panel could still be silently absent here. Filtering on the
		// column directly (discoverableModel(model, true), the EXACT function
		// /v1/models' MuhiyaCode branch calls) removes that drift entirely
		// instead of chasing every path that must keep the tag in sync, and
		// guarantees the two endpoints can never again disagree about which
		// models a MuhiyaCode caller sees.
		if !discoverableModel(model, true) {
			continue
		}
		record, err := catalogRecord(model)
		if err != nil {
			h.internalErrorResponse(w, "invalid model catalog record "+model.ID, err)
			return
		}
		document.Models = append(document.Models, record)
	}
	sort.Slice(document.Models, func(i, j int) bool {
		return document.Models[i].ModelID < document.Models[j].ModelID
	})
	body, err := json.Marshal(document)
	if err != nil {
		h.internalErrorResponse(w, "catalog encoding failed", err)
		return
	}
	digest := sha256.Sum256(body)
	version := fmt.Sprintf("%x", digest)
	etag := `"` + version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Muhiya-Catalog-Version", version)
	// The ETag is already recomputed from a fresh ListModels() query on every
	// request — it can never be stale relative to the database. max-age=60
	// let an intermediary (a CDN or reverse proxy in front of this gateway)
	// serve a blindly-reused response for up to 60s after an operator added or
	// edited a model, with no request reaching this handler at all — the exact
	// window in which "I just added a model" would appear to do nothing.
	// no-cache still permits storing the response but forces revalidation via
	// If-None-Match on every use, so the bandwidth win from ETag/304 is kept
	// while the blind-reuse window is closed.
	w.Header().Set("Cache-Control", "private, no-cache")
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// explainMuhiyaCodeDiscoverability classifies a single model against the
// same rule /v1/muhiyacode/models applies (discoverableModel(model, true),
// plus this endpoint's own router-name exclusion), reporting WHICH condition
// failed rather than just true/false. Pure so it can be tested without a
// database. Order matches discoverableModel's own check order.
func explainMuhiyaCodeDiscoverability(model *db.Model) muhiyaCatalogExplainEntry {
	entry := muhiyaCatalogExplainEntry{ModelID: model.Name}
	switch {
	case model.Name == routerModelDeprecated:
		entry.ExcludedReason = "deprecated router model"
	case model.Status != "active":
		entry.ExcludedReason = "status=" + model.Status
	case model.Transcribe:
		entry.ExcludedReason = "transcribe-only"
	case !model.MuhiyaCodeVisible:
		entry.ExcludedReason = "not muhiyacode_visible"
	default:
		entry.Included = true
	}
	return entry
}

// writeMuhiyaCatalogExplain reports, for every model regardless of
// discoverability, whether it is included in the MuhiyaCode catalog and why
// not when it is excluded. discoverableModel itself gates on three
// conditions (status, transcribe, muhiyacode_visible) with no way for an
// operator to ask which one is failing for a given model — this is that
// diagnostic.
func (h *ProxyHandler) writeMuhiyaCatalogExplain(w http.ResponseWriter, models []db.Model) {
	entries := make([]muhiyaCatalogExplainEntry, 0, len(models))
	for i := range models {
		entries = append(entries, explainMuhiyaCodeDiscoverability(&models[i]))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ModelID < entries[j].ModelID })
	body, err := json.Marshal(map[string]any{"models": entries})
	if err != nil {
		h.internalErrorResponse(w, "catalog explain encoding failed", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *ProxyHandler) establishModelResolution(
	w http.ResponseWriter,
	r *http.Request,
	model *db.Model,
) bool {
	record, err := catalogRecord(model)
	if err != nil {
		h.internalErrorResponse(w, "model resolution metadata invalid", err)
		return false
	}
	if expected := strings.TrimSpace(r.Header.Get("X-Muhiya-Expected-Model-Record")); expected != "" &&
		expected != record.RecordID {
		// X-Muhiya-Refresh-Catalog tells a client that CAN re-discover (rather
		// than abandon the task outright) to re-fetch /v1/muhiyacode/models and
		// retry once with the fresh record id — recovering from exactly the
		// case the RecordID narrowing above already reduced the frequency of.
		w.Header().Set("X-Muhiya-Refresh-Catalog", "true")
		h.writeError(w, http.StatusConflict,
			fmt.Sprintf("model record mismatch: expected %s, resolved %s", expected, record.RecordID),
			"model_resolution_mismatch")
		return false
	}
	if expected := strings.TrimSpace(r.Header.Get("X-Muhiya-Expected-Target-Model")); expected != "" &&
		expected != record.TargetModel {
		w.Header().Set("X-Muhiya-Refresh-Catalog", "true")
		h.writeError(w, http.StatusConflict,
			fmt.Sprintf("target model mismatch: expected %s, resolved %s", expected, record.TargetModel),
			"model_resolution_mismatch")
		return false
	}
	w.Header().Set("X-Muhiya-Resolved-Model-Record", record.RecordID)
	w.Header().Set("X-Muhiya-Resolved-Target-Model", record.TargetModel)
	w.Header().Set("X-Muhiya-Resolved-Provider-Family", record.ProviderFamily)
	w.Header().Set("X-Muhiya-Compatibility-Epoch", fmt.Sprintf("%d", record.CompatibilityEpoch))
	if rules, err := pricingRulesForModel(model); err == nil {
		w.Header().Set("X-Muhiya-Price-Snapshot", rules.ID)
	}
	return true
}

func catalogRecord(model *db.Model) (muhiyaCatalogModel, error) {
	tags := sortedCopy(model.Tags)
	parameters := sortedCopy(model.SupportedParameters)
	tiers := append([]pricing.Tier(nil), model.PricingTiers...)
	cacheContract := json.RawMessage(model.CacheContract)
	if len(cacheContract) == 0 {
		cacheContract = json.RawMessage(`{}`)
	}
	if !json.Valid(cacheContract) {
		return muhiyaCatalogModel{}, fmt.Errorf("cache contract is not valid JSON")
	}
	displayName := model.DisplayName
	if displayName == "" {
		displayName = model.Name
	}
	record := muhiyaCatalogModel{
		ModelID:             model.Name,
		TargetModel:         model.TargetModel,
		DisplayName:         displayName,
		Description:         model.Description,
		Tags:                tags,
		ProviderFamily:      model.ProviderFamily,
		AdapterVersion:      model.AdapterVersion,
		CompatibilityEpoch:  model.CompatibilityEpoch,
		ContextWindow:       model.ContextWindow,
		MaxOutputTokens:     model.MaxOutputTokens,
		SupportedParameters: parameters,
		CacheContract:       cacheContract,
		Pricing: muhiyaCatalogPricing{
			RuleSetID:                   model.PricingRuleSetID,
			InputNanoUSDPerMillion:      model.InputCostNanoPerMillion,
			OutputNanoUSDPerMillion:     model.OutputCostNanoPerMillion,
			CacheReadNanoUSDPerMillion:  model.CacheReadCostNanoPerMillion,
			CacheWriteNanoUSDPerMillion: model.CacheWriteCostNanoPerMillion,
			Tiers:                       tiers,
		},
		Capabilities: muhiyaCapabilities{
			Vision:            model.SupportsVision,
			Thinking:          model.SupportsThinking,
			Audio:             model.SupportsAudio,
			Video:             model.SupportsVideo,
			Documents:         model.SupportsDocuments,
			MaxAttachmentMB:   model.MaxAttachmentMB,
			AcceptedMIMETypes: splitMimeList(model.AcceptedMimeTypes),
			InputModalities:   modelInputModalities(model),
		},
		CodingTier:         model.CodingTier,
		SpeedScore:         model.SpeedScore,
		Health:             model.Health,
		DeprecatedAt:       model.DeprecatedAt,
		DeprecationMessage: model.DeprecationMessage,
	}
	// RecordID identifies the model's WIRE CONTRACT — what the agent's
	// prefix-shape guard and cache-affinity logic must be told about the
	// instant it changes. It intentionally excludes ContextWindow,
	// MaxOutputTokens, Pricing, and Capabilities: those are catalog-
	// informational limits the agent uses for its own client-side budget
	// display, not fields that change what bytes the gateway forwards or how
	// it interprets them. Before this exclusion, an operator editing a
	// model's PRICE while a session was live changed the RecordID, and the
	// next request's X-Muhiya-Expected-Model-Record no longer matched —
	// establishModelResolution returned an unrecoverable 409
	// model_resolution_mismatch and killed a task over a change with zero
	// wire impact. AdapterVersion is deliberately excluded too: an operator
	// who ships an adapter change that DOES alter wire behavior is expected
	// to bump CompatibilityEpoch, which is the one field whose entire
	// purpose is "clients must notice this changed" (see
	// TestCatalogRecordIDIsStableAndCompatibilitySensitive).
	immutable := struct {
		ModelID             string
		TargetModel         string
		ProviderFamily      string
		CompatibilityEpoch  int
		SupportedParameters []string
		CacheContract       json.RawMessage
	}{
		record.ModelID, record.TargetModel, record.ProviderFamily,
		record.CompatibilityEpoch, record.SupportedParameters, record.CacheContract,
	}
	canonical, err := json.Marshal(immutable)
	if err != nil {
		return muhiyaCatalogModel{}, err
	}
	digest := sha256.Sum256(canonical)
	record.RecordID = fmt.Sprintf("model:%x", digest)
	return record, nil
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if result == nil {
		return []string{}
	}
	return result
}
