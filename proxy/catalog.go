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

func (h *ProxyHandler) handleMuhiyaCodeCatalog(w http.ResponseWriter, r *http.Request) {
	models, err := h.db.ListModels()
	if err != nil {
		h.internalErrorResponse(w, "database error listing MuhiyaCode catalog", err)
		return
	}
	document := muhiyaCatalogDocument{SchemaVersion: muhiyaCatalogSchemaVersion}
	for i := range models {
		model := &models[i]
		if !discoverableModel(model, false) || model.Name == routerModelDeprecated ||
			!containsTag(model.Tags, "muhiyacode") {
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
	w.Header().Set("Cache-Control", "private, max-age=60")
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
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
		h.writeError(w, http.StatusConflict,
			fmt.Sprintf("model record mismatch: expected %s, resolved %s", expected, record.RecordID),
			"model_resolution_mismatch")
		return false
	}
	if expected := strings.TrimSpace(r.Header.Get("X-Muhiya-Expected-Target-Model")); expected != "" &&
		expected != record.TargetModel {
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
		Health:             model.Health,
		DeprecatedAt:       model.DeprecatedAt,
		DeprecationMessage: model.DeprecationMessage,
	}
	immutable := struct {
		ModelID             string
		TargetModel         string
		ProviderFamily      string
		AdapterVersion      string
		CompatibilityEpoch  int
		ContextWindow       int
		MaxOutputTokens     int
		SupportedParameters []string
		CacheContract       json.RawMessage
		Pricing             muhiyaCatalogPricing
		Capabilities        muhiyaCapabilities
	}{
		record.ModelID, record.TargetModel, record.ProviderFamily,
		record.AdapterVersion, record.CompatibilityEpoch, record.ContextWindow,
		record.MaxOutputTokens, record.SupportedParameters, record.CacheContract,
		record.Pricing, record.Capabilities,
	}
	canonical, err := json.Marshal(immutable)
	if err != nil {
		return muhiyaCatalogModel{}, err
	}
	digest := sha256.Sum256(canonical)
	record.RecordID = fmt.Sprintf("model:%x", digest)
	return record, nil
}

func containsTag(tags []string, expected string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), expected) {
			return true
		}
	}
	return false
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if result == nil {
		return []string{}
	}
	return result
}
