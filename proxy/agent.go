package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gateway/db"
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
	"web_search":            "Searching the web",
	"football_live_scores":  "Checking live scores",
	"football_fixtures":     "Looking up fixtures",
	"football_standings":    "Fetching the league table",
	"football_team":         "Looking up team info",
	"football_head_to_head": "Comparing head-to-head",
}

// shouldRunAgentLoop reports whether this request should use the native agent
// loop. It is deliberately conservative: MuhiyaChat only, streaming only, the
// client must not have supplied its own tools, and at least one tool key must
// be configured.
func (h *ProxyHandler) shouldRunAgentLoop(r *http.Request, oaiReq *OpenAIRequest, settings ToolSettings) bool {
	return getClientAppName(r) == "MuhiyaChat" &&
		oaiReq.Stream &&
		len(oaiReq.Tools) == 0 &&
		settings.ToolsEnabled()
}

// serveMuhiyaAgent runs the agent loop for an OpenAI-format request. It resolves
// the model exactly like the normal router, then, if the resolved provider is
// OpenAI-format, runs the loop. If the provider is Anthropic-format it returns
// false so the caller can fall back to the standard proxy path.
func (h *ProxyHandler) serveMuhiyaAgent(w http.ResponseWriter, r *http.Request, oaiReq *OpenAIRequest, key *db.VirtualKey, settings ToolSettings) (handled bool) {
	model, provider, complexity, err := h.resolveAgentModel(oaiReq)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Routing error: "+err.Error(), "api_error")
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
	if err := h.limiter.CheckLimit(key, promptTokens); err != nil {
		h.writeError(w, http.StatusTooManyRequests, "Limit exceeded: "+err.Error(), "rate_limit_error")
		return true
	}

	requestedModel := oaiReq.Model
	if oaiReq.Model == "muhiya-ai-router" {
		requestedModel = "muhiya-ai-router"
	}
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
		CreatedAt:      startTime,
	}

	tc := &ToolContext{DB: h.db, Settings: settings, teamIDCache: map[string]int{}, leagueIDCache: map[string]int{}}
	tools := BuildToolSchemas(settings)

	// Build the working message list: inject tool-use guidance so the model
	// knows the tools exist and how to cite results.
	messages := injectToolGuidance(oaiReq.Messages)

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
	var streamError string

	for iter := 0; iter < maxAgentIterations; iter++ {
		turn, err := h.streamOpenAITurn(r, w, flusher, msgID, model, provider, messages, tools)
		totalInput += turn.usage.PromptTokens
		totalOutput += turn.usage.CompletionTokens
		if turn.usage.PromptTokensDetails != nil {
			totalCacheRead += turn.usage.PromptTokensDetails.CachedTokens
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
			Role:      "assistant",
			Content:   turn.assistantText,
			ToolCalls: turn.toolCalls,
		})

		// Execute each tool call in order, streaming status + result events.
		for _, call := range turn.toolCalls {
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

	// Emit terminal marker.
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()

	// Finalize billing: one aggregated request log for the whole loop.
	if totalInput == 0 {
		totalInput = promptTokens
	}
	reqLog.StatusCode = http.StatusOK
	if streamError != "" {
		reqLog.ErrorMessage = streamError
	}
	reqLog.InputTokens = totalInput
	reqLog.OutputTokens = totalOutput
	reqLog.CacheReadTokens = totalCacheRead
	reqLog.Cost = calculateCost(model, totalInput, totalOutput, totalCacheRead, 0)
	reqLog.LatencyMS = int(time.Since(startTime).Milliseconds())
	_ = h.db.InsertRequestLog(reqLog)
	h.limiter.RecordTokens(reqLog.VirtualKeyID, totalOutput)

	sendMuhiyaMetaChunk(w, &reqLog, model.Name)
	return true
}

// resolveAgentModel picks the model + provider for the request, honoring the
// router alias exactly like serveOpenAIClient does.
func (h *ProxyHandler) resolveAgentModel(oaiReq *OpenAIRequest) (*db.Model, *db.Provider, string, error) {
	complexity := "direct"
	var model *db.Model
	var err error

	if oaiReq.Model == "muhiya-ai-router" {
		complexity = AnalyzePromptComplexity(oaiReq.Messages)
		needsVision := requestNeedsVision(oaiReq.Messages)
		thinkingRequested := oaiReq.Thinking != nil || oaiReq.ReasoningEffort != nil
		model, err = h.RouteToModel(complexity, needsVision, thinkingRequested)
		if err != nil {
			return nil, nil, complexity, err
		}
	} else {
		model, err = h.db.GetModelByName(oaiReq.Model)
		if err != nil {
			return nil, nil, complexity, err
		}
		if model == nil {
			return nil, nil, complexity, fmt.Errorf("model '%s' not found or inactive", oaiReq.Model)
		}
	}

	provider, err := h.db.GetProvider(model.ProviderID)
	if err != nil {
		return nil, nil, complexity, err
	}
	if provider == nil || provider.Status != "active" {
		return nil, nil, complexity, fmt.Errorf("provider for model '%s' is unavailable", model.Name)
	}
	return model, provider, complexity, nil
}

func requestNeedsVision(messages []OpenAIMessage) bool {
	for _, msg := range messages {
		if arr, ok := msg.Content.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					if m["type"] == "image_url" {
						return true
					}
				}
			}
		}
	}
	return false
}

// turnResult captures what a single upstream streaming turn produced.
type turnResult struct {
	assistantText string
	toolCalls     []OpenAIToolCall
	usage         OpenAIUsage
	finishReason  string
}

// streamOpenAITurn performs one streaming upstream call. It relays text and
// reasoning deltas to the client as OpenAI chunks, accumulates any tool calls
// (which are NOT forwarded to the client), and returns the turn result.
func (h *ProxyHandler) streamOpenAITurn(r *http.Request, w http.ResponseWriter, flusher http.Flusher, msgID string, model *db.Model, provider *db.Provider, messages []OpenAIMessage, tools []OpenAITool) (turnResult, error) {
	var res turnResult

	temperature := 0.7
	maxTokens := 4096
	toolChoice := interface{}("auto")
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
	body, _ := json.Marshal(upstreamReq)

	url := strings.TrimSuffix(provider.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") && !strings.HasSuffix(url, "/completions") {
		url += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return res, fmt.Errorf("failed to build upstream request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

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
	reader := bufio.NewReader(resp.Body)

	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
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
	res.toolCalls = accum.finalize()
	return res, nil
}

// injectToolGuidance appends tool-usage guidance to the system prompt (or adds
// a system message if none exists) without disturbing the rest of the history.
func injectToolGuidance(messages []OpenAIMessage) []OpenAIMessage {
	guidance := "You have live tools available: `web_search` for current information (news, prices, weather, anything recent or uncertain) and the `football_*` tools for real-time football data (live scores, fixtures, standings, teams, head-to-head). When a question is time-sensitive, factual, or about football, CALL the appropriate tool instead of guessing. After a web_search, cite sources inline as [1], [2]. Never invent scores, standings, or facts."

	out := make([]OpenAIMessage, len(messages))
	copy(out, messages)

	for i := range out {
		if out[i].Role == "system" {
			if s, ok := out[i].Content.(string); ok {
				out[i].Content = s + "\n\n" + guidance
				return out
			}
		}
	}
	// No string system message found — prepend one.
	return append([]OpenAIMessage{{Role: "system", Content: guidance}}, out...)
}

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
