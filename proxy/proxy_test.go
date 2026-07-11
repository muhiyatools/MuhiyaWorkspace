package proxy

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"gateway/db"
)

func TestTranslateOpenAIToAnthropic(t *testing.T) {
	origReq := &OpenAIRequest{
		Model: "gpt-4o",
		Messages: []OpenAIMessage{
			{Role: "system", Content: "System prompt 1"},
			{Role: "system", Content: "System prompt 2"},
			{Role: "user", Content: "User message 1"},
			{Role: "user", Content: "User message 2"},
			{Role: "assistant", Content: "Assistant reply"},
		},
		Temperature: floatPtr(0.7),
		MaxTokens:   intPtr(250),
	}

	anthReq, err := TranslateOpenAIToAnthropic(origReq, "claude-3-5-sonnet")
	if err != nil {
		t.Fatalf("Failed to translate: %v", err)
	}

	if anthReq.Model != "claude-3-5-sonnet" {
		t.Errorf("Expected model 'claude-3-5-sonnet', got '%s'", anthReq.Model)
	}

	// Verify system prompt concatenation
	expectedSystem := "System prompt 1\n\nSystem prompt 2"
	if string(anthReq.System) != expectedSystem {
		t.Errorf("Expected system prompt '%s', got '%s'", expectedSystem, anthReq.System)
	}

	// Verify messages structure
	// User 1 & 2 should be merged because they are adjacent and have the same role
	if len(anthReq.Messages) != 2 {
		t.Fatalf("Expected 2 message blocks after role merging, got %d", len(anthReq.Messages))
	}

	if anthReq.Messages[0].Role != "user" {
		t.Errorf("Expected first message role 'user', got '%s'", anthReq.Messages[0].Role)
	}

	if len(anthReq.Messages[0].Content) != 2 {
		t.Fatalf("Expected 2 content blocks, got %d", len(anthReq.Messages[0].Content))
	}
	if anthReq.Messages[0].Content[0].Text != "User message 1" {
		t.Errorf("Expected first content 'User message 1', got '%s'", anthReq.Messages[0].Content[0].Text)
	}
	if anthReq.Messages[0].Content[1].Text != "User message 2" {
		t.Errorf("Expected second content 'User message 2', got '%s'", anthReq.Messages[0].Content[1].Text)
	}

	if anthReq.Messages[1].Role != "assistant" {
		t.Errorf("Expected second message role 'assistant', got '%s'", anthReq.Messages[1].Role)
	}

	if anthReq.MaxTokens != 250 {
		t.Errorf("Expected max tokens 250, got %d", anthReq.MaxTokens)
	}
}

func TestTranslateAnthropicToOpenAI(t *testing.T) {
	anthResp := &AnthropicResponse{
		ID:    "msg_123",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-3-5-sonnet",
		Content: []AnthropicContent{
			{Type: "text", Text: "Hello world!"},
		},
		StopReason: "end_turn",
		Usage: AnthropicUsage{
			InputTokens:  10,
			OutputTokens: 20,
		},
	}

	oaiResp := TranslateAnthropicToOpenAIResponse(anthResp, "gpt-4o")

	if oaiResp.ID != "msg_123" {
		t.Errorf("Expected ID 'msg_123', got '%s'", oaiResp.ID)
	}

	if oaiResp.Model != "gpt-4o" {
		t.Errorf("Expected model 'gpt-4o', got '%s'", oaiResp.Model)
	}

	if len(oaiResp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(oaiResp.Choices))
	}

	if oaiResp.Choices[0].Message.Role != "assistant" {
		t.Errorf("Expected choice role 'assistant', got '%s'", oaiResp.Choices[0].Message.Role)
	}

	contentStr := GetMessageContentString(oaiResp.Choices[0].Message.Content)
	if contentStr != "Hello world!" {
		t.Errorf("Expected content 'Hello world!', got '%s'", contentStr)
	}

	if oaiResp.Usage.PromptTokens != 10 {
		t.Errorf("Expected prompt tokens 10, got %d", oaiResp.Usage.PromptTokens)
	}

	if oaiResp.Usage.CompletionTokens != 20 {
		t.Errorf("Expected completion tokens 20, got %d", oaiResp.Usage.CompletionTokens)
	}
}

func TestTranslateAnthropicChunkToOpenAI(t *testing.T) {
	usage := &OpenAIUsage{}
	msgID := "chatcmpl-123"

	// 1. Message start event
	line1 := `data: {"type": "message_start", "message": {"usage": {"input_tokens": 15}}}`
	bytes, done, err := TranslateAnthropicChunkToOpenAI(line1, msgID, "claude-3", usage)
	if err != nil {
		t.Fatalf("Error translating chunk: %v", err)
	}
	if done {
		t.Error("Expected done to be false")
	}
	if usage.PromptTokens != 15 {
		t.Errorf("Expected usage.PromptTokens 15, got %d", usage.PromptTokens)
	}

	var chunk OpenAIChunk
	if err := json.Unmarshal(bytes, &chunk); err != nil {
		t.Fatalf("Error parsing translated chunk JSON: %v", err)
	}
	if chunk.Choices[0].Delta.Role != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", chunk.Choices[0].Delta.Role)
	}

	// 2. Content delta event
	line2 := `data: {"type": "content_block_delta", "delta": {"type": "text_delta", "text": " hello"}}`
	bytes, done, err = TranslateAnthropicChunkToOpenAI(line2, msgID, "claude-3", usage)
	if err != nil {
		t.Fatalf("Error translating chunk: %v", err)
	}
	if err := json.Unmarshal(bytes, &chunk); err != nil {
		t.Fatalf("Error parsing translated chunk JSON: %v", err)
	}
	if chunk.Choices[0].Delta.Content != " hello" {
		t.Errorf("Expected content ' hello', got '%s'", chunk.Choices[0].Delta.Content)
	}

	// 3. Message delta event
	line3 := `data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 25}}`
	bytes, done, err = TranslateAnthropicChunkToOpenAI(line3, msgID, "claude-3", usage)
	if err != nil {
		t.Fatalf("Error translating chunk: %v", err)
	}
	if err := json.Unmarshal(bytes, &chunk); err != nil {
		t.Fatalf("Error parsing translated chunk JSON: %v", err)
	}
	if *chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("Expected finish reason 'stop', got '%s'", *chunk.Choices[0].FinishReason)
	}
	if usage.CompletionTokens != 25 {
		t.Errorf("Expected completion tokens 25, got %d", usage.CompletionTokens)
	}

	// 4. Message stop event
	line4 := `data: {"type": "message_stop"}`
	bytes, done, err = TranslateAnthropicChunkToOpenAI(line4, msgID, "claude-3", usage)
	if err != nil {
		t.Fatalf("Error translating chunk: %v", err)
	}
	if !done {
		t.Error("Expected done to be true")
	}
	if err := json.Unmarshal(bytes, &chunk); err != nil {
		t.Fatalf("Error parsing translated chunk JSON: %v", err)
	}
	if chunk.Usage.TotalTokens != 40 {
		t.Errorf("Expected total tokens 40, got %d", chunk.Usage.TotalTokens)
	}
}

func TestRateLimiter(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("Skipping database tests: TEST_DATABASE_URL or DATABASE_URL not set")
	}

	testDB, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer testDB.Close()

	// Cleanup any leftovers before testing
	_ = testDB.DeleteVirtualKey("key-unlimit")
	_ = testDB.DeleteUser("user-unlimit")
	_ = testDB.DeletePlan("plan-unlimit")
	_ = testDB.DeleteVirtualKey("key-limit")
	_ = testDB.DeleteUser("user-limit")
	_ = testDB.DeletePlan("plan-limit")
	_ = testDB.DeleteVirtualKey("key-budget")
	_ = testDB.DeleteUser("user-budget")
	_ = testDB.DeletePlan("plan-budget")

	defer func() {
		_ = testDB.DeleteVirtualKey("key-unlimit")
		_ = testDB.DeleteUser("user-unlimit")
		_ = testDB.DeletePlan("plan-unlimit")
		_ = testDB.DeleteVirtualKey("key-limit")
		_ = testDB.DeleteUser("user-limit")
		_ = testDB.DeletePlan("plan-limit")
		_ = testDB.DeleteVirtualKey("key-budget")
		_ = testDB.DeleteUser("user-budget")
		_ = testDB.DeletePlan("plan-budget")
	}()

	rl := NewRateLimiter(testDB)

	// Create a test plan with custom limits
	planUnlimited := db.Plan{
		ID:       "plan-unlimit",
		Name:     "Unlimited Test Plan",
		RPMLimit: 0,
		TPMLimit: 0,
	}
	if err := testDB.CreatePlan(planUnlimited); err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}

	userUnlimit := db.User{
		ID:     "user-unlimit",
		Name:   "Unlimit User",
		Email:  "unlimit@test.com",
		PlanID: planUnlimited.ID,
		Status: "active",
	}
	if err := testDB.CreateUser(userUnlimit); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	keyUnlimit := db.VirtualKey{
		ID:     "key-unlimit",
		Name:   "Unlimit Key",
		UserID: userUnlimit.ID,
		Status: "active",
	}
	if _, err := testDB.CreateVirtualKey(keyUnlimit); err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	// 1. Unlimited plan check
	for i := 0; i < 5; i++ {
		err := rl.CheckLimit(&keyUnlimit, 1000)
		if err != nil {
			t.Errorf("Unexpected error in unlimited plan check: %v", err)
		}
	}

	// 2. RPM & TPM limits
	planLimited := db.Plan{
		ID:       "plan-limit",
		Name:     "Limited Test Plan",
		RPMLimit: 2,
		TPMLimit: 5000,
	}
	if err := testDB.CreatePlan(planLimited); err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}

	userLimit := db.User{
		ID:     "user-limit",
		Name:   "Limit User",
		Email:  "limit@test.com",
		PlanID: planLimited.ID,
		Status: "active",
	}
	if err := testDB.CreateUser(userLimit); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	keyLimit := db.VirtualKey{
		ID:     "key-limit",
		Name:   "Limit Key",
		UserID: userLimit.ID,
		Status: "active",
	}
	if _, err := testDB.CreateVirtualKey(keyLimit); err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	// Reset rl limiters for safety
	rl.limiters = make(map[string]*KeyLimiter)

	// 1st request - ok
	if err := rl.CheckLimit(&keyLimit, 1000); err != nil {
		t.Errorf("Unexpected error for 1st request: %v", err)
	}

	// 2nd request - ok
	if err := rl.CheckLimit(&keyLimit, 2000); err != nil {
		t.Errorf("Unexpected error for 2nd request: %v", err)
	}

	// 3rd request - should fail due to RPM
	if err := rl.CheckLimit(&keyLimit, 1000); err == nil {
		t.Error("Expected 3rd request to fail RPM check but it succeeded")
	} else {
		t.Logf("Got expected RPM error: %v", err)
	}

	// Reset sliding windows for key-limit TPM check
	rl.limiters = make(map[string]*KeyLimiter)

	// 1st request uses 4000 tokens - ok
	if err := rl.CheckLimit(&keyLimit, 4000); err != nil {
		t.Errorf("Unexpected error for high token request: %v", err)
	}

	// 2nd request uses 2000 tokens (Total = 6000 > 5000) - should fail TPM check
	if err := rl.CheckLimit(&keyLimit, 2000); err == nil {
		t.Error("Expected 2nd request to fail TPM check but it succeeded")
	} else {
		t.Logf("Got expected TPM error: %v", err)
	}

	// 3. Sliding Budget Window limit check
	planBudget := db.Plan{
		ID:       "plan-budget",
		Name:     "Budget Test Plan",
		RPMLimit: 0,
		TPMLimit: 0,
	}
	if err := testDB.CreatePlan(planBudget); err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}

	// Create a budget window with $5.00 limit for 60 seconds
	bw := db.BudgetWindow{
		ID:              "bw-1",
		PlanID:          planBudget.ID,
		Name:            "Test Window",
		DurationSeconds: 60,
		BudgetUSD:       5.00,
	}
	if err := testDB.CreateBudgetWindow(bw); err != nil {
		t.Fatalf("failed to create budget window: %v", err)
	}

	userBudget := db.User{
		ID:     "user-budget",
		Name:   "Budget User",
		Email:  "budget@test.com",
		PlanID: planBudget.ID,
		Status: "active",
	}
	if err := testDB.CreateUser(userBudget); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	keyBudget := db.VirtualKey{
		ID:     "key-budget",
		Name:   "Budget Key",
		UserID: userBudget.ID,
		Status: "active",
	}
	if _, err := testDB.CreateVirtualKey(keyBudget); err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	// Insert mock request log with cost exceeding budget
	err = testDB.InsertRequestLog(db.RequestLog{
		ID:           "test-log-1",
		VirtualKeyID: keyBudget.ID,
		UserID:       userBudget.ID,
		RequestPath:  "/chat/completions",
		StatusCode:   200,
		Cost:         6.00, // exceeds $5.00 budget
		LatencyMS:    100,
		CreatedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to insert mock request log: %v", err)
	}

	if err := rl.CheckLimit(&keyBudget, 10); err == nil {
		t.Error("Expected request to fail Budget window check but it succeeded")
	} else {
		t.Logf("Got expected Budget window limit error: %v", err)
	}
}

func TestTranslateErrorBytes(t *testing.T) {
	// 1. OpenAI to Anthropic translation
	oaiError := `{"error":{"message":"Invalid API Key","type":"invalid_request_error","code":"invalid_api_key"}}`
	translated := translateErrorBytes([]byte(oaiError), true)
	var anthErr anthropicErrorPayload
	if err := json.Unmarshal(translated, &anthErr); err != nil {
		t.Fatalf("Failed to parse translated error: %v", err)
	}
	if anthErr.Type != "error" || anthErr.Error.Type != "invalid_request_error" || anthErr.Error.Message != "Invalid API Key" {
		t.Errorf("Unexpected translated Anthropic error: %s", string(translated))
	}

	// 2. Anthropic to OpenAI translation
	anthError := `{"type":"error","error":{"type":"authentication_error","message":"Invalid API Key"}}`
	translatedOai := translateErrorBytes([]byte(anthError), false)
	var oaiErr openaiErrorPayload
	if err := json.Unmarshal(translatedOai, &oaiErr); err != nil {
		t.Fatalf("Failed to parse translated error: %v", err)
	}
	if oaiErr.Error.Type != "authentication_error" || oaiErr.Error.Message != "Invalid API Key" {
		t.Errorf("Unexpected translated OpenAI error: %s", string(translatedOai))
	}

	// 3. Raw text fallback to Anthropic
	rawText := `Bad Gateway`
	translatedRawAnth := translateErrorBytes([]byte(rawText), true)
	if err := json.Unmarshal(translatedRawAnth, &anthErr); err != nil {
		t.Fatalf("Failed to parse translated error: %v", err)
	}
	if anthErr.Type != "error" || anthErr.Error.Type != "api_error" || anthErr.Error.Message != "Bad Gateway" {
		t.Errorf("Unexpected translated Anthropic error: %s", string(translatedRawAnth))
	}

	// 4. Raw text fallback to OpenAI
	translatedRawOai := translateErrorBytes([]byte(rawText), false)
	if err := json.Unmarshal(translatedRawOai, &oaiErr); err != nil {
		t.Fatalf("Failed to parse translated error: %v", err)
	}
	if oaiErr.Error.Type != "api_error" || oaiErr.Error.Message != "Bad Gateway" {
		t.Errorf("Unexpected translated OpenAI error: %s", string(translatedRawOai))
	}
}

func TestTranslateOpenAIToAnthropicWithAttachments(t *testing.T) {
	origReq := &OpenAIRequest{
		Model: "gpt-4o",
		Messages: []OpenAIMessage{
			{
				Role: "user",
				Content: []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "What is in this image?",
					},
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": "data:image/png;base64,iVBORw0KGgoAAAANS",
						},
					},
				},
			},
		},
	}

	anthReq, err := TranslateOpenAIToAnthropic(origReq, "claude-3-5-sonnet")
	if err != nil {
		t.Fatalf("Failed to translate request with attachments: %v", err)
	}

	if len(anthReq.Messages) != 1 {
		t.Fatalf("Expected 1 translated message, got %d", len(anthReq.Messages))
	}

	content := anthReq.Messages[0].Content
	if len(content) != 2 {
		t.Fatalf("Expected 2 content blocks in translated message, got %d", len(content))
	}

	if content[0].Type != "text" || content[0].Text != "What is in this image?" {
		t.Errorf("Expected first content block to be text, got type=%s text=%s", content[0].Type, content[0].Text)
	}

	if content[1].Type != "image" {
		t.Fatalf("Expected second content block to be image, got type=%s", content[1].Type)
	}

	if content[1].Source == nil {
		t.Fatalf("Expected image source to be not nil")
	}

	if content[1].Source.Type != "base64" || content[1].Source.MediaType != "image/png" || content[1].Source.Data != "iVBORw0KGgoAAAANS" {
		t.Errorf("Unexpected translated image source details: %+v", content[1].Source)
	}
}

// Helpers
func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int         { return &i }

