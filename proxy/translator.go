package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ==========================================
// OpenAI Protocol Structures
// ==========================================

type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content,omitempty"` // string or array
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"` // for role: tool
	// ReasoningContent round-trips DeepSeek's thinking-mode key on assistant
	// tool-call turns. The OpenAI<->OpenAI passthrough no longer decodes
	// into this struct at all (it forwards the client's original bytes
	// verbatim - see serveOpenAIClient), so this field only matters for
	// typed paths: Anthropic translation and the MuhiyaChat agent loop.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type OpenAIToolCall struct {
	Index    *int               `json:"index,omitempty"` // set on streaming deltas; nil on complete calls
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function OpenAIFunctionCall `json:"function"`
}

type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type OpenAITool struct {
	Type     string            `json:"type"` // "function"
	Function OpenAIFunctionDef `json:"function"`
}

type OpenAIFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"` // JSON Schema object
}

type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type OpenAIRequest struct {
	Model               string               `json:"model"`
	Messages            []OpenAIMessage      `json:"messages"`
	Temperature         *float64             `json:"temperature,omitempty"`
	MaxTokens           *int                 `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                 `json:"max_completion_tokens,omitempty"`
	Stream              bool                 `json:"stream,omitempty"`
	StreamOptions       *OpenAIStreamOptions `json:"stream_options,omitempty"`
	Tools               []OpenAITool         `json:"tools,omitempty"`
	ToolChoice          interface{}          `json:"tool_choice,omitempty"`
	ReasoningEffort     *string              `json:"reasoning_effort,omitempty"`
	Thinking            *AnthropicThinking   `json:"thinking,omitempty"`
	WebSearch           *bool                `json:"web_search,omitempty"`
}

type PromptTokensDetail struct {
	CachedTokens int `json:"cached_tokens"`
}

type OpenAIUsage struct {
	PromptTokens        int                 `json:"prompt_tokens"`
	CompletionTokens    int                 `json:"completion_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	PromptTokensDetails *PromptTokensDetail `json:"prompt_tokens_details,omitempty"`
	// DeepSeek reports prefix-cache usage under its own names instead of (or
	// in addition to) the OpenAI prompt_tokens_details shape. Without these
	// fields every DeepSeek request logged 0 cache tokens even on real hits.
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
	CacheWriteTokens      int `json:"-"` // internal tracking
}

// CacheReadTokens returns the prompt tokens served from the provider's
// prefix cache, whichever dialect the upstream reported them in.
func (u *OpenAIUsage) CacheReadTokens() int {
	if u == nil {
		return 0
	}
	read := 0
	if u.PromptTokensDetails != nil {
		read = u.PromptTokensDetails.CachedTokens
	}
	if u.PromptCacheHitTokens > read {
		read = u.PromptCacheHitTokens
	}
	return read
}

const minimaxCacheThresholdTokens = 512

// CacheReadTokensFor applies provider-specific reporting guarantees without
// changing the legacy dialect parser. MiniMax only caches prompts at or above
// 512 tokens, so a contradictory sub-threshold cached count is ignored.
func (u *OpenAIUsage) CacheReadTokensFor(baseURL, targetModel string) int {
	read := u.CacheReadTokens()
	if u == nil || classifyUpstream(baseURL, targetModel) != famMiniMax {
		return read
	}
	if u.PromptTokens < minimaxCacheThresholdTokens {
		return 0
	}
	if read > u.PromptTokens {
		return u.PromptTokens
	}
	return read
}

// CacheMissTokensFor returns the provider-reported DeepSeek miss count or the
// MiniMax-derived miss (prompt minus cached). A nil result means unavailable,
// including every MiniMax prompt below its documented cache threshold.
func (u *OpenAIUsage) CacheMissTokensFor(baseURL, targetModel string) *int64 {
	if u == nil {
		return nil
	}
	if u.PromptCacheMissTokens > 0 {
		miss := int64(u.PromptCacheMissTokens)
		return &miss
	}
	if classifyUpstream(baseURL, targetModel) != famMiniMax || u.PromptTokens < minimaxCacheThresholdTokens || u.PromptTokensDetails == nil {
		return nil
	}
	miss := int64(u.PromptTokens - u.CacheReadTokensFor(baseURL, targetModel))
	return &miss
}

type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

type OpenAIDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type OpenAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []OpenAIChunkChoice `json:"choices"`
	Usage   *OpenAIUsage        `json:"usage,omitempty"`
}

// ==========================================
// Anthropic Protocol Structures
// ==========================================

type AnthropicSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/jpeg"
	Data      string `json:"data"`       // base64 data
}

type AnthropicContent struct {
	Type      string           `json:"type"` // "text", "image", "tool_use", "tool_result", "thinking", "redacted_thinking"
	Text      string           `json:"text,omitempty"`
	Thinking  string           `json:"thinking,omitempty"` // For Claude 3.7
	Signature string           `json:"signature,omitempty"`
	ID        string           `json:"id,omitempty"`          // tool_use ID
	Name      string           `json:"name,omitempty"`        // tool_use Name
	Input     interface{}      `json:"input,omitempty"`       // tool_use Input
	ToolUseID string           `json:"tool_use_id,omitempty"` // tool_result ID
	Content   interface{}      `json:"content,omitempty"`     // tool_result Content (string or array)
	IsError   bool             `json:"is_error,omitempty"`    // tool_result IsError
	Source    *AnthropicSource `json:"source,omitempty"`
}

type AnthropicMessage struct {
	Role    string             `json:"role"` // "user", "assistant"
	Content []AnthropicContent `json:"content"`
}

func (m *AnthropicMessage) UnmarshalJSON(data []byte) error {
	var aux struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Role = aux.Role

	if aux.Content == nil {
		m.Content = nil
		return nil
	}

	switch v := aux.Content.(type) {
	case string:
		m.Content = []AnthropicContent{{Type: "text", Text: v}}
	case []interface{}:
		bytes, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(bytes, &m.Content); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid type for content: %T", v)
	}
	return nil
}

type AnthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema"` // JSON Schema
}

type AnthropicSystem string

func (s *AnthropicSystem) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = AnthropicSystem(str)
		return nil
	}
	var arr []interface{}
	if err := json.Unmarshal(data, &arr); err == nil {
		var parts []string
		for _, item := range arr {
			switch v := item.(type) {
			case string:
				parts = append(parts, v)
			case map[string]interface{}:
				if t, ok := v["type"].(string); ok && t == "text" {
					if text, ok := v["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		*s = AnthropicSystem(strings.Join(parts, "\n\n"))
		return nil
	}
	return fmt.Errorf("system parameter must be string or list of content blocks")
}

type AnthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []AnthropicMessage `json:"messages"`
	System      AnthropicSystem    `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	Thinking    *AnthropicThinking `json:"thinking,omitempty"`
	// Gateway-level effort control (Muhiya extension, mirrors the OpenAI
	// field). Stripped by ApplyThinking* before anything reaches a provider.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type AnthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"` // "message"
	Role         string             `json:"role"` // "assistant"
	Content      []AnthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"` // "end_turn", "max_tokens", "stop_sequence", "tool_use"
	StopSequence *string            `json:"stop_sequence"`
	Usage        AnthropicUsage     `json:"usage"`
}

// ==========================================
// Parsing & Translating Helpers
// ==========================================

func GetMessageContentString(content interface{}) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, part := range v {
			if m, ok := part.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ------------------------------------------
// Translate OpenAI Request -> Anthropic Request
// ------------------------------------------
func TranslateOpenAIContentToAnthropic(content interface{}) []AnthropicContent {
	if content == nil {
		return nil
	}

	switch v := content.(type) {
	case string:
		return []AnthropicContent{{
			Type: "text",
			Text: v,
		}}
	case []interface{}:
		var result []AnthropicContent
		for _, part := range v {
			m, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := m["type"].(string)
			switch t {
			case "text":
				if text, ok := m["text"].(string); ok {
					result = append(result, AnthropicContent{
						Type: "text",
						Text: text,
					})
				}
			case "image_url":
				if imgMap, ok := m["image_url"].(map[string]interface{}); ok {
					if url, ok := imgMap["url"].(string); ok {
						if strings.HasPrefix(url, "data:") {
							// Parse data URL: data:<media-type>;base64,<data>
							commaIdx := strings.Index(url, ",")
							if commaIdx != -1 {
								header := url[:commaIdx]
								base64Data := url[commaIdx+1:]

								// Extract media type from header (e.g. "data:image/jpeg;base64")
								mediaType := "image/jpeg"
								semiIdx := strings.Index(header, ";")
								if semiIdx != -1 && strings.HasPrefix(header, "data:") {
									mediaType = header[5:semiIdx]
								}

								result = append(result, AnthropicContent{
									Type: "image",
									Source: &AnthropicSource{
										Type:      "base64",
										MediaType: mediaType,
										Data:      base64Data,
									},
								})
							}
						}
					}
				}
			}
		}
		return result
	default:
		return []AnthropicContent{{
			Type: "text",
			Text: fmt.Sprintf("%v", v),
		}}
	}
}

func TranslateOpenAIToAnthropic(orig *OpenAIRequest, targetModel string) (*AnthropicRequest, error) {
	var systemParts []string
	var rawMessages []OpenAIMessage

	for _, msg := range orig.Messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, GetMessageContentString(msg.Content))
		} else {
			rawMessages = append(rawMessages, msg)
		}
	}

	systemPrompt := strings.Join(systemParts, "\n\n")

	var anthropicMessages []AnthropicMessage
	for _, msg := range rawMessages {
		role := msg.Role
		if role != "user" && role != "assistant" && role != "tool" {
			role = "user" // fallback
		}

		var content []AnthropicContent

		// If it's a tool response
		if msg.Role == "tool" {
			role = "user"
			content = append(content, AnthropicContent{
				Type:      "tool_result",
				ToolUseID: msg.ToolID(),
				Content:   GetMessageContentString(msg.Content),
			})
		} else {
			// standard message content
			if msg.Content != nil {
				content = append(content, TranslateOpenAIContentToAnthropic(msg.Content)...)
			}

			// append tool calls if any
			for _, tc := range msg.ToolCalls {
				var input map[string]interface{}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
				content = append(content, AnthropicContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
		}

		if len(content) == 0 {
			continue
		}

		// Merge consecutive messages with the same role
		if len(anthropicMessages) > 0 && anthropicMessages[len(anthropicMessages)-1].Role == role {
			prev := &anthropicMessages[len(anthropicMessages)-1]
			prev.Content = append(prev.Content, content...)
		} else {
			anthropicMessages = append(anthropicMessages, AnthropicMessage{
				Role:    role,
				Content: content,
			})
		}
	}

	// Anthropic requires user first
	if len(anthropicMessages) > 0 && anthropicMessages[0].Role != "user" {
		anthropicMessages = append([]AnthropicMessage{{
			Role:    "user",
			Content: []AnthropicContent{{Type: "text", Text: "Hello"}},
		}}, anthropicMessages...)
	}

	if len(anthropicMessages) == 0 {
		anthropicMessages = []AnthropicMessage{{
			Role:    "user",
			Content: []AnthropicContent{{Type: "text", Text: "Hello"}},
		}}
	}

	// Translate tools
	var anthTools []AnthropicTool
	for _, t := range orig.Tools {
		if t.Type == "function" {
			anthTools = append(anthTools, AnthropicTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
	}

	maxTokens := 4096
	if orig.MaxTokens != nil {
		maxTokens = *orig.MaxTokens
	} else if orig.MaxCompletionTokens != nil {
		maxTokens = *orig.MaxCompletionTokens
	}

	req := &AnthropicRequest{
		Model:       targetModel,
		Messages:    anthropicMessages,
		System:      AnthropicSystem(systemPrompt),
		MaxTokens:   maxTokens,
		Temperature: orig.Temperature,
		Stream:      orig.Stream,
		Tools:       anthTools,
		Thinking:    orig.Thinking,
	}

	return req, nil
}

// ------------------------------------------
// Translate Anthropic Request -> OpenAI Request
// ------------------------------------------
func TranslateAnthropicToOpenAI(orig *AnthropicRequest, targetModel string) (*OpenAIRequest, error) {
	var oaiMessages []OpenAIMessage

	if orig.System != "" {
		oaiMessages = append(oaiMessages, OpenAIMessage{
			Role:    "system",
			Content: string(orig.System),
		})
	}

	for _, msg := range orig.Messages {
		role := msg.Role
		var oaiMsg OpenAIMessage
		oaiMsg.Role = role

		var textParts []string
		var toolCalls []OpenAIToolCall

		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "thinking":
				// Support Claude 3.7 reasoning content
				textParts = append(textParts, fmt.Sprintf("<thinking>\n%s\n</thinking>\n", block.Thinking))
			case "tool_use":
				argsBytes, _ := json.Marshal(block.Input)
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID:   block.ID,
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      block.Name,
						Arguments: string(argsBytes),
					},
				})
			case "tool_result":
				// Split tool result into a separate OpenAI message with role: tool
				var toolContent string
				if block.Content != nil {
					switch tc := block.Content.(type) {
					case string:
						toolContent = tc
					default:
						b, _ := json.Marshal(tc)
						toolContent = string(b)
					}
				}
				oaiMessages = append(oaiMessages, OpenAIMessage{
					Role:       "tool",
					ToolCallID: block.ToolUseID,
					Content:    toolContent,
				})
			}
		}

		oaiMsg.Content = strings.Join(textParts, "\n")
		oaiMsg.ToolCalls = toolCalls

		// Only append if it's not empty, or if it has tool calls
		if oaiMsg.Content != "" || len(oaiMsg.ToolCalls) > 0 {
			oaiMessages = append(oaiMessages, oaiMsg)
		}
	}

	// Translate tools
	var oaiTools []OpenAITool
	for _, t := range orig.Tools {
		oaiTools = append(oaiTools, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	maxTokens := orig.MaxTokens

	var streamOptions *OpenAIStreamOptions
	if orig.Stream {
		streamOptions = &OpenAIStreamOptions{IncludeUsage: true}
	}

	req := &OpenAIRequest{
		Model:         targetModel,
		Messages:      oaiMessages,
		Temperature:   orig.Temperature,
		MaxTokens:     &maxTokens,
		Stream:        orig.Stream,
		StreamOptions: streamOptions,
		Tools:         oaiTools,
	}

	return req, nil
}

// ------------------------------------------
// Translate OpenAI Response -> Anthropic Response
// ------------------------------------------
func TranslateOpenAIToAnthropicResponse(oaiResp *OpenAIResponse, virtualModel string) *AnthropicResponse {
	var content []AnthropicContent

	finishReason := "end_turn"
	if len(oaiResp.Choices) > 0 {
		choice := oaiResp.Choices[0]
		// Map text content
		if text := GetMessageContentString(choice.Message.Content); text != "" {
			content = append(content, AnthropicContent{
				Type: "text",
				Text: text,
			})
		}
		// Map tool calls
		for _, tc := range choice.Message.ToolCalls {
			var input map[string]interface{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			content = append(content, AnthropicContent{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: input,
			})
		}

		if choice.FinishReason == "length" {
			finishReason = "max_tokens"
		} else if choice.FinishReason == "tool_calls" {
			finishReason = "tool_use"
		}
	}

	// Parse cache tokens (OpenAI details shape or DeepSeek's own fields).
	cacheRead := oaiResp.Usage.CacheReadTokens()

	return &AnthropicResponse{
		ID:         oaiResp.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      virtualModel,
		StopReason: finishReason,
		Usage: AnthropicUsage{
			InputTokens:          oaiResp.Usage.PromptTokens,
			OutputTokens:         oaiResp.Usage.CompletionTokens,
			CacheReadInputTokens: cacheRead,
		},
	}
}

// ------------------------------------------
// Translate Anthropic Response -> OpenAI Response
// ------------------------------------------
func TranslateAnthropicToOpenAIResponse(resp *AnthropicResponse, virtualModel string) *OpenAIResponse {
	var textParts []string
	var toolCalls []OpenAIToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "thinking":
			textParts = append(textParts, fmt.Sprintf("<thinking>\n%s\n</thinking>\n", block.Thinking))
		case "tool_use":
			argsBytes, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name:      block.Name,
					Arguments: string(argsBytes),
				},
			})
		}
	}

	finishReason := "stop"
	if resp.StopReason == "max_tokens" {
		finishReason = "length"
	} else if resp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	}

	promptDetails := &PromptTokensDetail{
		CachedTokens: resp.Usage.CacheReadInputTokens,
	}

	return &OpenAIResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   virtualModel,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:      "assistant",
					Content:   strings.Join(textParts, "\n"),
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:        resp.Usage.InputTokens,
			CompletionTokens:    resp.Usage.OutputTokens,
			TotalTokens:         resp.Usage.InputTokens + resp.Usage.OutputTokens,
			PromptTokensDetails: promptDetails,
		},
	}
}

// ------------------------------------------
// Translating Streaming SSE Chunks
// ------------------------------------------

// Translate OpenAI SSE Chunk event payload -> Anthropic SSE events
func TranslateOpenAIChunkToAnthropic(line string, msgID string, virtualModel string, usageTracker *AnthropicUsage) ([]byte, string, bool, error) {
	if !strings.HasPrefix(line, "data:") {
		return nil, "", false, nil
	}

	dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if dataStr == "[DONE]" || dataStr == "" {
		return nil, "", false, nil
	}

	var chunk OpenAIChunk
	if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
		return nil, "", false, err
	}

	// If chunk has usage details
	if chunk.Usage != nil {
		usageTracker.InputTokens = chunk.Usage.PromptTokens
		usageTracker.OutputTokens = chunk.Usage.CompletionTokens
		if read := chunk.Usage.CacheReadTokens(); read > 0 {
			usageTracker.CacheReadInputTokens = read
		}
	}

	if len(chunk.Choices) == 0 {
		return nil, "", false, nil
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// Case 1: Initial Role definition
	if delta.Role == "assistant" && delta.Content == "" {
		event := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":      msgID,
				"type":    "message",
				"role":    "assistant",
				"content": []interface{}{},
				"model":   virtualModel,
				"usage": map[string]interface{}{
					"input_tokens": usageTracker.InputTokens,
				},
			},
		}
		bytes, _ := json.Marshal(event)
		return bytes, "message_start", false, nil
	}

	// Case 2: Content Delta (Reasoning / Thinking)
	if delta.ReasoningContent != "" {
		event := map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type":     "thinking_delta",
				"thinking": delta.ReasoningContent,
			},
		}
		bytes, _ := json.Marshal(event)
		return bytes, "content_block_delta", false, nil
	}

	// Case 3: Content Delta (Text Content)
	if delta.Content != "" {
		event := map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": delta.Content,
			},
		}
		bytes, _ := json.Marshal(event)
		return bytes, "content_block_delta", false, nil
	}

	// Case 4: Tool Call Delta
	if len(delta.ToolCalls) > 0 {
		tc := delta.ToolCalls[0]
		// If it's the start of a tool call
		if tc.Function.Name != "" {
			event := map[string]interface{}{
				"type":  "content_block_start",
				"index": 1,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": map[string]interface{}{},
				},
			}
			bytes, _ := json.Marshal(event)
			return bytes, "content_block_start", false, nil
		}
		// If it has inputs arguments (which are accumulated as string chunks)
		if tc.Function.Arguments != "" {
			event := map[string]interface{}{
				"type":  "content_block_delta",
				"index": 1,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": tc.Function.Arguments,
				},
			}
			bytes, _ := json.Marshal(event)
			return bytes, "content_block_delta", false, nil
		}
	}

	// Case 5: Finished
	if choice.FinishReason != nil {
		stopReason := "end_turn"
		if *choice.FinishReason == "length" {
			stopReason = "max_tokens"
		} else if *choice.FinishReason == "tool_calls" {
			stopReason = "tool_use"
		}

		eventDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason": stopReason,
			},
			"usage": map[string]interface{}{
				"output_tokens": usageTracker.OutputTokens,
			},
		}
		bytesDelta, _ := json.Marshal(eventDelta)

		// Also return message_stop
		return bytesDelta, "message_delta", true, nil
	}

	return nil, "", false, nil
}

// ------------------------------------------
// OpenAI Message Extensions
// ------------------------------------------
func (msg *OpenAIMessage) ToolID() string {
	return msg.ToolCallID
}

// Translate Anthropic SSE Chunk event payload -> OpenAI SSE events
func TranslateAnthropicChunkToOpenAI(line string, msgID string, virtualModel string, usageTracker *OpenAIUsage) ([]byte, bool, error) {
	if !strings.HasPrefix(line, "data:") {
		return nil, false, nil
	}

	dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if dataStr == "[DONE]" || dataStr == "" {
		return nil, false, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &raw); err != nil {
		return nil, false, err
	}

	eventType, _ := raw["type"].(string)

	switch eventType {
	case "message_start":
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			if usage, ok := msg["usage"].(map[string]interface{}); ok {
				if inTokens, ok := usage["input_tokens"].(float64); ok {
					usageTracker.PromptTokens = int(inTokens)
				}
				if cacheRead, ok := usage["cache_read_input_tokens"].(float64); ok {
					if usageTracker.PromptTokensDetails == nil {
						usageTracker.PromptTokensDetails = &PromptTokensDetail{}
					}
					usageTracker.PromptTokensDetails.CachedTokens = int(cacheRead)
				}
				if cacheWrite, ok := usage["cache_creation_input_tokens"].(float64); ok {
					usageTracker.CacheWriteTokens = int(cacheWrite)
				}
			}
		}

		chunk := OpenAIChunk{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   virtualModel,
			Choices: []OpenAIChunkChoice{
				{
					Index: 0,
					Delta: OpenAIDelta{
						Role: "assistant",
					},
				},
			},
		}
		bytes, _ := json.Marshal(chunk)
		return bytes, false, nil

	case "content_block_start":
		return nil, false, nil

	case "content_block_delta":
		deltaMap, _ := raw["delta"].(map[string]interface{})
		deltaType, _ := deltaMap["type"].(string)

		var delta OpenAIDelta
		if deltaType == "text_delta" {
			text, _ := deltaMap["text"].(string)
			delta.Content = text
		} else if deltaType == "thinking_delta" {
			thinking, _ := deltaMap["thinking"].(string)
			delta.ReasoningContent = thinking
		} else if deltaType == "input_json_delta" {
			partialJSON, _ := deltaMap["partial_json"].(string)
			delta.ToolCalls = []OpenAIToolCall{
				{
					Type: "function",
					Function: OpenAIFunctionCall{
						Arguments: partialJSON,
					},
				},
			}
		}

		chunk := OpenAIChunk{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   virtualModel,
			Choices: []OpenAIChunkChoice{
				{
					Index: 0,
					Delta: delta,
				},
			},
		}
		bytes, _ := json.Marshal(chunk)
		return bytes, false, nil

	case "content_block_stop":
		return nil, false, nil

	case "message_delta":
		deltaMap, _ := raw["delta"].(map[string]interface{})
		stopReason, _ := deltaMap["stop_reason"].(string)

		var finishReason string
		if stopReason == "end_turn" {
			finishReason = "stop"
		} else if stopReason == "max_tokens" {
			finishReason = "length"
		} else if stopReason == "tool_use" {
			finishReason = "tool_calls"
		}

		if usage, ok := raw["usage"].(map[string]interface{}); ok {
			if outTokens, ok := usage["output_tokens"].(float64); ok {
				usageTracker.CompletionTokens = int(outTokens)
			}
			if cacheWrite, ok := usage["cache_creation_input_tokens"].(float64); ok {
				usageTracker.CacheWriteTokens = int(cacheWrite)
			}
		}

		chunk := OpenAIChunk{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   virtualModel,
			Choices: []OpenAIChunkChoice{
				{
					Index:        0,
					FinishReason: &finishReason,
				},
			},
		}
		bytes, _ := json.Marshal(chunk)
		return bytes, false, nil

	case "message_stop":
		usageTracker.TotalTokens = usageTracker.PromptTokens + usageTracker.CompletionTokens
		chunk := OpenAIChunk{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   virtualModel,
			Choices: []OpenAIChunkChoice{},
			Usage:   usageTracker,
		}
		bytes, _ := json.Marshal(chunk)
		return bytes, true, nil
	}

	return nil, false, nil
}
