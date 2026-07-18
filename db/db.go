package db

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type UserBudgetUsage struct {
	WindowID        string     `json:"window_id"`
	Name            string     `json:"name"`
	DurationSeconds int        `json:"duration_seconds"`
	BudgetUSD       float64    `json:"budget_usd"`
	CurrentSpent    float64    `json:"current_spent"`
	ResetTime       *time.Time `json:"reset_time,omitempty"`
}

type User struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Email                 string            `json:"email"`
	PlanID                string            `json:"plan_id"`
	Status                string            `json:"status"` // active, suspended
	CreatedAt             time.Time         `json:"created_at"`
	PlanAssignedAt        time.Time         `json:"plan_assigned_at"`
	BudgetUsage           []UserBudgetUsage `json:"budget_usage,omitempty"`
	ExtraCredits          float64           `json:"extra_credits"`
	RemainingExtraCredits float64           `json:"remaining_extra_credits"`
}

type Plan struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	RPMLimit      int            `json:"rpm_limit"`
	TPMLimit      int            `json:"tpm_limit"`
	BudgetWindows []BudgetWindow `json:"budget_windows,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

type BudgetWindow struct {
	ID              string    `json:"id"`
	PlanID          string    `json:"plan_id"`
	Name            string    `json:"name"`
	DurationSeconds int       `json:"duration_seconds"`
	BudgetUSD       float64   `json:"budget_usd"`
	CreatedAt       time.Time `json:"created_at"`
}

type VirtualKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	UserID    string     `json:"user_id"`
	Status    string     `json:"status"` // active, revoked
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	// Token is the plaintext bearer credential. It is populated ONLY by
	// CreateVirtualKey's return value - the one moment it ever exists in
	// plaintext outside the caller's memory - and is never scanned from the
	// database (only its hash, key_hash, is stored).
	Token string `json:"key,omitempty"`
}

type Provider struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	APIKey           string    `json:"api_key"`
	BaseURL          string    `json:"base_url"`
	AnthropicBaseURL string    `json:"anthropic_base_url"`
	Status           string    `json:"status"` // active, inactive
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Model struct {
	ID                       string    `json:"id"`
	Name                     string    `json:"name"` // virtual model name (e.g. gpt-4o)
	ProviderID               string    `json:"provider_id"`
	TargetModel              string    `json:"target_model"` // provider target name
	InputCostPerMillion      float64   `json:"input_cost_per_million"`
	OutputCostPerMillion     float64   `json:"output_cost_per_million"`
	CacheReadCostPerMillion  float64   `json:"cache_read_cost_per_million"`
	CacheWriteCostPerMillion float64   `json:"cache_write_cost_per_million"`
	Status                   string    `json:"status"`       // active, inactive
	RoutingTier              string    `json:"routing_tier"` // none, simple, medium, hard
	ModelType                string    `json:"model_type"`   // llm, transcript
	PricePerMinute           float64   `json:"price_per_minute"`
	Transcribe               bool      `json:"transcribe"`
	ContextWindow            int       `json:"context_window"`
	MaxOutputTokens          int       `json:"max_output_tokens"`
	DisplayName              string    `json:"display_name"`
	Description              string    `json:"description"`
	OwnedBy                  string    `json:"owned_by"`
	// SupportsVision is the operator-set flag that a model accepts image input.
	// It is the source of truth for vision routing (the name heuristic is only a
	// fallback), so a vision model with any name is routed correctly.
	SupportsVision bool `json:"supports_vision"`
	// SupportsThinking is the operator-set flag that a model supports reasoning /
	// thinking effort. Source of truth for thinking-tier routing (the target-name
	// heuristic is only a fallback for unflagged rows).
	SupportsThinking bool `json:"supports_thinking"`
	// SupportsAudio / SupportsVideo / SupportsDocuments extend the capability
	// system (migration 020) to every input modality: audio parts (input_audio),
	// video parts (video_url) and document parts (file, e.g. PDF). Operator-set,
	// flag-only (no name heuristics) — the router and /v1/models read them as the
	// single source of truth for attachment routing.
	SupportsAudio     bool `json:"supports_audio"`
	SupportsVideo     bool `json:"supports_video"`
	SupportsDocuments bool `json:"supports_documents"`
	// MaxAttachmentMB caps a single attachment for THIS model; 0 means "no
	// model-specific cap" (clients fall back to their own default). Informational
	// for clients — the gateway does not reject on it.
	MaxAttachmentMB int `json:"max_attachment_mb"`
	// AcceptedMimeTypes optionally narrows the flag-derived accepted set to an
	// explicit comma-separated MIME allowlist (e.g. "image/png,application/pdf").
	// Empty = accept whatever the capability flags imply. Published on
	// /v1/models (as an array) for client-side pre-checks.
	AcceptedMimeTypes string    `json:"accepted_mime_types"`
	CreatedAt         time.Time `json:"created_at"`
}

type RequestLog struct {
	ID               string `json:"id"`
	VirtualKeyID     string `json:"virtual_key_id"`
	UserID           string `json:"user_id"`
	ModelID          string `json:"model_id"`
	ProviderID       string `json:"provider_id"`
	RequestPath      string `json:"request_path"`
	StatusCode       int    `json:"status_code"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	// CacheMissTokens is the provider-reported non-cached (cache-miss) prompt
	// token count. It is nil for rows logged before feature 007 or by upstreams
	// that do not report it, in which case the dashboard hit-rate falls back to
	// the input_tokens-based approximation.
	CacheMissTokens  *int64  `json:"cache_miss_tokens,omitempty"`
	Cost             float64 `json:"cost"`
	LatencyMS        int     `json:"latency_ms"`
	ErrorMessage     string  `json:"error_message"`
	ClientApp        string  `json:"client_app"`
	RequestedModel   string  `json:"requested_model"`
	Complexity       string  `json:"complexity"`
	ThinkingLevel    string  `json:"thinking_level"`
	FailoverAttempts int     `json:"failover_attempts"`
	// UsageEstimated is true when the upstream disconnected before sending
	// its usage payload and InputTokens/OutputTokens/Cost were computed from
	// the local word-count heuristic instead of provider-reported numbers.
	UsageEstimated bool      `json:"usage_estimated"`
	CreatedAt      time.Time `json:"created_at"`
}

type SystemSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type UserTopup struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Credits     float64    `json:"credits"`
	UsedCredits float64    `json:"used_credits"`
	CreatedAt   time.Time  `json:"created_at"`
	// ExpiresAt (migration 012) lapses a top-up on a date; DeletedAt is a soft
	// delete. Either one hides the top-up from all user-facing balances and from
	// consumption (INV-6); admins still see it, badged. Nil = never / not deleted.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	// IdemKey (migration 013) makes a top-up idempotent: a retried insert with the
	// same key is a no-op (ON CONFLICT DO NOTHING), so a gift-card redemption or a
	// double-submit can never double-credit. Nil for ordinary admin top-ups.
	IdemKey *string `json:"idem_key,omitempty"`
}

type DashboardStats struct {
	TotalRequests    int            `json:"total_requests"`
	TotalCost        float64        `json:"total_cost"`
	TotalTokens      int            `json:"total_tokens"`
	AvgLatency       float64        `json:"avg_latency"`
	SuccessRate      float64        `json:"success_rate"`
	CacheReadTokens  int            `json:"cache_read_tokens"`
	CacheWriteTokens int            `json:"cache_write_tokens"`
	CacheHitRate     float64        `json:"cache_hit_rate"`
	DailyStats       []DailyStat    `json:"daily_stats"`
	TopModels        []TopModelStat `json:"top_models"`
}

type DailyStat struct {
	Date     string  `json:"date"`
	Requests int     `json:"requests"`
	Tokens   int     `json:"tokens"`
	Cost     float64 `json:"cost"`
}

type TopModelStat struct {
	Model string `json:"model"`
	Count int    `json:"count"`
}

type DB struct {
	conn *sql.DB
}

func Open(dsn string) (*DB, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres database: %w", err)
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to ping postgres database: %w", err)
	}

	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(25)
	conn.SetConnMaxLifetime(5 * time.Minute)
	conn.SetConnMaxIdleTime(5 * time.Minute)

	if err := RunMigrations(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.backfillVirtualKeyHashes(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to backfill virtual key hashes: %w", err)
	}
	if err := db.seedDefaults(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to seed defaults: %w", err)
	}
	if raw := strings.TrimSpace(os.Getenv("PROVIDER_KEY_ENCRYPTION_KEY")); raw == "" {
		log.Printf("[SECURITY] PROVIDER_KEY_ENCRYPTION_KEY is not set; upstream provider API keys are stored in PLAINTEXT. Set it (32 raw bytes, base64-encoded) to encrypt them at rest.")
	} else if providerKeyCipher() == nil {
		log.Printf("[SECURITY] PROVIDER_KEY_ENCRYPTION_KEY is set but invalid; upstream provider API keys are stored in PLAINTEXT.")
	}

	return db, nil
}

// backfillVirtualKeyHashes computes key_hash for any row that predates
// migration 006 (whose id IS the bearer token clients already send), so
// existing keys keep authenticating after the switch to hash-based lookup.
// Idempotent: only touches rows with a NULL key_hash, safe to run every boot.
func (db *DB) backfillVirtualKeyHashes() error {
	rows, err := db.conn.Query("SELECT id FROM virtual_keys WHERE key_hash IS NULL")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if _, err := db.conn.Exec("UPDATE virtual_keys SET key_hash = $1 WHERE id = $2 AND key_hash IS NULL", hashToken(id), id); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		log.Printf("[MIGRATION] Backfilled key_hash for %d existing virtual key(s)", len(ids))
	}
	return nil
}

// --- Virtual key secrecy helpers ---

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string, nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// --- Provider API key encryption at rest ---
//
// Encrypted with AES-256-GCM when PROVIDER_KEY_ENCRYPTION_KEY (32 raw bytes,
// base64) is configured. Ciphertext carries an "enc:v1:" marker so legacy
// plaintext rows (written before a key was configured) keep working
// unchanged - encryption is opportunistic rather than mandatory, because
// losing the encryption key would otherwise permanently lock out every
// stored upstream credential.

const providerKeyEncPrefix = "enc:v1:"

func providerKeyCipher() cipher.AEAD {
	raw := strings.TrimSpace(os.Getenv("PROVIDER_KEY_ENCRYPTION_KEY"))
	if raw == "" {
		return nil
	}
	keyBytes, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(keyBytes) != 32 {
		return nil
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	return gcm
}

func encryptProviderKey(plaintext string) string {
	gcm := providerKeyCipher()
	if gcm == nil || plaintext == "" {
		return plaintext
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("[SECURITY] failed to generate nonce, storing provider key in PLAINTEXT: %v", err)
		return plaintext
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return providerKeyEncPrefix + base64.StdEncoding.EncodeToString(sealed)
}

func decryptProviderKey(stored string) string {
	if !strings.HasPrefix(stored, providerKeyEncPrefix) {
		return stored // legacy plaintext row, or encryption not configured
	}
	gcm := providerKeyCipher()
	if gcm == nil {
		// Ciphertext exists but no usable key is configured - fail safe by
		// returning empty rather than the undecryptable blob, which would
		// otherwise be sent upstream verbatim as a bogus Authorization value.
		log.Printf("[SECURITY] provider key is encrypted but PROVIDER_KEY_ENCRYPTION_KEY is unavailable/invalid")
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, providerKeyEncPrefix))
	if err != nil || len(raw) < gcm.NonceSize() {
		log.Printf("[SECURITY] failed to decode stored provider key ciphertext")
		return ""
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		log.Printf("[SECURITY] failed to decrypt provider key: %v", err)
		return ""
	}
	return string(plain)
}

func (db *DB) Close() error {
	return db.conn.Close()
}

// PingContext verifies live connectivity with a caller-supplied deadline.
// Used by the runtime watchdog; a ping on a dead pool also dials a fresh
// connection, which is what lets the pool self-heal after an outage.
func (db *DB) PingContext(ctx context.Context) error {
	return db.conn.PingContext(ctx)
}

func (db *DB) seedDefaults() error {
	var exists bool
	err := db.conn.QueryRow("SELECT EXISTS(SELECT 1 FROM system_settings WHERE key = 'seeded_defaults')").Scan(&exists)
	if err == nil && exists {
		return nil
	}

	var count int
	err = db.conn.QueryRow("SELECT COUNT(*) FROM plans").Scan(&count)
	if err == nil && count > 0 {
		return nil
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("INSERT INTO system_settings (key, value) VALUES ($1, $2) ON CONFLICT(key) DO NOTHING", "gateway_name", "MuhiyaLLM Gateway")
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO system_settings (key, value) VALUES ($1, $2) ON CONFLICT(key) DO NOTHING", "theme_accent", "emerald")
	if err != nil {
		return err
	}

	plans := []Plan{
		{ID: "plan-dev", Name: "MuhiyaCode Free", RPMLimit: 30, TPMLimit: 300000},
		{ID: "yalla", Name: "MuhiyaCode Yalla", RPMLimit: 60, TPMLimit: 1200000},
		{ID: "max", Name: "MuhiyaCode Max", RPMLimit: 100, TPMLimit: 2500000},
	}
	for _, p := range plans {
		_, err := tx.Exec(`
			INSERT INTO plans (id, name, rpm_limit, tpm_limit) 
			VALUES ($1, $2, $3, $4) 
			ON CONFLICT (id) DO NOTHING`,
			p.ID, p.Name, p.RPMLimit, p.TPMLimit)
		if err != nil {
			return err
		}
	}

	budgetWindows := []BudgetWindow{
		{ID: "budget-plan-dev-5h", PlanID: "plan-dev", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 0.50},
		{ID: "budget-yalla-5h", PlanID: "yalla", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 25.00},
		{ID: "budget-yalla-31d", PlanID: "yalla", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 25.00},
		{ID: "budget-max-5h", PlanID: "max", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 50.00},
		{ID: "budget-max-31d", PlanID: "max", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 50.00},
	}
	for _, bw := range budgetWindows {
		_, err = tx.Exec(`
			INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd) 
			VALUES ($1, $2, $3, $4, $5) 
			ON CONFLICT (id) DO NOTHING`,
			bw.ID, bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD)
		if err != nil {
			return err
		}
	}

	providers := []Provider{
		{ID: "openai", Name: "OpenAI", APIKey: "mock-openai-key", BaseURL: "https://api.openai.com/v1", AnthropicBaseURL: "", Status: "active"},
		{ID: "anthropic", Name: "Anthropic", APIKey: "mock-anthropic-key", BaseURL: "", AnthropicBaseURL: "https://api.anthropic.com", Status: "active"},
		{ID: "deepseek", Name: "DeepSeek", APIKey: "mock-deepseek-key", BaseURL: "https://api.deepseek.com", AnthropicBaseURL: "https://api.deepseek.com/anthropic", Status: "active"},
	}
	for _, pr := range providers {
		_, err := tx.Exec(`
			INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO NOTHING`,
			pr.ID, pr.Name, encryptProviderKey(pr.APIKey), pr.BaseURL, pr.AnthropicBaseURL, pr.Status)
		if err != nil {
			return err
		}
	}

	models := []Model{
		{ID: "model-gpt4o", Name: "gpt-4o", ProviderID: "openai", TargetModel: "gpt-4o", InputCostPerMillion: 2.50, OutputCostPerMillion: 10.00, CacheReadCostPerMillion: 1.25, CacheWriteCostPerMillion: 2.50, Status: "active", ModelType: "llm", PricePerMinute: 0.0, Transcribe: false, ContextWindow: 128000, MaxOutputTokens: 4096, DisplayName: "GPT-4o", Description: "OpenAI flagship model", OwnedBy: "openai"},
		{ID: "model-claude", Name: "claude-3-5-sonnet", ProviderID: "anthropic", TargetModel: "claude-3-5-sonnet-20241022", InputCostPerMillion: 3.00, OutputCostPerMillion: 15.00, CacheReadCostPerMillion: 0.30, CacheWriteCostPerMillion: 3.75, Status: "active", ModelType: "llm", PricePerMinute: 0.0, Transcribe: false, ContextWindow: 200000, MaxOutputTokens: 8192, DisplayName: "Claude 3.5 Sonnet", Description: "Anthropic high-intelligence model", OwnedBy: "anthropic"},
		{ID: "model-deepseek", Name: "deepseek-chat", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "active", ModelType: "llm", PricePerMinute: 0.0, Transcribe: false, ContextWindow: 64000, MaxOutputTokens: 8192, DisplayName: "DeepSeek Chat", Description: "DeepSeek cheap general-purpose model", OwnedBy: "deepseek"},
		{ID: "model-deepseek-r1", Name: "deepseek-reasoner", ProviderID: "deepseek", TargetModel: "deepseek-reasoner", InputCostPerMillion: 0.55, OutputCostPerMillion: 2.19, CacheReadCostPerMillion: 0.14, CacheWriteCostPerMillion: 0.55, Status: "active", ModelType: "llm", PricePerMinute: 0.0, Transcribe: false, ContextWindow: 64000, MaxOutputTokens: 8192, DisplayName: "DeepSeek Reasoner", Description: "DeepSeek reasoning model (R1)", OwnedBy: "deepseek"},
		{ID: "model-deepseek-flash", Name: "deepseek-v4-flash", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "active", ModelType: "llm", PricePerMinute: 0.0, Transcribe: false, ContextWindow: 64000, MaxOutputTokens: 8192, DisplayName: "DeepSeek v4 Flash", Description: "DeepSeek flash model", OwnedBy: "deepseek"},
		{ID: "model-whisper", Name: "whisper-1", ProviderID: "openai", TargetModel: "whisper-1", InputCostPerMillion: 0.00, OutputCostPerMillion: 0.00, CacheReadCostPerMillion: 0.00, CacheWriteCostPerMillion: 0.00, Status: "active", ModelType: "transcript", PricePerMinute: 0.006, Transcribe: true, ContextWindow: 0, MaxOutputTokens: 0, DisplayName: "Whisper 1", Description: "OpenAI speech-to-text model", OwnedBy: "openai"},
	}
	for _, m := range models {
		_, err := tx.Exec(`
			INSERT INTO models (
				id, name, provider_id, target_model, input_cost_per_million, 
				output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status, 
				routing_tier, model_type, price_per_minute, transcribe, context_window, max_output_tokens,
				display_name, description, owned_by
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) 
			ON CONFLICT (id) DO NOTHING`,
			m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
			m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status,
			m.RoutingTier, m.ModelType, m.PricePerMinute, m.Transcribe, m.ContextWindow, m.MaxOutputTokens,
			m.DisplayName, m.Description, m.OwnedBy)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec("INSERT INTO system_settings (key, value) VALUES ($1, $2) ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value", "seeded_defaults", "true")
	if err != nil {
		return err
	}

	return tx.Commit()
}

// --- Users CRUD ---

func (db *DB) GetUser(id string) (*User, error) {
	var u User
	err := db.conn.QueryRow("SELECT id, name, email, plan_id, status, created_at, plan_assigned_at FROM users WHERE id = $1", id).
		Scan(&u.ID, &u.Name, &u.Email, &u.PlanID, &u.Status, &u.CreatedAt, &u.PlanAssignedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	usage, err := db.GetUserBudgetUsage(u.ID, u.PlanID)
	if err == nil {
		u.BudgetUsage = usage
	}
	var extra, remaining float64
	err = db.conn.QueryRow("SELECT COALESCE(SUM(credits), 0.0), COALESCE(SUM(credits - used_credits), 0.0) FROM user_topups WHERE user_id = $1"+activeTopupFilter, u.ID).Scan(&extra, &remaining)
	if err == nil {
		u.ExtraCredits = extra
		u.RemainingExtraCredits = remaining
	}
	return &u, nil
}

func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.conn.Query("SELECT id, name, email, plan_id, status, created_at, plan_assigned_at FROM users ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.PlanID, &u.Status, &u.CreatedAt, &u.PlanAssignedAt); err != nil {
			return nil, err
		}
		usage, err := db.GetUserBudgetUsage(u.ID, u.PlanID)
		if err == nil {
			u.BudgetUsage = usage
		}
		var extra, remaining float64
		err = db.conn.QueryRow("SELECT COALESCE(SUM(credits), 0.0), COALESCE(SUM(credits - used_credits), 0.0) FROM user_topups WHERE user_id = $1"+activeTopupFilter, u.ID).Scan(&extra, &remaining)
		if err == nil {
			u.ExtraCredits = extra
			u.RemainingExtraCredits = remaining
		}
		list = append(list, u)
	}
	return list, nil
}

func (db *DB) CreateUser(u User) error {
	if u.PlanAssignedAt.IsZero() {
		u.PlanAssignedAt = time.Now()
	}
	_, err := db.conn.Exec("INSERT INTO users (id, name, email, plan_id, status, plan_assigned_at) VALUES ($1, $2, $3, $4, $5, $6)", u.ID, u.Name, u.Email, u.PlanID, u.Status, u.PlanAssignedAt)
	return err
}

func (db *DB) UpdateUser(u User) error {
	existing, err := db.GetUser(u.ID)
	if err != nil {
		return err
	}
	if existing != nil && existing.PlanID != u.PlanID {
		_, err = db.conn.Exec("UPDATE users SET name = $1, email = $2, plan_id = $3, status = $4, plan_assigned_at = CURRENT_TIMESTAMP WHERE id = $5", u.Name, u.Email, u.PlanID, u.Status, u.ID)
	} else {
		_, err = db.conn.Exec("UPDATE users SET name = $1, email = $2, plan_id = $3, status = $4 WHERE id = $5", u.Name, u.Email, u.PlanID, u.Status, u.ID)
	}
	return err
}

func (db *DB) DeleteUser(id string) error {
	_, err := db.conn.Exec("DELETE FROM users WHERE id = $1", id)
	return err
}

// --- Plans CRUD ---

func (db *DB) GetPlan(id string) (*Plan, error) {
	var p Plan
	err := db.conn.QueryRow("SELECT id, name, rpm_limit, tpm_limit, created_at FROM plans WHERE id = $1", id).
		Scan(&p.ID, &p.Name, &p.RPMLimit, &p.TPMLimit, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	budgets, err := db.ListBudgetWindowsByPlan(p.ID)
	if err != nil {
		return nil, err
	}
	p.BudgetWindows = budgets

	return &p, nil
}

func (db *DB) ListPlans() ([]Plan, error) {
	rows, err := db.conn.Query("SELECT id, name, rpm_limit, tpm_limit, created_at FROM plans ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Plan{}
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.RPMLimit, &p.TPMLimit, &p.CreatedAt); err != nil {
			return nil, err
		}

		budgets, err := db.ListBudgetWindowsByPlan(p.ID)
		if err != nil {
			return nil, err
		}
		p.BudgetWindows = budgets

		list = append(list, p)
	}
	return list, nil
}

func (db *DB) CreatePlan(p Plan) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("INSERT INTO plans (id, name, rpm_limit, tpm_limit) VALUES ($1, $2, $3, $4)", p.ID, p.Name, p.RPMLimit, p.TPMLimit)
	if err != nil {
		return err
	}

	for _, bw := range p.BudgetWindows {
		if bw.ID == "" {
			bw.ID = "budget-" + uuid.New().String()
		}
		_, err = tx.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd) VALUES ($1, $2, $3, $4, $5)",
			bw.ID, p.ID, bw.Name, bw.DurationSeconds, bw.BudgetUSD)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (db *DB) UpdatePlan(p Plan) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("UPDATE plans SET name = $1, rpm_limit = $2, tpm_limit = $3 WHERE id = $4", p.Name, p.RPMLimit, p.TPMLimit, p.ID)
	if err != nil {
		return err
	}

	_, err = tx.Exec("DELETE FROM budget_windows WHERE plan_id = $1", p.ID)
	if err != nil {
		return err
	}

	for _, bw := range p.BudgetWindows {
		if bw.ID == "" {
			bw.ID = "budget-" + uuid.New().String()
		}
		_, err = tx.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd) VALUES ($1, $2, $3, $4, $5)",
			bw.ID, p.ID, bw.Name, bw.DurationSeconds, bw.BudgetUSD)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (db *DB) DeletePlan(id string) error {
	_, err := db.conn.Exec("DELETE FROM plans WHERE id = $1", id)
	return err
}

// --- Budget Windows CRUD ---

func (db *DB) GetBudgetWindow(id string) (*BudgetWindow, error) {
	var bw BudgetWindow
	err := db.conn.QueryRow("SELECT id, plan_id, name, duration_seconds, budget_usd, created_at FROM budget_windows WHERE id = $1", id).
		Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bw, nil
}

func (db *DB) ListBudgetWindows() ([]BudgetWindow, error) {
	rows, err := db.conn.Query("SELECT id, plan_id, name, duration_seconds, budget_usd, created_at FROM budget_windows ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []BudgetWindow{}
	for rows.Next() {
		var bw BudgetWindow
		if err := rows.Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, bw)
	}
	return list, nil
}

func (db *DB) ListBudgetWindowsByPlan(planID string) ([]BudgetWindow, error) {
	rows, err := db.conn.Query("SELECT id, plan_id, name, duration_seconds, budget_usd, created_at FROM budget_windows WHERE plan_id = $1 ORDER BY duration_seconds ASC", planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []BudgetWindow{}
	for rows.Next() {
		var bw BudgetWindow
		if err := rows.Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, bw)
	}
	return list, nil
}

func (db *DB) CreateBudgetWindow(bw BudgetWindow) error {
	_, err := db.conn.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd) VALUES ($1, $2, $3, $4, $5)", bw.ID, bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD)
	return err
}

func (db *DB) UpdateBudgetWindow(bw BudgetWindow) error {
	_, err := db.conn.Exec("UPDATE budget_windows SET plan_id = $1, name = $2, duration_seconds = $3, budget_usd = $4 WHERE id = $5", bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD, bw.ID)
	return err
}

func (db *DB) DeleteBudgetWindow(id string) error {
	_, err := db.conn.Exec("DELETE FROM budget_windows WHERE id = $1", id)
	return err
}

// --- Virtual Keys CRUD ---

// GetVirtualKey resolves a presented bearer token by its hash. Only the hash
// ever touches the database or a comparison - the raw token exists only in
// the caller's memory and, once, in CreateVirtualKey's return value.
func (db *DB) GetVirtualKey(presentedToken string) (*VirtualKey, error) {
	var vk VirtualKey
	err := db.conn.QueryRow("SELECT id, name, user_id, status, expires_at, created_at FROM virtual_keys WHERE key_hash = $1", hashToken(presentedToken)).
		Scan(&vk.ID, &vk.Name, &vk.UserID, &vk.Status, &vk.ExpiresAt, &vk.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &vk, nil
}

// GetVirtualKeyByID looks up a virtual key by its internal (non-secret) ID,
// as used by the admin UI/API - distinct from GetVirtualKey, which resolves
// a presented bearer TOKEN by its hash for request authentication.
func (db *DB) GetVirtualKeyByID(id string) (*VirtualKey, error) {
	var vk VirtualKey
	err := db.conn.QueryRow("SELECT id, name, user_id, status, expires_at, created_at FROM virtual_keys WHERE id = $1", id).
		Scan(&vk.ID, &vk.Name, &vk.UserID, &vk.Status, &vk.ExpiresAt, &vk.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &vk, nil
}

func (db *DB) ListVirtualKeys() ([]VirtualKey, error) {
	rows, err := db.conn.Query("SELECT id, name, user_id, status, expires_at, created_at FROM virtual_keys ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []VirtualKey{}
	for rows.Next() {
		var vk VirtualKey
		if err := rows.Scan(&vk.ID, &vk.Name, &vk.UserID, &vk.Status, &vk.ExpiresAt, &vk.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, vk)
	}
	return list, nil
}

func (db *DB) ListVirtualKeysByUserID(userID string) ([]VirtualKey, error) {
	rows, err := db.conn.Query("SELECT id, name, user_id, status, expires_at, created_at FROM virtual_keys WHERE user_id = $1 ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []VirtualKey{}
	for rows.Next() {
		var vk VirtualKey
		if err := rows.Scan(&vk.ID, &vk.Name, &vk.UserID, &vk.Status, &vk.ExpiresAt, &vk.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, vk)
	}
	return list, nil
}

// CreateVirtualKey generates a fresh random bearer token, stores only its
// hash, and returns it via the result's Token field - the one time it is
// ever available in plaintext. vk.ID becomes an internal (non-secret)
// identifier; if the caller left it blank one is generated.
func (db *DB) CreateVirtualKey(vk VirtualKey) (VirtualKey, error) {
	if vk.ID == "" {
		vk.ID = randomToken("vk-", 12)
	}
	token := randomToken("sk-virt-", 16)
	_, err := db.conn.Exec("INSERT INTO virtual_keys (id, key_hash, name, user_id, status, expires_at) VALUES ($1, $2, $3, $4, $5, $6)",
		vk.ID, hashToken(token), vk.Name, vk.UserID, vk.Status, vk.ExpiresAt)
	if err != nil {
		return VirtualKey{}, err
	}
	vk.Token = token
	return vk, nil
}

func (db *DB) UpdateVirtualKey(vk VirtualKey) error {
	_, err := db.conn.Exec("UPDATE virtual_keys SET name = $1, user_id = $2, status = $3, expires_at = $4 WHERE id = $5", vk.Name, vk.UserID, vk.Status, vk.ExpiresAt, vk.ID)
	return err
}

func (db *DB) DeleteVirtualKey(id string) error {
	_, err := db.conn.Exec("DELETE FROM virtual_keys WHERE id = $1", id)
	return err
}

// --- Providers CRUD ---

func (db *DB) GetProvider(id string) (*Provider, error) {
	var p Provider
	err := db.conn.QueryRow("SELECT id, name, api_key, base_url, COALESCE(anthropic_base_url, ''), status, created_at, updated_at FROM providers WHERE id = $1", id).
		Scan(&p.ID, &p.Name, &p.APIKey, &p.BaseURL, &p.AnthropicBaseURL, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.APIKey = decryptProviderKey(p.APIKey)
	return &p, nil
}

func (db *DB) ListProviders() ([]Provider, error) {
	rows, err := db.conn.Query("SELECT id, name, api_key, base_url, COALESCE(anthropic_base_url, ''), status, created_at, updated_at FROM providers ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Provider{}
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.APIKey, &p.BaseURL, &p.AnthropicBaseURL, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.APIKey = decryptProviderKey(p.APIKey)
		list = append(list, p)
	}
	return list, nil
}

func (db *DB) CreateProvider(p Provider) error {
	_, err := db.conn.Exec("INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status) VALUES ($1, $2, $3, $4, $5, $6)", p.ID, p.Name, encryptProviderKey(p.APIKey), p.BaseURL, p.AnthropicBaseURL, p.Status)
	return err
}

func (db *DB) UpdateProvider(p Provider) error {
	_, err := db.conn.Exec("UPDATE providers SET name = $1, api_key = $2, base_url = $3, anthropic_base_url = $4, status = $5, updated_at = CURRENT_TIMESTAMP WHERE id = $6", p.Name, encryptProviderKey(p.APIKey), p.BaseURL, p.AnthropicBaseURL, p.Status, p.ID)
	return err
}

func (db *DB) DeleteProvider(id string) error {
	_, err := db.conn.Exec("DELETE FROM providers WHERE id = $1", id)
	return err
}

// --- Models CRUD ---

func (db *DB) GetModelByName(name string) (*Model, error) {
	// Legacy convenience alias: a bare "deepseek" resolves to the default chat
	// model. The old "claude"/"openai" aliases were removed — this platform ships
	// real gateway virtual ids (DeepSeek family), never fake Claude/GPT rows, so
	// those aliases only ever 404'd.
	normalizedName := name
	if name == "deepseek" {
		normalizedName = "deepseek-chat"
	}

	// Resolve primarily by the virtual name (the routable slug clients should
	// send). As defense-in-depth, also accept a case-insensitive display_name
	// match so a client that mistakenly echoes back the human-readable label
	// (e.g. "Deepseek V4 Pro") still resolves instead of 404ing. An exact
	// name match is always preferred when both would match.
	var m Model
	err := db.conn.QueryRow(`SELECT id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status,
		COALESCE(routing_tier, 'none'), COALESCE(model_type, 'llm'), COALESCE(price_per_minute, 0.0),
		COALESCE(transcribe, FALSE), created_at, COALESCE(context_window, 0), COALESCE(max_output_tokens, 0),
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, ''), COALESCE(supports_vision, FALSE), COALESCE(supports_thinking, FALSE),
		COALESCE(supports_audio, FALSE), COALESCE(supports_video, FALSE), COALESCE(supports_documents, FALSE), COALESCE(max_attachment_mb, 0), COALESCE(accepted_mime_types, '')
		FROM models
		WHERE status = 'active' AND (name = $1 OR id = $1 OR lower(display_name) = lower($1))
		ORDER BY (name = $1) DESC, (id = $1) DESC
		LIMIT 1`, normalizedName).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.RoutingTier, &m.ModelType,
			&m.PricePerMinute, &m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy, &m.SupportsVision, &m.SupportsThinking,
			&m.SupportsAudio, &m.SupportsVideo, &m.SupportsDocuments, &m.MaxAttachmentMB, &m.AcceptedMimeTypes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (db *DB) GetModel(id string) (*Model, error) {
	var m Model
	err := db.conn.QueryRow(`SELECT id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status,
		COALESCE(routing_tier, 'none'), COALESCE(model_type, 'llm'), COALESCE(price_per_minute, 0.0),
		COALESCE(transcribe, FALSE), created_at, COALESCE(context_window, 0), COALESCE(max_output_tokens, 0),
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, ''), COALESCE(supports_vision, FALSE), COALESCE(supports_thinking, FALSE),
		COALESCE(supports_audio, FALSE), COALESCE(supports_video, FALSE), COALESCE(supports_documents, FALSE), COALESCE(max_attachment_mb, 0), COALESCE(accepted_mime_types, '')
		FROM models WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.RoutingTier, &m.ModelType,
			&m.PricePerMinute, &m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy, &m.SupportsVision, &m.SupportsThinking,
			&m.SupportsAudio, &m.SupportsVideo, &m.SupportsDocuments, &m.MaxAttachmentMB, &m.AcceptedMimeTypes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (db *DB) ListModels() ([]Model, error) {
	rows, err := db.conn.Query(`SELECT id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status,
		COALESCE(routing_tier, 'none'), COALESCE(model_type, 'llm'), COALESCE(price_per_minute, 0.0),
		COALESCE(transcribe, FALSE), created_at, COALESCE(context_window, 0), COALESCE(max_output_tokens, 0),
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, ''), COALESCE(supports_vision, FALSE), COALESCE(supports_thinking, FALSE),
		COALESCE(supports_audio, FALSE), COALESCE(supports_video, FALSE), COALESCE(supports_documents, FALSE), COALESCE(max_attachment_mb, 0), COALESCE(accepted_mime_types, '')
		FROM models ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Model{}
	for rows.Next() {
		var m Model
		err := rows.Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.RoutingTier, &m.ModelType,
			&m.PricePerMinute, &m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy, &m.SupportsVision, &m.SupportsThinking,
			&m.SupportsAudio, &m.SupportsVideo, &m.SupportsDocuments, &m.MaxAttachmentMB, &m.AcceptedMimeTypes)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (db *DB) CreateModel(m Model) error {
	if m.RoutingTier == "" {
		m.RoutingTier = "none"
	}
	if m.ModelType == "" {
		m.ModelType = "llm"
	}
	_, err := db.conn.Exec(`INSERT INTO models (
		id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status,
		routing_tier, model_type, price_per_minute, transcribe, context_window, max_output_tokens,
		display_name, description, owned_by, supports_vision, supports_thinking,
		supports_audio, supports_video, supports_documents, max_attachment_mb, accepted_mime_types
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
		m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
		m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status,
		m.RoutingTier, m.ModelType, m.PricePerMinute, m.Transcribe, m.ContextWindow, m.MaxOutputTokens,
		m.DisplayName, m.Description, m.OwnedBy, m.SupportsVision, m.SupportsThinking,
		m.SupportsAudio, m.SupportsVideo, m.SupportsDocuments, m.MaxAttachmentMB, m.AcceptedMimeTypes)
	return err
}

func (db *DB) UpdateModel(m Model) error {
	if m.RoutingTier == "" {
		m.RoutingTier = "none"
	}
	if m.ModelType == "" {
		m.ModelType = "llm"
	}
	_, err := db.conn.Exec(`UPDATE models SET name = $1, provider_id = $2, target_model = $3,
		input_cost_per_million = $4, output_cost_per_million = $5,
		cache_read_cost_per_million = $6, cache_write_cost_per_million = $7, status = $8, routing_tier = $9,
		model_type = $10, price_per_minute = $11, transcribe = $12, context_window = $13, max_output_tokens = $14,
		display_name = $15, description = $16, owned_by = $17, supports_vision = $18,
		supports_thinking = $19, supports_audio = $20, supports_video = $21, supports_documents = $22,
		max_attachment_mb = $23, accepted_mime_types = $24 WHERE id = $25`,
		m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion, m.OutputCostPerMillion,
		m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status, m.RoutingTier, m.ModelType,
		m.PricePerMinute, m.Transcribe, m.ContextWindow, m.MaxOutputTokens, m.DisplayName, m.Description,
		m.OwnedBy, m.SupportsVision, m.SupportsThinking, m.SupportsAudio, m.SupportsVideo,
		m.SupportsDocuments, m.MaxAttachmentMB, m.AcceptedMimeTypes, m.ID)
	return err
}

func (db *DB) DeleteModel(id string) error {
	_, err := db.conn.Exec("DELETE FROM models WHERE id = $1", id)
	return err
}

// --- System Settings CRUD ---

func (db *DB) GetSetting(key string) (string, error) {
	var val string
	err := db.conn.QueryRow("SELECT value FROM system_settings WHERE key = $1", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (db *DB) ListSettings() ([]SystemSetting, error) {
	rows, err := db.conn.Query("SELECT key, value FROM system_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []SystemSetting{}
	for rows.Next() {
		var s SystemSetting
		if err := rows.Scan(&s.Key, &s.Value); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, nil
}

func (db *DB) SetSetting(key, value string) error {
	_, err := db.conn.Exec("INSERT INTO system_settings (key, value) VALUES ($1, $2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", key, value)
	return err
}

// CountMigrations returns how many migrations have been applied (the row count
// of the _migrations tracking table). Surfaced on /health so a deploy can be
// verified — "is the new binary live?" becomes a single curl instead of a guess.
func (db *DB) CountMigrations() (int, error) {
	var n int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM _migrations").Scan(&n)
	return n, err
}

// --- Logging & Budget Queries ---

func (db *DB) InsertRequestLog(log RequestLog) error {
	// Every budget path sums this column (GetUserSpendingInWindow and friends), so
	// one negative row subtracts from a user's measured spend and hands them budget
	// they never paid for. No caller has a legitimate negative: calculateCost floors
	// every component at zero. Reject rather than clamp — a clamped row would
	// silently persist a forged billing record.
	if log.Cost < 0 {
		return fmt.Errorf("refusing to insert request log %s: negative cost %.6f", log.ID, log.Cost)
	}

	var modelID, providerID interface{}
	if log.ModelID != "" {
		modelID = log.ModelID
	}
	if log.ProviderID != "" {
		providerID = log.ProviderID
	}

	_, err := db.conn.Exec(`INSERT INTO request_logs (
		id, virtual_key_id, user_id, model_id, provider_id, request_path, status_code,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cache_miss_tokens, cost, latency_ms, error_message, created_at, client_app,
		requested_model, complexity, failover_attempts, thinking_level, usage_estimated
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`,
		log.ID, log.VirtualKeyID, log.UserID, modelID, providerID, log.RequestPath, log.StatusCode,
		log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.CacheWriteTokens, log.CacheMissTokens, log.Cost, log.LatencyMS, log.ErrorMessage, log.CreatedAt, log.ClientApp,
		log.RequestedModel, log.Complexity, log.FailoverAttempts, log.ThinkingLevel, log.UsageEstimated)

	if err == nil && log.StatusCode >= 200 && log.StatusCode < 300 && log.Cost > 0 {
		if derr := db.DeductExtraCreditsIfExceeded(log.UserID, log.Cost); derr != nil {
			// F2: never swallow a credit-deduction failure — the billing row is
			// already committed, so a lost deduction is a revenue leak. Surface it
			// with request context for the ops log / alerting.
			logCreditFailure(log.UserID, log.Cost, derr)
		}
	}
	return err
}

// logCreditFailure surfaces a (no-longer-swallowed) credit-deduction error (F2).
// It uses the package-level standard logger because inside InsertRequestLog the
// identifier `log` is shadowed by the RequestLog parameter.
func logCreditFailure(userID string, costUSD float64, err error) {
	log.Printf("[BILLING] credit deduction failed for user %s (cost %.6f): %v", userID, costUSD, err)
}

func (db *DB) ListRequestLogs(limit int, offset int, userID string, keyID string) ([]RequestLog, error) {
	// virtual_key_id / user_id are COALESCEd because migration 005 makes them
	// nullable (ON DELETE SET NULL): a log whose user or key was later deleted
	// survives with a null reference and must still scan into a string.
	query := `
		SELECT request_logs.id, COALESCE(request_logs.virtual_key_id, ''), COALESCE(request_logs.user_id, ''), COALESCE(models.name, request_logs.model_id, ''), COALESCE(request_logs.provider_id, ''), request_logs.request_path, request_logs.status_code,
		       request_logs.input_tokens, request_logs.output_tokens, request_logs.cache_read_tokens, request_logs.cache_write_tokens, request_logs.cost, request_logs.latency_ms, COALESCE(request_logs.error_message, ''), request_logs.created_at, COALESCE(request_logs.client_app, ''),
		       COALESCE(request_logs.requested_model, ''), COALESCE(request_logs.complexity, ''), COALESCE(request_logs.failover_attempts, 0), COALESCE(request_logs.thinking_level, ''), COALESCE(request_logs.usage_estimated, FALSE)
		FROM request_logs
		LEFT JOIN models ON request_logs.model_id = models.id
		WHERE 1=1`

	args := []interface{}{}
	paramCount := 1

	if userID != "" {
		query += fmt.Sprintf(" AND request_logs.user_id = $%d", paramCount)
		args = append(args, userID)
		paramCount++
	}

	if keyID != "" {
		query += fmt.Sprintf(" AND request_logs.virtual_key_id = $%d", paramCount)
		args = append(args, keyID)
		paramCount++
	}

	query += fmt.Sprintf(" ORDER BY request_logs.created_at DESC LIMIT $%d OFFSET $%d", paramCount, paramCount+1)
	args = append(args, limit, offset)

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []RequestLog{}
	for rows.Next() {
		var r RequestLog
		err := rows.Scan(&r.ID, &r.VirtualKeyID, &r.UserID, &r.ModelID, &r.ProviderID, &r.RequestPath, &r.StatusCode,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.Cost, &r.LatencyMS, &r.ErrorMessage, &r.CreatedAt, &r.ClientApp,
			&r.RequestedModel, &r.Complexity, &r.FailoverAttempts, &r.ThinkingLevel, &r.UsageEstimated)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, nil
}

func (db *DB) GetUserSpendingInWindow(userID string, durationSeconds int) (float64, error) {
	var planAssignedAt time.Time
	var usageResetAt sql.NullTime
	err := db.conn.QueryRow("SELECT plan_assigned_at, usage_reset_at FROM users WHERE id = $1", userID).Scan(&planAssignedAt, &usageResetAt)
	if err != nil {
		return 0, err
	}
	floor := effectiveFloor(windowPeriodStart(planAssignedAt, durationSeconds, time.Now()), usageResetAt)
	var total float64
	err = db.conn.QueryRow("SELECT COALESCE(SUM(cost), 0.0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300", userID, floor).Scan(&total)
	return total, err
}

func (db *DB) GetRemainingExtraCredits(userID string) (float64, error) {
	var remaining float64
	err := db.conn.QueryRow("SELECT COALESCE(SUM(credits - used_credits), 0.0) FROM user_topups WHERE user_id = $1"+activeTopupFilter, userID).Scan(&remaining)
	return remaining, err
}

// GetUserSpendingToday sums a user's cost over the current UTC calendar day
// (003 T005, usage-api.md §2). It uses the same 2xx-only status filter as
// GetUserSpendingInWindow/GetUserBudgetUsage so the "today" figure sums the
// identical row set as the budget windows' current_spent.
func (db *DB) GetUserSpendingToday(userID string) (float64, error) {
	var total float64
	err := db.conn.QueryRow(
		"SELECT COALESCE(SUM(cost), 0.0) FROM request_logs WHERE user_id = $1 AND status_code >= 200 AND status_code < 300 AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC')",
		userID,
	).Scan(&total)
	return total, err
}

// DeductExtraCreditsIfExceeded charges a user's top-up credits for the over-budget
// portion of a just-billed request, atomically and exactly once (findings F1/F7).
// The whole operation — spend read, overage computation, and credit deduction —
// runs in ONE transaction under a per-user advisory lock, so concurrent 2xx
// requests for the same user serialize instead of racing. Each budget window keeps
// a charge watermark (migration 010): the amount charged is the increase in
// over-budget spend since the last charge, so a replay or a concurrent double-read
// charges nothing. plan_assigned_at is read once (finding F6) and every window's
// period start derives from it via windowPeriodStart. The math lives in billing.go
// as pure, unit-tested functions.
func (db *DB) DeductExtraCreditsIfExceeded(userID string, costUSD float64) error {
	if costUSD <= 0 {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Serialize all credit accounting for this user; the lock releases on
	// commit/rollback. hashtext maps the id into the advisory-lock key space.
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext($1))", userID); err != nil {
		return err
	}

	var planID string
	var planAssignedAt time.Time
	var usageResetAt sql.NullTime
	if err := tx.QueryRow("SELECT plan_id, plan_assigned_at, usage_reset_at FROM users WHERE id = $1", userID).Scan(&planID, &planAssignedAt, &usageResetAt); err != nil {
		return err
	}
	windows, err := listBudgetWindowsByPlanTx(tx, planID)
	if err != nil {
		return err
	}

	now := time.Now()
	var maxCredits float64
	for _, w := range windows {
		if w.BudgetUSD <= 0 {
			continue
		}
		floor := effectiveFloor(windowPeriodStart(planAssignedAt, w.DurationSeconds, now), usageResetAt)
		currentSpend, err := spendInWindowTx(tx, userID, floor)
		if err != nil {
			return err
		}
		lastBilled, err := loadChargeWatermark(tx, userID, w.ID, currentSpend, costUSD)
		if err != nil {
			return err
		}
		credits, newWatermark := overageChargeCredits(currentSpend, w.BudgetUSD, lastBilled)
		if credits > maxCredits {
			maxCredits = credits
		}
		if err := saveChargeWatermark(tx, userID, w.ID, newWatermark); err != nil {
			return err
		}
	}

	// Charge the single worst window's marginal overage — unchanged semantics:
	// overlapping windows (e.g. 5h and monthly) count the same spend, so summing
	// them would double-charge.
	if maxCredits > 0 {
		if err := deductFromTopups(tx, userID, maxCredits); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListUserTopups returns a user's top-ups for the ADMIN view, newest first. Unlike
// the user-facing sums it does NOT hide expired/deleted rows — admins keep full
// visibility (badged in the UI); only the money paths apply activeTopupFilter.
func (db *DB) ListUserTopups(userID string) ([]UserTopup, error) {
	rows, err := db.conn.Query("SELECT id, user_id, credits, used_credits, created_at, expires_at, deleted_at FROM user_topups WHERE user_id = $1 ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []UserTopup{}
	for rows.Next() {
		var u UserTopup
		var expiresAt, deletedAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.UserID, &u.Credits, &u.UsedCredits, &u.CreatedAt, &expiresAt, &deletedAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			u.ExpiresAt = &expiresAt.Time
		}
		if deletedAt.Valid {
			u.DeletedAt = &deletedAt.Time
		}
		list = append(list, u)
	}
	return list, nil
}

// CreateUserTopup inserts a top-up. When IdemKey is set, a second insert with the
// same key is silently ignored (migration 013 partial unique index) so retries and
// gift-card redemptions can never double-credit. A NULL IdemKey never conflicts.
func (db *DB) CreateUserTopup(t UserTopup) error {
	_, err := db.conn.Exec(
		"INSERT INTO user_topups (id, user_id, credits, used_credits, created_at, expires_at, idem_key) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (idem_key) WHERE idem_key IS NOT NULL DO NOTHING",
		t.ID, t.UserID, t.Credits, t.UsedCredits, t.CreatedAt, t.ExpiresAt, t.IdemKey)
	return err
}

// DeleteUserTopup soft-deletes a top-up (migration 012): it vanishes from every
// user-facing balance immediately (INV-6) while the row survives for audit. A
// second delete is a no-op.
func (db *DB) DeleteUserTopup(id string) error {
	_, err := db.conn.Exec("UPDATE user_topups SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", id)
	return err
}

// SetUserTopupExpiry sets or clears a top-up's expiry (pass nil to make it
// permanent). Takes effect immediately across the user-facing balances.
func (db *DB) SetUserTopupExpiry(id string, expiresAt *time.Time) error {
	_, err := db.conn.Exec("UPDATE user_topups SET expires_at = $2 WHERE id = $1", id, expiresAt)
	return err
}

func (db *DB) GetUserBudgetUsage(userID string, planID string) ([]UserBudgetUsage, error) {
	var planAssignedAt time.Time
	var usageResetAt sql.NullTime
	err := db.conn.QueryRow("SELECT plan_assigned_at, usage_reset_at FROM users WHERE id = $1", userID).Scan(&planAssignedAt, &usageResetAt)
	if err != nil {
		return nil, err
	}

	windows, err := db.ListBudgetWindowsByPlan(planID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var usage []UserBudgetUsage
	for _, w := range windows {
		periodStart := windowPeriodStart(planAssignedAt, w.DurationSeconds, now)
		// resetTime derives from plan_assigned_at ONLY — a bonus reset never shifts
		// the displayed schedule (INV-5). The floor only lowers the spend sum.
		resetTime := periodStart.Add(time.Duration(w.DurationSeconds) * time.Second)
		floor := effectiveFloor(periodStart, usageResetAt)

		var spent float64
		err := db.conn.QueryRow("SELECT COALESCE(SUM(cost), 0.0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300", userID, floor).Scan(&spent)
		if err != nil {
			return nil, err
		}

		usage = append(usage, UserBudgetUsage{
			WindowID:        w.ID,
			Name:            w.Name,
			DurationSeconds: w.DurationSeconds,
			BudgetUSD:       w.BudgetUSD,
			CurrentSpent:    spent,
			ResetTime:       &resetTime,
		})
	}

	return usage, nil
}

func (db *DB) GetDashboardStats() (*DashboardStats, error) {
	var stats DashboardStats

	err := db.conn.QueryRow(`
		SELECT 
			COUNT(*), 
			COALESCE(SUM(cost), 0.0), 
			COALESCE(SUM(input_tokens + output_tokens), 0),
			COALESCE(AVG(latency_ms), 0.0),
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN 1 ELSE 0 END) * 100.0 / NULLIF(COUNT(*), 0), 100.0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(cache_read_tokens) * 100.0 / NULLIF(SUM(CASE WHEN cache_miss_tokens IS NOT NULL THEN cache_read_tokens + cache_miss_tokens ELSE input_tokens END), 0), 0.0)
		FROM request_logs`).Scan(&stats.TotalRequests, &stats.TotalCost, &stats.TotalTokens, &stats.AvgLatency, &stats.SuccessRate, &stats.CacheReadTokens, &stats.CacheWriteTokens, &stats.CacheHitRate)
	if err != nil {
		return nil, err
	}

	daysLimit := time.Now().Add(-7 * 24 * time.Hour)
	rows, err := db.conn.Query(`
		SELECT 
			to_char(created_at, 'YYYY-MM-DD') as day, 
			COUNT(*), 
			SUM(input_tokens + output_tokens), 
			SUM(cost)
		FROM request_logs 
		WHERE created_at >= $1
		GROUP BY day 
		ORDER BY day ASC`, daysLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ds DailyStat
		var tokens sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&ds.Date, &ds.Requests, &tokens, &cost); err != nil {
			return nil, err
		}
		ds.Tokens = int(tokens.Int64)
		ds.Cost = cost.Float64
		stats.DailyStats = append(stats.DailyStats, ds)
	}

	modelRows, err := db.conn.Query(`
		SELECT 
			COALESCE(models.name, 'unknown') as model_name, 
			COUNT(*) as req_count
		FROM request_logs
		LEFT JOIN models ON request_logs.model_id = models.id
		GROUP BY model_name
		ORDER BY req_count DESC
		LIMIT 5`)
	if err != nil {
		return nil, err
	}
	defer modelRows.Close()

	for modelRows.Next() {
		var tms TopModelStat
		if err := modelRows.Scan(&tms.Model, &tms.Count); err != nil {
			return nil, err
		}
		stats.TopModels = append(stats.TopModels, tms)
	}

	return &stats, nil
}

// PruneOldRequestLogs deletes request_logs rows older than the given
// duration. request_logs has no built-in retention, so a long-running
// deployment grows it - and the cost of every dashboard/budget aggregate
// query - without bound. Called periodically by main.go's retention
// sweeper, gated by REQUEST_LOG_RETENTION_DAYS (opt-in: unset disables it,
// since some deployments want to keep the full audit history).
func (db *DB) PruneOldRequestLogs(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	res, err := db.conn.Exec("DELETE FROM request_logs WHERE created_at < $1", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
