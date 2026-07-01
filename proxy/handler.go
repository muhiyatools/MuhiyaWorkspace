package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gateway/db"
	"github.com/google/uuid"
)

var httpClient = &http.Client{
	Timeout: 15 * time.Minute, // Allow very long responses / reasoning models
	Transport: &http.Transport{
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
	},
}

type ProxyHandler struct {
	db      *db.DB
	limiter *RateLimiter
}

func NewProxyHandler(database *db.DB, limiter *RateLimiter) *ProxyHandler {
	return &ProxyHandler{
		db:      database,
		limiter: limiter,
	}
}

type responseWriterWithRequest struct {
	http.ResponseWriter
	req *http.Request
}

func (w *responseWriterWithRequest) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *ProxyHandler) getRequest(w http.ResponseWriter) *http.Request {
	if wrap, ok := w.(*responseWriterWithRequest); ok {
		return wrap.req
	}
	return nil
}

func isAnthropicRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.Contains(r.URL.Path, "/messages") {
		return true
	}
	if r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" {
		return true
	}
	return false
}

func getClientAppName(r *http.Request) string {
	if r == nil {
		return "Unknown"
	}
	if clientApp := r.Header.Get("X-Client-App"); clientApp != "" {
		return clientApp
	}
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		if r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" {
			return "Anthropic Client"
		}
		return "API Client"
	}
	uaLower := strings.ToLower(ua)
	if strings.Contains(uaLower, "claude-code") || strings.Contains(uaLower, "claude-cli") {
		return "Claude Code"
	}
	if strings.Contains(uaLower, "anthropic") {
		return "Anthropic SDK"
	}
	if strings.Contains(uaLower, "openai-python") {
		return "OpenAI Python"
	}
	if strings.Contains(uaLower, "openai-node") {
		return "OpenAI Node"
	}
	if strings.Contains(uaLower, "openai") {
		return "OpenAI SDK"
	}
	if strings.Contains(uaLower, "curl") {
		return "curl"
	}
	if strings.Contains(uaLower, "mozilla") || strings.Contains(uaLower, "safari") || strings.Contains(uaLower, "chrome") {
		return "Web Browser"
	}
	if strings.Contains(uaLower, "postman") {
		return "Postman"
	}
	if len(ua) > 20 {
		return ua[:17] + "..."
	}
	return ua
}

type openaiErrorPayload struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
}

type anthropicErrorPayload struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func translateErrorBytes(respBytes []byte, clientIsAnthropic bool) []byte {
	if len(respBytes) == 0 {
		return respBytes
	}

	// Try parsing as Anthropic error
	var anthErr anthropicErrorPayload
	isAnth := json.Unmarshal(respBytes, &anthErr) == nil && anthErr.Type == "error" && anthErr.Error.Message != ""

	// Try parsing as OpenAI error
	var oaiErr openaiErrorPayload
	isOAI := json.Unmarshal(respBytes, &oaiErr) == nil && oaiErr.Error.Message != ""

	if clientIsAnthropic {
		if isAnth {
			return respBytes
		}
		if isOAI {
			translated := anthropicErrorPayload{
				Type: "error",
			}
			translated.Error.Type = oaiErr.Error.Type
			if translated.Error.Type == "" {
				translated.Error.Type = "api_error"
			}
			translated.Error.Message = oaiErr.Error.Message
			out, err := json.Marshal(translated)
			if err == nil {
				return out
			}
		}
		// Fallback: wrap unknown error in Anthropic format
		translated := anthropicErrorPayload{
			Type: "error",
		}
		translated.Error.Type = "api_error"
		translated.Error.Message = string(respBytes)
		out, err := json.Marshal(translated)
		if err == nil {
			return out
		}
	} else {
		// Client is OpenAI
		if isOAI {
			return respBytes
		}
		if isAnth {
			translated := openaiErrorPayload{}
			translated.Error.Message = anthErr.Error.Message
			translated.Error.Type = anthErr.Error.Type
			if translated.Error.Type == "" {
				translated.Error.Type = "api_error"
			}
			out, err := json.Marshal(translated)
			if err == nil {
				return out
			}
		}
		// Fallback: wrap unknown error in OpenAI format
		translated := openaiErrorPayload{}
		translated.Error.Message = string(respBytes)
		translated.Error.Type = "api_error"
		out, err := json.Marshal(translated)
		if err == nil {
			return out
		}
	}

	return respBytes
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = &responseWriterWithRequest{ResponseWriter: w, req: r}

	// Handle GET /v1/models and /v1/models/{id} endpoints (Model Discovery)
	if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/models") || strings.Contains(r.URL.Path, "/models/")) {
		h.handleModelDiscovery(w, r)
		return
	}

	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed", "invalid_request_error")
		return
	}

	// 1. Authenticate Virtual Key (Support Authorization: Bearer OR x-api-key)
	keyID := ""
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		keyID = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		keyID = r.Header.Get("x-api-key")
	}

	if keyID == "" {
		h.writeError(w, http.StatusUnauthorized, "Missing or invalid credentials. Use Bearer token or x-api-key.", "invalid_request_error")
		return
	}

	key, err := h.db.GetVirtualKey(keyID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Database error: "+err.Error(), "api_error")
		return
	}
	if key == nil || key.Status != "active" {
		h.writeError(w, http.StatusUnauthorized, "Invalid or revoked virtual key.", "invalid_request_error")
		return
	}

	if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
		h.writeError(w, http.StatusUnauthorized, "Virtual key has expired.", "invalid_request_error")
		return
	}

	// 2. Read Request Body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error")
		return
	}
	r.Body.Close()

	// 3. Detect Route Protocol
	isAnthropicRoute := strings.Contains(r.URL.Path, "/messages")
	isTranscriptionRoute := strings.Contains(r.URL.Path, "/audio/transcriptions")

	if isTranscriptionRoute {
		h.serveTranscriptionClient(w, r, bodyBytes, key)
	} else if isAnthropicRoute {
		h.serveAnthropicClient(w, r, bodyBytes, key)
	} else {
		h.serveOpenAIClient(w, r, bodyBytes, key)
	}
}

// ------------------------------------------
// OpenAI Client Routing (Incoming: OpenAI format)
// ------------------------------------------
func (h *ProxyHandler) serveOpenAIClient(w http.ResponseWriter, r *http.Request, bodyBytes []byte, key *db.VirtualKey) {
	var oaiReq OpenAIRequest
	if err := json.Unmarshal(bodyBytes, &oaiReq); err != nil {
		log.Printf("[ERROR] failed to unmarshal OpenAI request: %v. Body: %s", err, string(bodyBytes))
		h.writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error(), "invalid_request_error")
		return
	}

	if oaiReq.Model == "" {
		h.writeError(w, http.StatusBadRequest, "Model parameter is required", "invalid_request_error")
		return
	}

	isRouterRequest := oaiReq.Model == "muhiya-ai-router"
	var targetModel *db.Model
	var fallbackModels []*db.Model
	var err error
	complexity := "direct"

	if isRouterRequest {
		complexity = AnalyzePromptComplexity(oaiReq.Messages)
		needsVision := false
		for _, msg := range oaiReq.Messages {
			if arr, ok := msg.Content.([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						if m["type"] == "image_url" {
							needsVision = true
							break
						}
					}
				}
			}
		}

		thinkingRequested := oaiReq.Thinking != nil || oaiReq.ReasoningEffort != nil
		targetModel, err = h.RouteToModel(complexity, needsVision, thinkingRequested)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Routing error: "+err.Error(), "api_error")
			return
		}
		fallbackModels, _ = h.GetFallbackModels(targetModel.ID, needsVision)
		log.Printf("[ROUTER] Selected target model: %s (complexity: %s, vision: %v) with %d fallback(s)", targetModel.Name, complexity, needsVision, len(fallbackModels))
	} else {
		targetModel, err = h.db.GetModelByName(oaiReq.Model)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Database error: "+err.Error(), "api_error")
			return
		}
		if targetModel == nil {
			h.writeError(w, http.StatusNotFound, fmt.Sprintf("Model '%s' not found or inactive", oaiReq.Model), "invalid_request_error")
			return
		}
	}

	// Prepare candidate models to try
	candidates := []*db.Model{targetModel}
	if isRouterRequest {
		candidates = append(candidates, fallbackModels...)
	}

	var lastBuffer *BufferedResponseWriter
	success := false

	for idx, model := range candidates {
		// Clone request to avoid mutating original for subsequent retries
		reqCopy := oaiReq
		reqCopy.Model = model.Name

		provider, err := h.db.GetProvider(model.ProviderID)
		if err != nil {
			log.Printf("[ROUTER-WARNING] Failed to get provider for model %s: %v", model.Name, err)
			continue
		}
		if provider == nil || provider.Status != "active" {
			log.Printf("[ROUTER-WARNING] Provider %s is inactive for model %s", model.ProviderID, model.Name)
			continue
		}

		// Estimate prompt tokens
		var textBuilder strings.Builder
		for _, m := range reqCopy.Messages {
			textBuilder.WriteString(GetMessageContentString(m.Content))
		}
		promptTokens := estimateTokens(textBuilder.String())

		// Check Rate Limits and Budgets
		if err := h.limiter.CheckLimit(key, promptTokens); err != nil {
			h.writeError(w, http.StatusTooManyRequests, "Limit exceeded: "+err.Error(), "rate_limit_error")
			return
		}

		requestedModel := oaiReq.Model
		complexityStr := "direct"
		if isRouterRequest {
			requestedModel = "muhiya-ai-router"
			complexityStr = complexity
		}

		startTime := time.Now()
		reqLog := db.RequestLog{
			ID:               uuid.New().String(),
			VirtualKeyID:     key.ID,
			UserID:           key.UserID,
			ModelID:          model.ID,
			ProviderID:       provider.ID,
			RequestPath:      r.URL.Path,
			InputTokens:      promptTokens,
			ClientApp:        getClientAppName(r),
			RequestedModel:   requestedModel,
			Complexity:       complexityStr,
			FailoverAttempts: idx,
			CreatedAt:        startTime,
		}

		var targetURL string
		var useAnthropicUpstream bool

		if provider.BaseURL != "" {
			targetURL = provider.BaseURL
			useAnthropicUpstream = false
		} else if provider.AnthropicBaseURL != "" {
			targetURL = provider.AnthropicBaseURL
			useAnthropicUpstream = true
		} else {
			log.Printf("[ROUTER-WARNING] Provider %s has no base URL", provider.ID)
			continue
		}

		resolvedProvider := *provider
		resolvedProvider.BaseURL = targetURL

		// Create buffered writer to intercept failures cleanly
		bufW := NewBufferedResponseWriter(w)
		lastBuffer = bufW

		// Perform proxy
		if useAnthropicUpstream {
			h.proxyOpenAIToAnthropic(bufW, r, &reqCopy, model, &resolvedProvider, reqLog, startTime)
		} else {
			newBody, _ := json.Marshal(reqCopy)
			h.proxyOpenAIToOpenAI(bufW, r, newBody, model, &resolvedProvider, reqLog, startTime)
		}

		// Check if request was successful
		if bufW.statusCode > 0 && bufW.statusCode < 400 {
			success = true
			if idx > 0 {
				log.Printf("[ROUTER-SUCCESS] Router failover succeeded with model %s on attempt %d", model.Name, idx+1)
			}
			break
		}

		log.Printf("[ROUTER-FAILOVER] Model %s failed (status %d). Trying next candidate...", model.Name, bufW.statusCode)
	}

	// If all candidates failed, flush the last failure response to the client
	if !success && lastBuffer != nil {
		lastBuffer.FlushToActual()
	}
}

// ------------------------------------------
// Anthropic Client Routing (Incoming: Anthropic format)
// ------------------------------------------
func (h *ProxyHandler) serveAnthropicClient(w http.ResponseWriter, r *http.Request, bodyBytes []byte, key *db.VirtualKey) {
	var anthReq AnthropicRequest
	if err := json.Unmarshal(bodyBytes, &anthReq); err != nil {
		log.Printf("[ERROR] failed to unmarshal Anthropic request: %v. Body: %s", err, string(bodyBytes))
		h.writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error(), "invalid_request_error")
		return
	}

	if anthReq.Model == "" {
		h.writeError(w, http.StatusBadRequest, "Model parameter is required", "invalid_request_error")
		return
	}

	isRouterRequest := anthReq.Model == "muhiya-ai-router"
	var targetModel *db.Model
	var fallbackModels []*db.Model
	var err error
	complexity := "direct"

	if isRouterRequest {
		// Convert Anthropic format messages to OpenAI format for simple complexity analysis
		var oaiMessages []OpenAIMessage
		for _, m := range anthReq.Messages {
			var textBuilder strings.Builder
			for _, c := range m.Content {
				textBuilder.WriteString(c.Text)
			}
			oaiMessages = append(oaiMessages, OpenAIMessage{
				Role:    m.Role,
				Content: textBuilder.String(),
			})
		}
		complexity = AnalyzePromptComplexity(oaiMessages)

		needsVision := false
		for _, msg := range anthReq.Messages {
			for _, contentBlock := range msg.Content {
				if contentBlock.Type == "image" {
					needsVision = true
					break
				}
			}
		}

		thinkingRequested := anthReq.Thinking != nil
		targetModel, err = h.RouteToModel(complexity, needsVision, thinkingRequested)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Routing error: "+err.Error(), "api_error")
			return
		}
		fallbackModels, _ = h.GetFallbackModels(targetModel.ID, needsVision)
		log.Printf("[ROUTER] Selected target model: %s (complexity: %s, vision: %v) with %d fallback(s)", targetModel.Name, complexity, needsVision, len(fallbackModels))
	} else {
		targetModel, err = h.db.GetModelByName(anthReq.Model)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "Database error: "+err.Error(), "api_error")
			return
		}
		if targetModel == nil {
			h.writeError(w, http.StatusNotFound, fmt.Sprintf("Model '%s' not found or inactive", anthReq.Model), "invalid_request_error")
			return
		}
	}

	// Prepare candidate models to try
	candidates := []*db.Model{targetModel}
	if isRouterRequest {
		candidates = append(candidates, fallbackModels...)
	}

	var lastBuffer *BufferedResponseWriter
	success := false

	for idx, model := range candidates {
		// Clone request to avoid mutating original for subsequent retries
		reqCopy := anthReq
		reqCopy.Model = model.Name

		provider, err := h.db.GetProvider(model.ProviderID)
		if err != nil {
			log.Printf("[ROUTER-WARNING] Failed to get provider for model %s: %v", model.Name, err)
			continue
		}
		if provider == nil || provider.Status != "active" {
			log.Printf("[ROUTER-WARNING] Provider %s is inactive for model %s", model.ProviderID, model.Name)
			continue
		}

		// Estimate prompt tokens
		var textBuilder strings.Builder
		textBuilder.WriteString(string(reqCopy.System))
		for _, m := range reqCopy.Messages {
			for _, b := range m.Content {
				textBuilder.WriteString(b.Text)
				textBuilder.WriteString(b.Thinking)
			}
		}
		promptTokens := estimateTokens(textBuilder.String())

		// Check limits
		if err := h.limiter.CheckLimit(key, promptTokens); err != nil {
			h.writeError(w, http.StatusTooManyRequests, "Limit exceeded: "+err.Error(), "rate_limit_error")
			return
		}

		requestedModel := anthReq.Model
		complexityStr := "direct"
		if isRouterRequest {
			requestedModel = "muhiya-ai-router"
			complexityStr = complexity
		}

		startTime := time.Now()
		reqLog := db.RequestLog{
			ID:               uuid.New().String(),
			VirtualKeyID:     key.ID,
			UserID:           key.UserID,
			ModelID:          model.ID,
			ProviderID:       provider.ID,
			RequestPath:      r.URL.Path,
			InputTokens:      promptTokens,
			ClientApp:        getClientAppName(r),
			RequestedModel:   requestedModel,
			Complexity:       complexityStr,
			FailoverAttempts: idx,
			CreatedAt:        startTime,
		}

		var targetURL string
		var useAnthropicUpstream bool

		if provider.BaseURL != "" {
			targetURL = provider.BaseURL
			useAnthropicUpstream = false
		} else if provider.AnthropicBaseURL != "" {
			targetURL = provider.AnthropicBaseURL
			useAnthropicUpstream = true
		} else {
			log.Printf("[ROUTER-WARNING] Provider %s has no base URL", provider.ID)
			continue
		}

		resolvedProvider := *provider
		resolvedProvider.BaseURL = targetURL

		// Create buffered writer to intercept failures cleanly
		bufW := NewBufferedResponseWriter(w)
		lastBuffer = bufW

		// Perform proxy
		if useAnthropicUpstream {
			newBody, _ := json.Marshal(reqCopy)
			h.proxyAnthropicToAnthropic(bufW, r, newBody, &reqCopy, model, &resolvedProvider, reqLog, startTime)
		} else {
			h.proxyAnthropicToOpenAI(bufW, r, &reqCopy, model, &resolvedProvider, reqLog, startTime)
		}

		// Check if request was successful
		if bufW.statusCode > 0 && bufW.statusCode < 400 {
			success = true
			if idx > 0 {
				log.Printf("[ROUTER-SUCCESS] Router failover succeeded with model %s on attempt %d", model.Name, idx+1)
			}
			break
		}

		log.Printf("[ROUTER-FAILOVER] Model %s failed (status %d). Trying next candidate...", model.Name, bufW.statusCode)
	}

	// If all candidates failed, flush the last failure response to the client
	if !success && lastBuffer != nil {
		lastBuffer.FlushToActual()
	}
}

// ------------------------------------------
// Proxies Implementation
// ------------------------------------------

func (h *ProxyHandler) proxyOpenAIToOpenAI(w http.ResponseWriter, r *http.Request, origBody []byte, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	var bodyMap map[string]interface{}
	_ = json.Unmarshal(origBody, &bodyMap)
	bodyMap["model"] = model.TargetModel

	stream, _ := bodyMap["stream"].(bool)
	if stream {
		bodyMap["stream_options"] = map[string]interface{}{
			"include_usage": true,
		}
	}

	newBody, _ := json.Marshal(bodyMap)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") && !strings.HasSuffix(url, "/completions") {
		url += "/chat/completions"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Connection failed: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, &log, startTime)
		return
	}

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		reader := bufio.NewReader(resp.Body)
		var textAccumulator strings.Builder
		var finalUsage *OpenAIUsage
		normalClose := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			w.Write([]byte(line))
			flusher.Flush()

			if strings.HasPrefix(line, "data:") {
				dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if dataStr == "[DONE]" {
					normalClose = true
				} else if dataStr != "" {
					var chunk OpenAIChunk
					if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
						if len(chunk.Choices) > 0 {
							textAccumulator.WriteString(chunk.Choices[0].Delta.Content)
						}
						if chunk.Usage != nil {
							finalUsage = chunk.Usage
						}
					}
				}
			}
		}

		if !normalClose {
			w.Write([]byte("data: {\"error\": {\"message\": \"Upstream connection disconnected prematurely.\", \"type\": \"api_error\"}}\n\n"))
			flusher.Flush()
		}

		completionTokens := estimateTokens(textAccumulator.String())
		inputTokens := log.InputTokens
		cacheRead := 0

		if finalUsage != nil {
			inputTokens = finalUsage.PromptTokens
			completionTokens = finalUsage.CompletionTokens
			if finalUsage.PromptTokensDetails != nil {
				cacheRead = finalUsage.PromptTokensDetails.CachedTokens
			}
		}

		log.StatusCode = http.StatusOK
		log.InputTokens = inputTokens
		log.OutputTokens = completionTokens
		log.CacheReadTokens = cacheRead
		log.Cost = calculateCost(model, inputTokens, completionTokens, cacheRead, 0)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, completionTokens)
		
		sendMuhiyaMetaChunk(w, &log, model.Name)
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			h.logAndWriteError(w, http.StatusInternalServerError, "Read response error", "api_error", &log, startTime)
			return
		}

		var oaiResp OpenAIResponse
		completionTokens := 0
		cacheRead := 0
		if err := json.Unmarshal(respBody, &oaiResp); err == nil && oaiResp.Usage.TotalTokens > 0 {
			log.InputTokens = oaiResp.Usage.PromptTokens
			completionTokens = oaiResp.Usage.CompletionTokens
			if oaiResp.Usage.PromptTokensDetails != nil {
				cacheRead = oaiResp.Usage.PromptTokensDetails.CachedTokens
			}
		} else {
			if len(oaiResp.Choices) > 0 {
				completionTokens = estimateTokens(GetMessageContentString(oaiResp.Choices[0].Message.Content))
			}
		}

		log.StatusCode = http.StatusOK
		log.OutputTokens = completionTokens
		log.CacheReadTokens = cacheRead
		log.Cost = calculateCost(model, log.InputTokens, completionTokens, cacheRead, 0)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, completionTokens)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBody)
	}
}

func (h *ProxyHandler) proxyOpenAIToAnthropic(w http.ResponseWriter, r *http.Request, oaiReq *OpenAIRequest, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	anthRequest, err := TranslateOpenAIToAnthropic(oaiReq, model.TargetModel)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadRequest, "Payload translation error: "+err.Error(), "invalid_request_error", &log, startTime)
		return
	}
	newBody, _ := json.Marshal(anthRequest)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/v1/messages") && !strings.HasSuffix(url, "/messages") {
		url += "/v1/messages"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Upstream error: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, &log, startTime)
		return
	}

	if oaiReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		reader := bufio.NewReader(resp.Body)
		var usageTracker OpenAIUsage
		msgID := "chatcmpl-" + uuid.New().String()

		normalClose := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			oaiChunkBytes, done, err := TranslateAnthropicChunkToOpenAI(line, msgID, oaiReq.Model, &usageTracker)
			if err != nil {
				continue
			}
			if len(oaiChunkBytes) > 0 {
				w.Write([]byte("data: " + string(oaiChunkBytes) + "\n\n"))
				flusher.Flush()
			}
			if done {
				normalClose = true
				w.Write([]byte("data: [DONE]\n\n"))
				flusher.Flush()
				break
			}
		}

		if !normalClose {
			w.Write([]byte("data: {\"error\": {\"message\": \"Upstream connection disconnected prematurely.\", \"type\": \"api_error\"}}\n\n"))
			flusher.Flush()
		}

		log.StatusCode = http.StatusOK
		log.InputTokens = usageTracker.PromptTokens
		log.OutputTokens = usageTracker.CompletionTokens
		cacheRead := 0
		if usageTracker.PromptTokensDetails != nil {
			cacheRead = usageTracker.PromptTokensDetails.CachedTokens
		}
		log.CacheReadTokens = cacheRead
		log.CacheWriteTokens = usageTracker.CacheWriteTokens
		log.Cost = calculateCost(model, usageTracker.PromptTokens, usageTracker.CompletionTokens, cacheRead, usageTracker.CacheWriteTokens)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, usageTracker.CompletionTokens)

		sendMuhiyaMetaChunk(w, &log, model.Name)
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		var anthResp AnthropicResponse
		_ = json.Unmarshal(respBody, &anthResp)

		oaiResponse := TranslateAnthropicToOpenAIResponse(&anthResp, oaiReq.Model)
		translated, _ := json.Marshal(oaiResponse)

		log.StatusCode = http.StatusOK
		log.InputTokens = oaiResponse.Usage.PromptTokens
		log.OutputTokens = oaiResponse.Usage.CompletionTokens
		cacheRead := anthResp.Usage.CacheReadInputTokens
		cacheWrite := anthResp.Usage.CacheCreationInputTokens
		log.CacheReadTokens = cacheRead
		log.CacheWriteTokens = cacheWrite
		log.Cost = calculateCost(model, log.InputTokens, log.OutputTokens, cacheRead, cacheWrite)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, log.OutputTokens)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(translated)
	}
}

func (h *ProxyHandler) proxyAnthropicToOpenAI(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	oaiReq, err := TranslateAnthropicToOpenAI(anthReq, model.TargetModel)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadRequest, "Payload translation error: "+err.Error(), "invalid_request_error", &log, startTime)
		return
	}
	newBody, _ := json.Marshal(oaiReq)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") && !strings.HasSuffix(url, "/completions") {
		url += "/chat/completions"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Upstream connection failed: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, &log, startTime)
		return
	}

	if anthReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		reader := bufio.NewReader(resp.Body)
		var usageTracker AnthropicUsage
		msgID := "msg_" + uuid.New().String()

		normalClose := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			anthChunkBytes, eventType, done, err := TranslateOpenAIChunkToAnthropic(line, msgID, anthReq.Model, &usageTracker)
			if err != nil {
				continue
			}
			if len(anthChunkBytes) > 0 {
				w.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(anthChunkBytes))))
				flusher.Flush()
			}
			if done {
				normalClose = true
				w.Write([]byte("event: message_stop\ndata: {\"type\": \"message_stop\"}\n\n"))
				flusher.Flush()
				break
			}
		}

		if !normalClose {
			w.Write([]byte("event: error\ndata: {\"type\": \"error\", \"error\": {\"type\": \"api_error\", \"message\": \"Upstream connection disconnected prematurely.\"}}\n\n"))
			flusher.Flush()
		}

		log.StatusCode = http.StatusOK
		log.InputTokens = usageTracker.InputTokens
		log.OutputTokens = usageTracker.OutputTokens
		log.CacheReadTokens = usageTracker.CacheReadInputTokens
		log.Cost = calculateCost(model, usageTracker.InputTokens, usageTracker.OutputTokens, usageTracker.CacheReadInputTokens, 0)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, usageTracker.OutputTokens)

		sendMuhiyaMetaChunk(w, &log, model.Name)
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		var oaiResp OpenAIResponse
		_ = json.Unmarshal(respBody, &oaiResp)

		anthResponse := TranslateOpenAIToAnthropicResponse(&oaiResp, anthReq.Model)
		translated, _ := json.Marshal(anthResponse)

		log.StatusCode = http.StatusOK
		log.InputTokens = oaiResp.Usage.PromptTokens
		log.OutputTokens = oaiResp.Usage.CompletionTokens
		cacheRead := 0
		if oaiResp.Usage.PromptTokensDetails != nil {
			cacheRead = oaiResp.Usage.PromptTokensDetails.CachedTokens
		}
		log.CacheReadTokens = cacheRead
		log.Cost = calculateCost(model, log.InputTokens, log.OutputTokens, cacheRead, 0)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, log.OutputTokens)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(translated)
	}
}

func (h *ProxyHandler) proxyAnthropicToAnthropic(w http.ResponseWriter, r *http.Request, origBody []byte, anthReq *AnthropicRequest, model *db.Model, provider *db.Provider, log db.RequestLog, startTime time.Time) {
	var bodyMap map[string]interface{}
	_ = json.Unmarshal(origBody, &bodyMap)
	bodyMap["model"] = model.TargetModel

	newBody, _ := json.Marshal(bodyMap)
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/v1/messages") && !strings.HasSuffix(url, "/messages") {
		url += "/v1/messages"
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(newBody))
	if err != nil {
		h.logAndWriteError(w, http.StatusInternalServerError, "Failed to create upstream request", "api_error", &log, startTime)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		h.logAndWriteError(w, http.StatusBadGateway, "Upstream connection failed: "+err.Error(), "api_error", &log, startTime)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		h.logFailedUpstream(w, resp.StatusCode, respBody, &log, startTime)
		return
	}

	if anthReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		reader := bufio.NewReader(resp.Body)
		var usageTracker AnthropicUsage

		normalClose := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			w.Write([]byte(line))
			flusher.Flush()

			// Parse usage out of chunks
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				dataStr := strings.TrimPrefix(line, "data:")
				var event map[string]interface{}
				if err := json.Unmarshal([]byte(dataStr), &event); err == nil {
					eventType, _ := event["type"].(string)
					if eventType == "message_stop" {
						normalClose = true
					} else if eventType == "message_start" {
						if message, ok := event["message"].(map[string]interface{}); ok {
							if usage, ok := message["usage"].(map[string]interface{}); ok {
								if in, ok := usage["input_tokens"].(float64); ok {
									usageTracker.InputTokens = int(in)
								}
								if cr, ok := usage["cache_read_input_tokens"].(float64); ok {
									usageTracker.CacheReadInputTokens = int(cr)
								}
							}
						}
					} else if eventType == "message_delta" {
						if usage, ok := event["usage"].(map[string]interface{}); ok {
							if out, ok := usage["output_tokens"].(float64); ok {
								usageTracker.OutputTokens = int(out)
							}
							if cw, ok := usage["cache_creation_input_tokens"].(float64); ok {
								usageTracker.CacheCreationInputTokens = int(cw)
							}
						}
					}
				}
			}
		}

		if !normalClose {
			w.Write([]byte("event: error\ndata: {\"type\": \"error\", \"error\": {\"type\": \"api_error\", \"message\": \"Upstream connection disconnected prematurely.\"}}\n\n"))
			flusher.Flush()
		}

		log.StatusCode = http.StatusOK
		log.InputTokens = usageTracker.InputTokens
		log.OutputTokens = usageTracker.OutputTokens
		log.CacheReadTokens = usageTracker.CacheReadInputTokens
		log.CacheWriteTokens = usageTracker.CacheCreationInputTokens
		log.Cost = calculateCost(model, log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.CacheWriteTokens)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, log.OutputTokens)

		sendMuhiyaMetaChunk(w, &log, model.Name)
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		var anthResp AnthropicResponse
		_ = json.Unmarshal(respBody, &anthResp)

		log.StatusCode = http.StatusOK
		log.InputTokens = anthResp.Usage.InputTokens
		log.OutputTokens = anthResp.Usage.OutputTokens
		cacheRead := anthResp.Usage.CacheReadInputTokens
		cacheWrite := anthResp.Usage.CacheCreationInputTokens
		log.CacheReadTokens = cacheRead
		log.CacheWriteTokens = cacheWrite
		log.Cost = calculateCost(model, log.InputTokens, log.OutputTokens, cacheRead, cacheWrite)
		log.LatencyMS = int(time.Since(startTime).Milliseconds())
		_ = h.db.InsertRequestLog(log)
		h.limiter.RecordTokens(log.VirtualKeyID, log.OutputTokens)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBody)
	}
}

// ------------------------------------------
// Model Discovery Endpoint (Anthropic Specification)
// ------------------------------------------
func (h *ProxyHandler) handleModelDiscovery(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	models, err := h.db.ListModels()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Database error listing models: "+err.Error(), "api_error")
		return
	}

	clientIsAnthropic := isAnthropicRequest(r)

	// If asking for a specific model details
	pathParts := strings.Split(r.URL.Path, "/models/")
	if len(pathParts) > 1 && pathParts[1] != "" {
		modelID := pathParts[1]
		var matchedModel *db.Model
		for _, m := range models {
			if m.Name == modelID && !m.Transcribe {
				matchedModel = &m
				break
			}
		}

		if matchedModel == nil {
			h.writeError(w, http.StatusNotFound, "Model not found: "+modelID, "not_found_error")
			return
		}

		if clientIsAnthropic {
			response := map[string]interface{}{
				"type":         "model",
				"id":           matchedModel.Name,
				"display_name": matchedModel.Name + " (via MuhiyaLLM)",
				"created_at":   matchedModel.CreatedAt.Format(time.RFC3339),
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(response)
		} else {
			response := map[string]interface{}{
				"id":       matchedModel.Name,
				"object":   "model",
				"created":  matchedModel.CreatedAt.Unix(),
				"owned_by": "MuhiyaLLM",
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(response)
		}
		return
	}

	// List all models
	if clientIsAnthropic {
		var data []map[string]interface{}
		for _, m := range models {
			if m.Status == "active" && !m.Transcribe {
				data = append(data, map[string]interface{}{
					"type":         "model",
					"id":           m.Name,
					"display_name": m.Name + " (via MuhiyaLLM)",
					"created_at":   m.CreatedAt.Format(time.RFC3339),
				})
			}
		}

		response := map[string]interface{}{
			"data":     data,
			"has_more": false,
			"first_id": nil,
			"last_id":  nil,
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	} else {
		var data []map[string]interface{}
		for _, m := range models {
			if m.Status == "active" && !m.Transcribe {
				data = append(data, map[string]interface{}{
					"id":       m.Name,
					"object":   "model",
					"created":  m.CreatedAt.Unix(),
					"owned_by": "MuhiyaLLM",
				})
			}
		}

		response := map[string]interface{}{
			"object": "list",
			"data":   data,
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}
}

// ------------------------------------------
// Common Error Logging Helpers
// ------------------------------------------

func (h *ProxyHandler) writeError(w http.ResponseWriter, code int, msg string, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	r := h.getRequest(w)
	if isAnthropicRequest(r) {
		anthErrType := errType
		if errType == "unauthorized" {
			anthErrType = "authentication_error"
		}
		errPayload := anthropicErrorPayload{
			Type: "error",
		}
		errPayload.Error.Type = anthErrType
		errPayload.Error.Message = msg
		_ = json.NewEncoder(w).Encode(errPayload)
		return
	}

	errPayload := map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"type":    errType,
		},
	}
	_ = json.NewEncoder(w).Encode(errPayload)
}

func (h *ProxyHandler) logAndWriteError(w http.ResponseWriter, code int, msg string, errType string, log *db.RequestLog, startTime time.Time) {
	log.StatusCode = code
	log.ErrorMessage = msg
	log.LatencyMS = int(time.Since(startTime).Milliseconds())
	_ = h.db.InsertRequestLog(*log)

	h.writeError(w, code, msg, errType)
}

func (h *ProxyHandler) logFailedUpstream(w http.ResponseWriter, statusCode int, respBytes []byte, log *db.RequestLog, startTime time.Time) {
	log.StatusCode = statusCode
	log.ErrorMessage = string(respBytes)
	log.LatencyMS = int(time.Since(startTime).Milliseconds())
	_ = h.db.InsertRequestLog(*log)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	r := h.getRequest(w)
	clientIsAnth := isAnthropicRequest(r)
	translated := translateErrorBytes(respBytes, clientIsAnth)
	w.Write(translated)
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	
	tokens := 0
	words := strings.Fields(text)
	
	for _, word := range words {
		isArabic := false
		for _, r := range word {
			if r >= 0x0600 && r <= 0x06FF { // Arabic unicode block
				isArabic = true
				break
			}
		}
		
		if isArabic {
			// Arabic words average ~2.5 tokens
			tokens += 3
		} else {
			// English words average ~1.3 tokens (approx 4 chars per token)
			wordLen := len(word)
			tokens += (wordLen + 3) / 4
		}
	}
	
	// Add tokens for common code punctuations that might be stripped by Fields
	punctuations := []string{"{", "}", "(", ")", "[", "]", ";", ",", ".", "=", "+", "-", "*", "/", "<", ">", "!", "&", "|"}
	for _, p := range punctuations {
		tokens += strings.Count(text, p)
	}
	
	// Add tokens for indentation (4 spaces = 1 token)
	spaces := strings.Count(text, " ")
	tokens += spaces / 4
	
	if tokens == 0 {
		return 1
	}
	return tokens
}

func calculateCost(model *db.Model, input, output, cacheRead, cacheWrite int) float64 {
	// Cost calculation supporting prompt caching tokens
	standardInput := input - cacheRead - cacheWrite
	if standardInput < 0 {
		standardInput = 0
	}
	writeCostPerMillion := model.CacheWriteCostPerMillion
	if writeCostPerMillion == 0 && cacheWrite > 0 {
		writeCostPerMillion = model.InputCostPerMillion
	}
	inputCost := (float64(standardInput) / 1000000.0) * model.InputCostPerMillion
	outputCost := (float64(output) / 1000000.0) * model.OutputCostPerMillion
	cacheReadCost := (float64(cacheRead) / 1000000.0) * model.CacheReadCostPerMillion
	cacheWriteCost := (float64(cacheWrite) / 1000000.0) * writeCostPerMillion
	return inputCost + outputCost + cacheReadCost + cacheWriteCost
}

func (h *ProxyHandler) serveTranscriptionClient(w http.ResponseWriter, r *http.Request, bodyBytes []byte, key *db.VirtualKey) {
	// Restore body to parse multipart form
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Max upload size 10MB
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Failed to parse multipart form: "+err.Error(), "invalid_request_error")
		return
	}

	modelName := r.FormValue("model")
	if modelName == "" {
		modelName = "whisper-1"
	}

	// Retrieve target model
	targetModel, err := h.db.GetModelByName(modelName)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Database error: "+err.Error(), "api_error")
		return
	}
	if targetModel == nil {
		h.writeError(w, http.StatusNotFound, fmt.Sprintf("Model '%s' not found or inactive", modelName), "invalid_request_error")
		return
	}

	// Retrieve provider
	provider, err := h.db.GetProvider(targetModel.ProviderID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Database error: "+err.Error(), "api_error")
		return
	}
	if provider == nil || provider.Status != "active" {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("Provider '%s' is inactive or missing", targetModel.ProviderID), "invalid_request_error")
		return
	}

	// Extract file
	file, header, err := r.FormFile("file")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "Failed to get file field: "+err.Error(), "invalid_request_error")
		return
	}
	defer file.Close()

	// Recreate multipart request to send upstream
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to create upstream multipart file: "+err.Error(), "api_error")
		return
	}
	if _, err := io.Copy(part, file); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to copy file data: "+err.Error(), "api_error")
		return
	}

	// Set target model
	if err := writer.WriteField("model", targetModel.TargetModel); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to write model field: "+err.Error(), "api_error")
		return
	}

	// Copy other form fields
	for k, vals := range r.MultipartForm.Value {
		if k != "model" && k != "file" && k != "durationSec" {
			for _, v := range vals {
				_ = writer.WriteField(k, v)
			}
		}
	}

	if err := writer.Close(); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to close multipart writer: "+err.Error(), "api_error")
		return
	}

	// Determine upstream URL
	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/audio/transcriptions") {
		url += "/audio/transcriptions"
	}

	startTime := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, &requestBody)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to create upstream request: "+err.Error(), "api_error")
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "Connection to upstream failed: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to read upstream response: "+err.Error(), "api_error")
		return
	}

	latencyMs := int(time.Since(startTime).Milliseconds())

	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// Extract text for log
	var textResult string
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(respBody, &jsonMap); err == nil {
		if t, ok := jsonMap["text"].(string); ok {
			textResult = t
		}
	}

	// Calculate cost using price_per_minute from targetModel
	durationSecStr := r.FormValue("durationSec")
	durationSec := 0.0
	if durationSecStr != "" {
		if d, err := strconv.ParseFloat(durationSecStr, 64); err == nil {
			durationSec = d
		}
	}

	cost := (durationSec / 60.0) * targetModel.PricePerMinute

	// Log request and deduct credits
	logEntry := db.RequestLog{
		ID:             "log-" + uuid.New().String(),
		VirtualKeyID:   key.ID,
		UserID:         key.UserID,
		ModelID:        targetModel.ID,
		ProviderID:     targetModel.ProviderID,
		RequestPath:    "/v1/audio/transcriptions",
		StatusCode:     resp.StatusCode,
		InputTokens:    int(durationSec),
		OutputTokens:   len(strings.Fields(textResult)),
		Cost:           cost,
		LatencyMS:      latencyMs,
		ClientApp:      "MuhiyaChat",
		RequestedModel: targetModel.Name,
		Complexity:     "direct",
		CreatedAt:      time.Now(),
	}

	if err := h.db.InsertRequestLog(logEntry); err != nil {
		log.Printf("[TRANSCRIPTION-ERROR] Failed to insert request log: %v", err)
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}

func sendMuhiyaMetaChunk(w http.ResponseWriter, log *db.RequestLog, modelName string) {
	if log.ClientApp != "MuhiyaChat" {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	metaBytes, err := json.Marshal(map[string]interface{}{
		"id":     log.ID,
		"object": "chat.completion.chunk",
		"model":  modelName,
		"usage": map[string]interface{}{
			"prompt_tokens":     log.InputTokens,
			"completion_tokens": log.OutputTokens,
			"total_tokens":      log.InputTokens + log.OutputTokens,
		},
		"muhiya_log": map[string]interface{}{
			"cost":   log.Cost,
			"log_id": log.ID,
		},
	})
	if err == nil {
		w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(metaBytes))))
		flusher.Flush()
	}
}
