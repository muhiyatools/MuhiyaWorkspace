package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"gateway/db"
	"gateway/pricing"
	"gateway/upstreamurl"
	"github.com/google/uuid"
)

// ============================================================================
// MuhiyaChat native agent loop.
// ----------------------------------------------------------------------------
// When a MuhiyaChat request arrives and tools are configured, the gateway runs
// a multi-turn tool loop instead of a plain proxy:
//
//   inject tool schemas -> stream upstream turn -> if the model asks for a
//   tool, execute it in-gateway, stream a status + result event to the UI,
//   append the result, and loop -> otherwise stream the final answer.
//
// Only OpenAI-format upstreams (DeepSeek, GPT-4o, ... — i.e. provider.BaseURL
// set) run the loop. Anthropic-format upstreams fall through to the normal
// proxy path (handled by the caller). All non-MuhiyaChat traffic is untouched.
// ============================================================================

const maxAgentIterations = 5

// toolStatusLabels gives each tool a short human label for the UI status line.
var toolStatusLabels = map[string]string{
	"web_search": "Searching the web",
}

// shouldRunAgentLoop reports whether this request should use the native agent
// loop. It is deliberately conservative: MuhiyaChat only, streaming only, the
// client must not have supplied its own tools, and at least one tool key must
// be configured.
func (h *ProxyHandler) shouldRunAgentLoop(r *http.Request, oaiReq *OpenAIRequest, settings ToolSettings) bool {
	// Keep inference as one client-controlled generation by default. The
	// historical gateway loop could issue up to five hidden model calls, making
	// request cost and cancellation impossible to reason about.
	// It remains behind an explicit compatibility flag for MuhiyaChat while the
	// client-side tool loop is the production architecture.
	if !isTruthy(os.Getenv("ENABLE_GATEWAY_AGENT_LOOP")) {
		return false
	}
	if oaiReq.WebSearch != nil && !*oaiReq.WebSearch {
		return false
	}
	return getClientAppName(r) == "MuhiyaChat" &&
		oaiReq.Stream &&
		len(oaiReq.Tools) == 0 &&
		settings.ToolsEnabled()
}

// serveMuhiyaAgent runs the agent loop for an OpenAI-format request. It resolves
// the model exactly like the normal path, then, if the resolved provider is
// OpenAI-format, runs the loop. If the provider is Anthropic-format it returns
// false so the caller can fall back to the standard proxy path.
func (h *ProxyHandler) serveMuhiyaAgent(w http.ResponseWriter, r *http.Request, oaiReq *OpenAIRequest, key *db.VirtualKey, settings ToolSettings) (handled bool) {
	thinkingLevel := ResolveThinkingLevel(r, oaiReq.ReasoningEffort)
	model, provider, complexity, err := h.resolveAgentModel(oaiReq, thinkingLevel)
	if err != nil {
		// An availability failure is a 503, not a 500. The error string carries
		// an actionable message; the client maps routing_unavailable to a
		// friendly bubble.
		h.writeError(w, http.StatusServiceUnavailable, "Routing error: "+err.Error(), "routing_unavailable")
		return true
	}
	// The loop only supports OpenAI-format upstreams. Let Anthropic-format
	// providers use the normal (non-tool) streaming path.
	if provider.BaseURL == "" {
		return false
	}

	// Rate-limit check up front, mirroring the normal path.
	var textBuilder strings.Builder
	for _, m := range oaiReq.Messages {
		textBuilder.WriteString(GetMessageContentString(m.Content))
	}
	promptTokens := estimateTokens(textBuilder.String())
	agentOutputLimit, agentClientSetLimit := requestedOpenAIOutput(oaiReq, model)
	if err := h.limiter.CheckLimit(key, promptTokens+rateLimitOutputEstimate(agentOutputLimit, agentClientSetLimit)*maxAgentIterations); err != nil {
		// Parity with the proxy path: this used to answer a bare 429 with no
		// Retry-After, so a client here could not even back off correctly.
		h.writeLimitError(w, err)
		return true
	}

	requestedModel := oaiReq.Model
	startTime := time.Now()
	reqLog := db.RequestLog{
		ID:             uuid.New().String(),
		VirtualKeyID:   key.ID,
		UserID:         key.UserID,
		ModelID:        model.ID,
		ProviderID:     provider.ID,
		RequestPath:    r.URL.Path,
		InputTokens:    promptTokens,
		ClientApp:      "MuhiyaChat",
		RequestedModel: requestedModel,
		Complexity:     complexity,
		ThinkingLevel:  thinkingLevel,
		CreatedAt:      startTime,
	}

	tc := &ToolContext{DB: h.db, Settings: settings, Complexity: complexity}
	tools := BuildToolSchemas(settings)

	sourcesSkill := h.loadSearchSourcesSkill()
	messages := injectToolGuidance(oaiReq.Messages, sourcesSkill)
	// Targets must never receive media parts they cannot accept: DeepSeek's API
	// accepts text content only and 400s on image_url parts, so an unsupported
	// attachment anywhere in a conversation's history would break every later
	// turn on that model. Each dropped part becomes a stable text note.
	// (Router-selected requests keep their media — detectMediaNeedsOpenAI routes
	// them to a model that supports every modality present.)
	messages = stripUnsupportedMediaParts(messages, model)

	// Set up the SSE stream once.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "Streaming unsupported", "api_error")
		return true
	}

	msgID := "chatcmpl-" + uuid.New().String()
	var totalInput, totalOutput, totalCacheRead int
	var totalCacheMiss int64
	cacheMissReported := false
	var streamError string
	hasSearched := false

	for iter := 0; iter < maxAgentIterations; iter++ {
		// tools stays byte-identical across every iteration of this loop.
		// It used to be set to nil after the first web_search, which shrank
		// the rendered tool block and busted the provider's prefix cache
		// turn to turn. The single-search rule is now enforced by declining
		// a repeat call (below) instead of hiding the tool definition.
		turn, err := h.streamOpenAITurn(r, w, flusher, msgID, model, provider, messages, tools, thinkingLevel)
		// Cross-model failover was removed (single-active-model invariant): a
		// transient upstream failure is reported to the caller rather than
		// rerouted to a different model, which would silently substitute a
		// model mid-session and bust the upstream prefix cache.
		totalInput += turn.usage.PromptTokens
		totalOutput += turn.usage.CompletionTokens
		totalCacheRead += turn.usage.CacheReadTokensFor(provider.BaseURL, model.TargetModel)
		if miss := turn.usage.CacheMissTokensFor(provider.BaseURL, model.TargetModel); miss != nil {
			totalCacheMiss += *miss
			cacheMissReported = true
		}
		if err != nil {
			streamError = err.Error()
			writeSSEJSON(w, flusher, map[string]interface{}{
				"error": map[string]interface{}{"message": err.Error(), "type": "api_error"},
			})
			break
		}

		// No tool calls -> the model produced its final answer (already streamed).
		if len(turn.toolCalls) == 0 {
			break
		}

		// Append the assistant turn (with its tool calls) to the conversation.
		messages = append(messages, OpenAIMessage{
			Role:             "assistant",
			Content:          turn.assistantText,
			ReasoningContent: turn.reasoningContent,
			ToolCalls:        turn.toolCalls,
		})

		// Execute each tool call in order, streaming status + result events.
		for _, call := range turn.toolCalls {
			if call.Function.Name == "web_search" && hasSearched {
				// Decline instead of removing the tool from later turns:
				// keeps the tools array (and the cached prefix) byte-stable
				// for the rest of the loop.
				writeSSEJSON(w, flusher, map[string]interface{}{
					"muhiya_tool": map[string]interface{}{
						"status": "skipped",
						"tool":   call.Function.Name,
						"label":  "Already searched this turn",
					},
				})
				messages = append(messages, OpenAIMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "A web_search was already performed this turn. Use its results; do not search again.",
				})
				continue
			}
			if call.Function.Name == "web_search" {
				hasSearched = true
			}
			label := toolStatusLabels[call.Function.Name]
			if label == "" {
				label = "Working"
			}
			writeSSEJSON(w, flusher, map[string]interface{}{
				"muhiya_tool": map[string]interface{}{
					"status": "start",
					"tool":   call.Function.Name,
					"label":  label,
				},
			})

			exec := tc.ExecuteTool(call.Function.Name, call.Function.Arguments)
			if exec.ClientEvent != nil {
				writeSSEJSON(w, flusher, exec.ClientEvent)
			}
			writeSSEJSON(w, flusher, map[string]interface{}{
				"muhiya_tool": map[string]interface{}{
					"status": "done",
					"tool":   call.Function.Name,
				},
			})

			// Feed the tool result back to the model.
			messages = append(messages, OpenAIMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    exec.LLMContent,
			})
		}

		if iter == maxAgentIterations-1 {
			// Safety valve: force a final answer turn without tools next time
			// would exceed the cap — emit a note and stop.
			writeSSEJSON(w, flusher, map[string]interface{}{
				"muhiya_tool": map[string]interface{}{"status": "limit", "label": "Finalizing"},
			})
		}
	}

	// Finalize billing: one aggregated request log for the whole loop.
	if totalInput == 0 {
		totalInput = promptTokens
	}
	reqLog.StatusCode = http.StatusOK
	if streamError != "" {
		reqLog.ErrorMessage = streamError
		reqLog.StatusCode = requestFailureStatus(requestFailure{
			requestErr: r.Context().Err(),
			message:    streamError,
		})
		reqLog.RequestStatus = db.RequestStatusForHTTP(reqLog.StatusCode)
	}
	reqLog.InputTokens = totalInput
	reqLog.OutputTokens = totalOutput
	reqLog.CacheReadTokens = totalCacheRead
	if cacheMissReported {
		reqLog.CacheMissTokens = &totalCacheMiss
	}
	// Partial output from a failed stream stays visible in telemetry but is not
	// charged; a transport retry is accounted as a separate attempt.
	if streamError != "" {
		reqLog.Cost = 0
		reqLog.CostNanoUSD = 0
	} else {
		setCalculatedCost(&reqLog, model, pricing.ReportedUsage{
			PromptTokens:    int64(totalInput),
			OutputTokens:    int64(totalOutput),
			CacheReadTokens: int64(totalCacheRead),
		}, pricedAtFrom(r.Context()))
	}
	reqLog.LatencyMS = int(time.Since(startTime).Milliseconds())
	h.saveRequestLog(reqLog)

	sendMuhiyaMetaChunk(w, &reqLog, model.Name)
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	return true
}

// resolveAgentModel picks the exact model + provider for the request. The
// automatic model router was removed: the caller selects the concrete model for
// the session and the gateway forwards to it without cross-model fallback.
func (h *ProxyHandler) resolveAgentModel(oaiReq *OpenAIRequest, thinkingLevel string) (*db.Model, *db.Provider, string, error) {
	if oaiReq.Model == routerModelDeprecated {
		return nil, nil, "direct", fmt.Errorf("model '%s' is no longer supported; select a concrete model for the session", oaiReq.Model)
	}

	model, err := h.db.GetModelByName(oaiReq.Model)
	if err != nil {
		return nil, nil, "direct", err
	}
	if model == nil {
		return nil, nil, "direct", fmt.Errorf("model '%s' not found or inactive", oaiReq.Model)
	}

	provider, err := h.db.GetProvider(model.ProviderID)
	if err != nil {
		return nil, nil, "direct", err
	}
	if provider == nil || provider.Status != "active" {
		return nil, nil, "direct", fmt.Errorf("provider for model '%s' is unavailable", model.Name)
	}
	return model, provider, "direct", nil
}

// turnResult captures what a single upstream streaming turn produced.
type turnResult struct {
	assistantText    string
	reasoningContent string
	toolCalls        []OpenAIToolCall
	usage            OpenAIUsage
	finishReason     string
}

// streamOpenAITurn performs one streaming upstream call. It relays text and
// reasoning deltas to the client as OpenAI chunks, accumulates any tool calls
// (which are NOT forwarded to the client), and returns the turn result.
func (h *ProxyHandler) streamOpenAITurn(r *http.Request, w http.ResponseWriter, flusher http.Flusher, msgID string, model *db.Model, provider *db.Provider, messages []OpenAIMessage, tools []OpenAITool, thinkingLevel string) (turnResult, error) {
	var res turnResult

	temperature := 0.7
	maxTokens := 4096
	family := classifyUpstream(provider.BaseURL, model.TargetModel)
	var toolChoice interface{}
	if len(tools) > 0 {
		toolChoice = "auto"
	}
	upstreamReq := OpenAIRequest{
		Model:         model.TargetModel,
		Messages:      messages,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Stream:        true,
		StreamOptions: &OpenAIStreamOptions{IncludeUsage: true},
		Temperature:   &temperature,
		MaxTokens:     &maxTokens,
	}
	if family == famDeepseek {
		upstreamReq.MaxTokens = nil
	}
	structBody, _ := json.Marshal(upstreamReq)
	var bodyMap map[string]interface{}
	_ = json.Unmarshal(structBody, &bodyMap)
	ApplyThinkingOpenAI(bodyMap, provider.BaseURL, model.TargetModel, thinkingLevel, model.SupportsThinking)
	if family == famDeepseek {
		conditionDeepSeekChatCompletion(bodyMap)
	}
	sanitizeUpstreamIdentity(bodyMap, family, h.identitySecret, "")
	// Document parts to OpenRouter pin the free parser. Avoid resolving its
	// setting for direct providers, whose request path does not need it.
	if provider.ID == "openrouter" {
		maybeInjectOpenRouterPDFParser(bodyMap, provider, h.openRouterPDFEngine())
	}
	body, _ := json.Marshal(bodyMap)

	url := upstreamurl.ChatCompletions(provider.BaseURL)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return res, fmt.Errorf("failed to build upstream request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	setOpenRouterHeaders(req, provider, r)

	resp, err := httpClient.Do(req)
	if err != nil {
		return res, fmt.Errorf("upstream connection failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return res, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	accum := newToolCallAccumulator()
	var textBuf strings.Builder
	var reasoningBuf strings.Builder
	reader := bufio.NewReader(resp.Body)
	resetIdle, stopIdle, _ := armIdleWatchdog(resp.Body, streamIdleTimeout)
	defer stopIdle()

	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			resetIdle()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				dataStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if dataStr == "[DONE]" {
					break
				}
				if dataStr != "" {
					var chunk OpenAIChunk
					if json.Unmarshal([]byte(dataStr), &chunk) == nil {
						if chunk.Usage != nil {
							res.usage = *chunk.Usage
						}
						if len(chunk.Choices) > 0 {
							choice := chunk.Choices[0]
							delta := choice.Delta
							// Relay assistant-visible text + reasoning to the client.
							if delta.Content != "" {
								textBuf.WriteString(delta.Content)
								writeClientDelta(w, flusher, msgID, model.Name, OpenAIDelta{Content: delta.Content})
							}
							if delta.ReasoningContent != "" {
								reasoningBuf.WriteString(delta.ReasoningContent)
								writeClientDelta(w, flusher, msgID, model.Name, OpenAIDelta{ReasoningContent: delta.ReasoningContent})
							}
							// Accumulate tool calls silently.
							if len(delta.ToolCalls) > 0 {
								accum.add(delta.ToolCalls)
							}
							if choice.FinishReason != nil {
								res.finishReason = *choice.FinishReason
							}
						}
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	res.assistantText = textBuf.String()
	res.reasoningContent = reasoningBuf.String()
	res.toolCalls = accum.finalize()
	return res, nil
}

// injectToolGuidance appends tool-usage guidance to the system prompt (or adds
// a system message if none exists) without disturbing the rest of the history.
func injectToolGuidance(messages []OpenAIMessage, sourcesSkill string) []OpenAIMessage {
	guidance := "You have a live tool available: `web_search` for current information. When a question is time-sensitive, factual, or requires real-time information, CALL the `web_search` tool instead of guessing. After a web_search, cite sources inline as [1], [2]. Never invent facts.\n\n"
	if sourcesSkill != "" {
		guidance += "IMPORTANT: When calling `web_search`, you MUST format your search query to target the trusted sources defined below using the `site:domain` Google Search syntax (e.g., `query (site:imf.org OR site:worldbank.org)`). If no specific topic matches, search normally. Only search once.\n\n" + sourcesSkill
	}

	out := make([]OpenAIMessage, len(messages))
	copy(out, messages)

	for i := range out {
		if out[i].Role == "system" {
			if s, ok := out[i].Content.(string); ok {
				out[i].Content = s + "\n\n" + guidance
				return out
			}
			// Structured (array) system content: append a text block instead
			// of falling through to prepend a whole new system message -
			// that changed the message array's shape depending on whether
			// the client sent a string or array system message.
			if arr, ok := out[i].Content.([]interface{}); ok {
				out[i].Content = append(arr, map[string]interface{}{"type": "text", "text": guidance})
				return out
			}
		}
	}
	// No system message found at all — prepend one.
	return append([]OpenAIMessage{{Role: "system", Content: guidance}}, out...)
}

func (h *ProxyHandler) loadSearchSourcesSkill() string {
	// 1. Try to read from db
	val, _ := h.db.GetSetting("muhiya_chat_search_sources")
	if val != "" {
		return val
	}

	// 2. Try to read from file
	var content []byte
	var err error
	paths := []string{
		"proxy/skills/search_sources.md",
		"./proxy/skills/search_sources.md",
		"skills/search_sources.md",
		"../proxy/skills/search_sources.md",
	}
	for _, p := range paths {
		content, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}

	skillContent := ""
	if len(content) > 0 {
		skillContent = string(content)
	} else {
		skillContent = defaultSearchSourcesSkill
	}

	// Save to db so it is editable
	_ = h.db.SetSetting("muhiya_chat_search_sources", skillContent)
	return skillContent
}

const defaultSearchSourcesSkill = `# Trusted Search Sources & Scoping Guidance

Use these domain targets (with ` + "`site:domain`" + `) to retrieve high-quality, relevant data based on query categories:

## 1. Biography, History & General Reference
*   **Target Domains**: ` + "`wikipedia.org`, `britannica.com`, `imdb.com`, `biography.com`" + `
*   **Intent**: Biography queries (\"Who is X\"), historical events, entities, cast details, definitions, general facts.
*   **Query Example**: ` + "`site:wikipedia.org OR site:britannica.com albert einstein biography`" + `

## 2. Economy, Finance & Markets
*   **Target Domains**: ` + "`imf.org`, `worldbank.org`, `tradingeconomics.com`, `bloomberg.com`, `reuters.com`, `investopedia.com`, `finance.yahoo.com`" + `
*   **Intent**: GDP figures, inflation, interest rates, currency exchange, company revenue, financial market trends.
*   **Query Example**: ` + "`site:imf.org OR site:tradingeconomics.com egypt gdp growth`" + `

## 3. Sports & Football
*   **Target Domains**: ` + "`fifa.com`, `uefa.com`, `espn.com`, `skysports.com`, `goal.com`, `whoscored.com`, `transfermarkt.com`, `kooora.com`" + `
*   **Intent**: Real-time scores, standings, league tables, fixture schedules, transfer news, player stats.
*   **Query Example**: ` + "`site:kooora.com OR site:goal.com el ahly match results`" + `

## 4. Politics, Government & Global News
*   **Target Domains**: ` + "`reuters.com`, `apnews.com`, `bbc.com`, `aljazeera.com`, `cnn.com`" + `
*   **Intent**: Elections, state policies, treaties, global conflicts, governmental updates, breaking news.
*   **Query Example**: ` + "`site:reuters.com OR site:apnews.com france presidential election results`" + `

## 5. Technology, Coding & Science
*   **Target Domains**: ` + "`techcrunch.com`, `wired.com`, `theverge.com`, `github.com`, `nature.com`, `arxiv.org`" + `
*   **Intent**: AI/ML research papers, programming repositories, hardware specs, space/physics news, tech announcements.
*   **Query Example**: ` + "`site:arxiv.org machine learning transformer architecture`" + ``

// --- Streaming helpers ------------------------------------------------------

// writeClientDelta emits a single OpenAI-format chunk carrying a content or
// reasoning delta to the MuhiyaChat client.
func writeClientDelta(w http.ResponseWriter, flusher http.Flusher, msgID, modelName string, delta OpenAIDelta) {
	chunk := OpenAIChunk{
		ID:      msgID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []OpenAIChunkChoice{{Index: 0, Delta: delta}},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	w.Write([]byte("data: " + string(b) + "\n\n"))
	flusher.Flush()
}

// writeSSEJSON writes an arbitrary JSON object as one SSE data event.
func writeSSEJSON(w http.ResponseWriter, flusher http.Flusher, payload map[string]interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	w.Write([]byte("data: " + string(b) + "\n\n"))
	flusher.Flush()
}

// --- Tool-call accumulation -------------------------------------------------

// toolCallAccumulator stitches together tool-call fragments that arrive across
// streaming deltas. OpenAI tags each fragment with an `index`; when absent we
// fall back to a sequential heuristic (a new id/name starts a new call).
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*toolCallBuf
	next  int
}

type toolCallBuf struct {
	id   string
	name string
	args strings.Builder
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIdx: map[int]*toolCallBuf{}}
}

func (a *toolCallAccumulator) add(deltas []OpenAIToolCall) {
	for _, d := range deltas {
		idx := a.next
		if d.Index != nil {
			idx = *d.Index
		} else if d.ID == "" && d.Function.Name == "" {
			// Continuation fragment with no index: attach to the most recent call.
			if len(a.order) > 0 {
				idx = a.order[len(a.order)-1]
			}
		}
		buf, exists := a.byIdx[idx]
		if !exists {
			buf = &toolCallBuf{}
			a.byIdx[idx] = buf
			a.order = append(a.order, idx)
			if idx >= a.next {
				a.next = idx + 1
			}
		}
		if d.ID != "" {
			buf.id = d.ID
		}
		if d.Function.Name != "" {
			buf.name = d.Function.Name
		}
		if d.Function.Arguments != "" {
			buf.args.WriteString(d.Function.Arguments)
		}
	}
}

func (a *toolCallAccumulator) finalize() []OpenAIToolCall {
	var calls []OpenAIToolCall
	for _, idx := range a.order {
		buf := a.byIdx[idx]
		if buf.name == "" {
			continue
		}
		id := buf.id
		if id == "" {
			id = "call_" + uuid.New().String()
		}
		calls = append(calls, OpenAIToolCall{
			ID:   id,
			Type: "function",
			Function: OpenAIFunctionCall{
				Name:      buf.name,
				Arguments: buf.args.String(),
			},
		})
	}
	return calls
}
