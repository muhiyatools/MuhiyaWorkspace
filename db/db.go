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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gateway/money"
	"gateway/pricing"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

var (
	ErrPlanInUse          = errors.New("plan is assigned to one or more users")
	ErrInvalidModelConfig = errors.New("invalid model configuration")
)

type UserBudgetUsage struct {
	WindowID            string        `json:"window_id"`
	Name                string        `json:"name"`
	DurationSeconds     int           `json:"duration_seconds"`
	BudgetUSD           float64       `json:"budget_usd"`
	BudgetNanoUSD       money.NanoUSD `json:"budget_nano_usd"`
	CurrentSpent        float64       `json:"current_spent"`
	CurrentSpentNanoUSD money.NanoUSD `json:"current_spent_nano_usd"`
	ResetTime           *time.Time    `json:"reset_time,omitempty"`
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
	ExtraCreditsNanoUSD   money.NanoUSD     `json:"extra_credits_nano_usd"`
	RemainingExtraNanoUSD money.NanoUSD     `json:"remaining_extra_nano_usd"`
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
	ID              string        `json:"id"`
	PlanID          string        `json:"plan_id"`
	Name            string        `json:"name"`
	DurationSeconds int           `json:"duration_seconds"`
	BudgetUSD       float64       `json:"budget_usd"`
	BudgetNanoUSD   money.NanoUSD `json:"budget_nano_usd"`
	CreatedAt       time.Time     `json:"created_at"`
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
	// OwnerStatus is the status of the user this key belongs to, populated only
	// by GetVirtualKey (the authentication path). A key can be active while its
	// owner is suspended — there is no cascade — so authentication has to check
	// both. Not serialized: it is a join artifact, not a property of the key.
	OwnerStatus string `json:"-"`
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
	ID                           string         `json:"id"`
	Name                         string         `json:"name"` // virtual model name (e.g. gpt-4o)
	ProviderID                   string         `json:"provider_id"`
	TargetModel                  string         `json:"target_model"` // provider target name
	InputCostPerMillion          float64        `json:"input_cost_per_million"`
	OutputCostPerMillion         float64        `json:"output_cost_per_million"`
	CacheReadCostPerMillion      float64        `json:"cache_read_cost_per_million"`
	CacheWriteCostPerMillion     float64        `json:"cache_write_cost_per_million"`
	InputCostNanoPerMillion      money.NanoUSD  `json:"input_cost_nano_usd_per_million"`
	OutputCostNanoPerMillion     money.NanoUSD  `json:"output_cost_nano_usd_per_million"`
	CacheReadCostNanoPerMillion  money.NanoUSD  `json:"cache_read_cost_nano_usd_per_million"`
	CacheWriteCostNanoPerMillion money.NanoUSD  `json:"cache_write_cost_nano_usd_per_million"`
	Status                       string         `json:"status"`       // active, inactive
	RoutingTier                  string         `json:"routing_tier"` // none, simple, medium, hard
	ModelType                    string         `json:"model_type"`   // llm, transcript
	PricePerMinute               float64        `json:"price_per_minute"`
	PricePerMinuteNano           money.NanoUSD  `json:"price_per_minute_nano_usd"`
	PricingTiers                 []pricing.Tier `json:"pricing_tiers,omitempty"`
	// CacheTTLRates prices cache entries by requested lifetime; PriceWindows
	// scales rates by time of day. Both are empty for most models, in which
	// case pricing resolves exactly as it did before they existed.
	CacheTTLRates []pricing.TTLRate `json:"cache_ttl_rates,omitempty"`
	PriceWindows  []pricing.Window  `json:"price_windows,omitempty"`
	// PromptAccounting says whether this provider's reported prompt token
	// count already includes cached tokens. Getting it wrong bills fresh
	// input at zero, so it is stored per model rather than guessed.
	PromptAccounting string `json:"prompt_accounting,omitempty"`
	CatalogRecordID              string         `json:"catalog_record_id,omitempty"`
	Tags                         []string       `json:"tags,omitempty"`
	ProviderFamily               string         `json:"provider_family,omitempty"`
	AdapterVersion               string         `json:"adapter_version,omitempty"`
	CompatibilityEpoch           int            `json:"compatibility_epoch,omitempty"`
	CacheContract                string         `json:"cache_contract,omitempty"`
	SupportedParameters          []string       `json:"supported_parameters,omitempty"`
	PricingRuleSetID             string         `json:"pricing_rule_set_id,omitempty"`
	Health                       string         `json:"health,omitempty"`
	DeprecatedAt                 *time.Time     `json:"deprecated_at,omitempty"`
	DeprecationMessage           string         `json:"deprecation_message,omitempty"`
	Transcribe                   bool           `json:"transcribe"`
	ContextWindow                int            `json:"context_window"`
	MaxOutputTokens              int            `json:"max_output_tokens"`
	DisplayName                  string         `json:"display_name"`
	Description                  string         `json:"description"`
	OwnedBy                      string         `json:"owned_by"`
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
	AcceptedMimeTypes string `json:"accepted_mime_types"`
	// MuhiyaCodeVisible marks a model as discoverable by the MuhiyaCode app
	// (X-Client-App: MuhiyaCode) on /v1/models. Discovery-only: inference by
	// exact name and router selection are NOT gated by this flag, so a hidden
	// model stays fully usable — it just does not appear in the coding agent's
	// model picker. Opt-in for new rows (see migration 021).
	MuhiyaCodeVisible bool      `json:"muhiyacode_visible"`
	CreatedAt         time.Time `json:"created_at"`
}

type RequestLog struct {
	ID               string `json:"id"`
	VirtualKeyID     string `json:"virtual_key_id"`
	UserID           string `json:"user_id"`
	OwnerName        string `json:"owner_name,omitempty"`
	SessionID        string `json:"session_id"`
	ClientRequestID  string `json:"client_request_id"`
	AttemptNumber    int    `json:"attempt_number"`
	ModelID          string `json:"model_id"`
	ProviderID       string `json:"provider_id"`
	RequestPath      string `json:"request_path"`
	StatusCode       int    `json:"status_code"`
	RequestStatus    string `json:"request_status"`
	Streamed         bool   `json:"streamed"`
	CacheEpoch       int64  `json:"cache_epoch"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	// CacheMissTokens is the provider-reported non-cached (cache-miss) prompt
	// token count. It is nil for rows logged before feature 007 or by upstreams
	// that do not report it, in which case the dashboard hit-rate falls back to
	// the input_tokens-based approximation.
	CacheMissTokens *int64        `json:"cache_miss_tokens,omitempty"`
	Cost            float64       `json:"cost"`
	CostNanoUSD     money.NanoUSD `json:"cost_nano_usd"`
	CreditsConsumed float64       `json:"credits_consumed"`
	// ChargeCeilingNanoUSD is an ephemeral admission-time cap. Completed
	// request logs remain the sole source of budget-window usage.
	ChargeCeilingNanoUSD money.NanoUSD `json:"-"`
	LatencyMS            int           `json:"latency_ms"`
	ErrorMessage         string        `json:"error_message"`
	ClientApp            string        `json:"client_app"`
	RequestedModel       string        `json:"requested_model"`
	Complexity           string        `json:"complexity"`
	ThinkingLevel        string        `json:"thinking_level"`
	FailoverAttempts     int           `json:"failover_attempts"`
	// UsageEstimated is true when the upstream disconnected before sending
	// its usage payload and InputTokens/OutputTokens/Cost were computed from
	// the local word-count heuristic instead of provider-reported numbers.
	UsageEstimated bool `json:"usage_estimated"`
	// UpstreamProvider is the provider a routing layer actually served this
	// request from, as reported by OpenRouter. Distinct from ProviderID, which
	// is our own catalog row and is known before the request is sent. Empty for
	// direct connections and for rows logged before migration 022.
	//
	// It exists for one question: prompt caches are per-upstream, so a request
	// re-routed to a peer re-reads the whole conversation uncached. Without this
	// column that failure is indistinguishable from a mysterious cache miss.
	UpstreamProvider      string     `json:"upstream_provider,omitempty"`
	BudgetWindowID        string     `json:"budget_window_id,omitempty"`
	BudgetWindowStartedAt *time.Time `json:"budget_window_started_at,omitempty"`
	BudgetWindowResetAt   *time.Time `json:"budget_window_reset_at,omitempty"`

	// Pricing provenance. Together with PricingLines these make a charge
	// reproducible: the cost can be re-derived from this row alone, without
	// consulting the models table, which may since have been re-priced.
	PricingRuleSetID     string           `json:"pricing_rule_set_id,omitempty"`
	PricingTierThreshold *int64           `json:"pricing_tier_threshold,omitempty"`
	PriceWindowID        string           `json:"price_window_id,omitempty"`
	PriceMultiplierNum   int64            `json:"price_multiplier_num,omitempty"`
	PriceMultiplierDen   int64            `json:"price_multiplier_den,omitempty"`
	PricedAt             *time.Time       `json:"priced_at,omitempty"`
	PromptAccounting     string           `json:"prompt_accounting,omitempty"`
	PricingLines         []RequestPriceLine `json:"pricing_lines,omitempty"`
	// UsageAnomaly is set when the upstream's own token numbers were
	// internally inconsistent and had to be clamped.
	UsageAnomaly string `json:"usage_anomaly,omitempty"`
	// UpstreamCostNanoUSD is the provider's own reported cost where it gives
	// one. Divergence from CostNanoUSD means our catalog price has drifted.
	UpstreamCostNanoUSD *money.NanoUSD `json:"upstream_cost_nano_usd,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// RequestPriceLine is one token class's contribution to a request's charge.
type RequestPriceLine struct {
	TokenClass     string        `json:"token_class"`
	Tokens         int64         `json:"tokens"`
	RatePerMillion money.NanoUSD `json:"rate_nano_usd_per_million"`
	Cost           money.NanoUSD `json:"cost_nano_usd"`
}

func RequestStatusForHTTP(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return "succeeded"
	case statusCode == 402:
		return "rejected_budget"
	case statusCode == 429:
		return "rejected_rate_limit"
	case statusCode == 499:
		return "cancelled"
	case statusCode == 504:
		return "timed_out"
	default:
		return "failed"
	}
}

type RequestLogQuery struct {
	Limit   int
	Offset  int
	UserID  string
	KeyID   string
	Search  string
	Status  string
	ModelID string
	Since   time.Time
}

type RequestLogPage struct {
	Items    []RequestLog `json:"items"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

type RequestUsageSummary struct {
	Requests         int64         `json:"requests"`
	Successful       int64         `json:"successful"`
	Failed           int64         `json:"failed"`
	InputTokens      int64         `json:"input_tokens"`
	OutputTokens     int64         `json:"output_tokens"`
	CacheReadTokens  int64         `json:"cache_read_tokens"`
	CacheWriteTokens int64         `json:"cache_write_tokens"`
	CacheMissTokens  int64         `json:"cache_miss_tokens"`
	CostNanoUSD      money.NanoUSD `json:"cost_nano_usd"`
	CostUSD          float64       `json:"cost_usd"`
	CreditsConsumed  float64       `json:"credits_consumed"`
}

type UsageReset struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	UserID    string    `json:"user_id"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type SystemSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type UserTopup struct {
	ID            string        `json:"id"`
	UserID        string        `json:"user_id"`
	Credits       float64       `json:"credits"`
	UsedCredits   float64       `json:"used_credits"`
	AmountNanoUSD money.NanoUSD `json:"amount_nano_usd"`
	UsedNanoUSD   money.NanoUSD `json:"used_nano_usd"`
	CreatedAt     time.Time     `json:"created_at"`
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
	TotalCostNanoUSD money.NanoUSD  `json:"total_cost_nano_usd"`
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
	Date        string        `json:"date"`
	Requests    int           `json:"requests"`
	Tokens      int           `json:"tokens"`
	Cost        float64       `json:"cost"`
	CostNanoUSD money.NanoUSD `json:"cost_nano_usd"`
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
		log.Printf("[SECURITY] PROVIDER_KEY_ENCRYPTION_KEY is not set; existing plaintext provider keys remain usable for rollout compatibility. Configure a 32-byte base64 key before rotating credentials.")
	} else if providerKeyCipher() == nil {
		log.Printf("[SECURITY] PROVIDER_KEY_ENCRYPTION_KEY is invalid; existing plaintext provider keys remain usable, but encrypted provider keys cannot be read. Configure exactly 32 base64-encoded bytes.")
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
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
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
		if err := normalizeBudgetWindowMoney(&bw); err != nil {
			return err
		}
		_, err = tx.Exec(`
			INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO NOTHING`,
			bw.ID, bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD, bw.BudgetNanoUSD)
		if err != nil {
			return err
		}
	}

	providers := []Provider{
		{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Status: "inactive"},
		{ID: "anthropic", Name: "Anthropic", AnthropicBaseURL: "https://api.anthropic.com", Status: "inactive"},
		{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com", AnthropicBaseURL: "https://api.deepseek.com/anthropic", Status: "inactive"},
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
		{ID: "model-gpt4o", Name: "gpt-4o", ProviderID: "openai", TargetModel: "gpt-4o", InputCostPerMillion: 2.50, OutputCostPerMillion: 10.00, CacheReadCostPerMillion: 1.25, CacheWriteCostPerMillion: 2.50, Status: "inactive", ModelType: "llm", ContextWindow: 128000, MaxOutputTokens: 4096, DisplayName: "GPT-4o", Description: "OpenAI flagship model", OwnedBy: "openai"},
		{ID: "model-claude", Name: "claude-3-5-sonnet", ProviderID: "anthropic", TargetModel: "claude-3-5-sonnet-20241022", InputCostPerMillion: 3.00, OutputCostPerMillion: 15.00, CacheReadCostPerMillion: 0.30, CacheWriteCostPerMillion: 3.75, Status: "inactive", ModelType: "llm", ContextWindow: 200000, MaxOutputTokens: 8192, DisplayName: "Claude 3.5 Sonnet", Description: "Anthropic high-intelligence model", OwnedBy: "anthropic"},
		{ID: "model-deepseek", Name: "deepseek-chat", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "inactive", ModelType: "llm", ContextWindow: 64000, MaxOutputTokens: 8192, DisplayName: "DeepSeek Chat", Description: "DeepSeek cheap general-purpose model", OwnedBy: "deepseek"},
		{ID: "model-deepseek-r1", Name: "deepseek-reasoner", ProviderID: "deepseek", TargetModel: "deepseek-reasoner", InputCostPerMillion: 0.55, OutputCostPerMillion: 2.19, CacheReadCostPerMillion: 0.14, CacheWriteCostPerMillion: 0.55, Status: "inactive", ModelType: "llm", ContextWindow: 64000, MaxOutputTokens: 8192, DisplayName: "DeepSeek Reasoner", Description: "DeepSeek reasoning model (R1)", OwnedBy: "deepseek"},
		{ID: "model-deepseek-flash", Name: "deepseek-v4-flash", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "inactive", ModelType: "llm", ContextWindow: 64000, MaxOutputTokens: 8192, DisplayName: "DeepSeek v4 Flash", Description: "DeepSeek flash model", OwnedBy: "deepseek"},
		{ID: "model-whisper", Name: "whisper-1", ProviderID: "openai", TargetModel: "whisper-1", Status: "inactive", ModelType: "transcript", PricePerMinute: 0.006, Transcribe: true, DisplayName: "Whisper 1", Description: "OpenAI speech-to-text model", OwnedBy: "openai"},
	}
	for _, m := range models {
		if err := normalizeModelMoney(&m); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO models (
				id, name, provider_id, target_model, input_cost_per_million, 
				output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million,
				input_cost_nano_usd_per_million, output_cost_nano_usd_per_million,
				cache_read_cost_nano_usd_per_million, cache_write_cost_nano_usd_per_million, status,
				routing_tier, model_type, price_per_minute, price_per_minute_nano_usd, transcribe, context_window, max_output_tokens,
				display_name, description, owned_by
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
			ON CONFLICT (id) DO NOTHING`,
			m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
			m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion,
			m.InputCostNanoPerMillion, m.OutputCostNanoPerMillion, m.CacheReadCostNanoPerMillion, m.CacheWriteCostNanoPerMillion,
			m.Status, m.RoutingTier, m.ModelType, m.PricePerMinute, m.PricePerMinuteNano, m.Transcribe, m.ContextWindow, m.MaxOutputTokens,
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
	if err := db.hydrateUserUsage(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (db *DB) hydrateUserUsage(user *User) error {
	usage, err := db.GetUserBudgetUsage(user.ID, user.PlanID)
	if err != nil {
		return err
	}
	user.BudgetUsage = usage
	var extraNano, remainingNano int64
	err = db.conn.QueryRow("SELECT COALESCE(SUM(amount_nano_usd), 0), COALESCE(SUM(amount_nano_usd - used_nano_usd), 0) FROM user_topups WHERE user_id = $1"+activeTopupFilter, user.ID).Scan(&extraNano, &remainingNano)
	if err != nil {
		return err
	}
	user.ExtraCreditsNanoUSD = money.NanoUSD(extraNano)
	user.RemainingExtraNanoUSD = money.NanoUSD(remainingNano)
	user.ExtraCredits = user.ExtraCreditsNanoUSD.Credits()
	user.RemainingExtraCredits = user.RemainingExtraNanoUSD.Credits()
	return nil
}

func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.conn.Query("SELECT id, name, email, plan_id, status, created_at, plan_assigned_at FROM users ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	list := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.PlanID, &u.Status, &u.CreatedAt, &u.PlanAssignedAt); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, u)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := db.hydrateUsersUsage(list); err != nil {
		return nil, err
	}
	return list, nil
}

func (db *DB) hydrateUsersUsage(users []User) error {
	if len(users) == 0 {
		return nil
	}
	userIndexes := make(map[string]int, len(users))
	for i := range users {
		userIndexes[users[i].ID] = i
		users[i].BudgetUsage = []UserBudgetUsage{}
	}
	if err := db.loadUserTopupSummaries(users, userIndexes); err != nil {
		return err
	}
	return db.loadUserBudgetSummaries(users, userIndexes)
}

func (db *DB) loadUserTopupSummaries(users []User, userIndexes map[string]int) error {
	topupRows, err := db.conn.Query(`SELECT user_id,
		COALESCE(SUM(amount_nano_usd), 0),
		COALESCE(SUM(amount_nano_usd - used_nano_usd), 0)
		FROM user_topups
		WHERE deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		GROUP BY user_id`)
	if err != nil {
		return err
	}
	for topupRows.Next() {
		var userID string
		var total, remaining money.NanoUSD
		if err := topupRows.Scan(&userID, &total, &remaining); err != nil {
			topupRows.Close()
			return err
		}
		if i, ok := userIndexes[userID]; ok {
			users[i].ExtraCreditsNanoUSD = total
			users[i].RemainingExtraNanoUSD = remaining
			users[i].ExtraCredits = total.Credits()
			users[i].RemainingExtraCredits = remaining.Credits()
		}
	}
	if err := topupRows.Close(); err != nil {
		return err
	}
	if err := topupRows.Err(); err != nil {
		return err
	}
	return nil
}

func (db *DB) loadUserBudgetSummaries(users []User, userIndexes map[string]int) error {
	usageRows, err := db.conn.Query(`SELECT u.id, bw.id, bw.name,
		bw.duration_seconds, bw.budget_usd, bw.budget_nano_usd,
		COALESCE((
			SELECT SUM(rl.cost_nano_usd)
			FROM request_logs rl
			WHERE rl.user_id = u.id
			  AND rl.status_code BETWEEN 200 AND 299
			  AND rl.created_at >= GREATEST(periods.period_start, COALESCE(u.usage_reset_at, periods.period_start))
		), 0),
		periods.period_start + bw.duration_seconds * interval '1 second'
		FROM users u
		JOIN budget_windows bw ON bw.plan_id = u.plan_id AND bw.duration_seconds > 0
		CROSS JOIN LATERAL (
			SELECT u.plan_assigned_at +
				floor(extract(epoch FROM (now() - u.plan_assigned_at)) / bw.duration_seconds) *
				bw.duration_seconds * interval '1 second' AS period_start
		) periods
		ORDER BY u.id, bw.duration_seconds`)
	if err != nil {
		return err
	}
	defer usageRows.Close()
	for usageRows.Next() {
		var userID string
		var usage UserBudgetUsage
		var spent money.NanoUSD
		var reset time.Time
		if err := usageRows.Scan(
			&userID, &usage.WindowID, &usage.Name, &usage.DurationSeconds,
			&usage.BudgetUSD, &usage.BudgetNanoUSD, &spent, &reset,
		); err != nil {
			return err
		}
		usage.CurrentSpentNanoUSD = spent
		usage.CurrentSpent = spent.USD()
		usage.ResetTime = &reset
		if i, ok := userIndexes[userID]; ok {
			users[i].BudgetUsage = append(users[i].BudgetUsage, usage)
		}
	}
	return usageRows.Err()
}

func (db *DB) CreateUser(u User) error {
	if u.PlanAssignedAt.IsZero() {
		u.PlanAssignedAt = time.Now().UTC()
	} else {
		u.PlanAssignedAt = u.PlanAssignedAt.UTC()
	}
	_, err := db.conn.Exec("INSERT INTO users (id, name, email, plan_id, status, plan_assigned_at) VALUES ($1, $2, $3, $4, $5, $6)", u.ID, u.Name, u.Email, u.PlanID, u.Status, u.PlanAssignedAt)
	return err
}

func (db *DB) UpdateUser(u User) error {
	var existingPlanID string
	err := db.conn.QueryRow("SELECT plan_id FROM users WHERE id = $1", u.ID).Scan(&existingPlanID)
	if err != nil {
		return err
	}
	if existingPlanID == u.PlanID {
		_, err = db.conn.Exec(
			"UPDATE users SET name = $1, email = $2, status = $3 WHERE id = $4",
			u.Name, u.Email, u.Status, u.ID,
		)
		return err
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET name = $1, email = $2, plan_id = $3,
		status = $4, plan_assigned_at = CURRENT_TIMESTAMP, usage_reset_at = NULL
		WHERE id = $5`, u.Name, u.Email, u.PlanID, u.Status, u.ID); err != nil {
		return err
	}
	return tx.Commit()
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
	list := []Plan{}
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.RPMLimit, &p.TPMLimit, &p.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		budgets, err := db.ListBudgetWindowsByPlan(list[i].ID)
		if err != nil {
			return nil, err
		}
		list[i].BudgetWindows = budgets
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
		if err := normalizeBudgetWindowMoney(&bw); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd) VALUES ($1, $2, $3, $4, $5, $6)",
			bw.ID, p.ID, bw.Name, bw.DurationSeconds, bw.BudgetUSD, bw.BudgetNanoUSD)
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

	updateResult, err := tx.Exec("UPDATE plans SET name = $1, rpm_limit = $2, tpm_limit = $3 WHERE id = $4", p.Name, p.RPMLimit, p.TPMLimit, p.ID)
	if err != nil {
		return err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	_, err = tx.Exec("DELETE FROM budget_windows WHERE plan_id = $1", p.ID)
	if err != nil {
		return err
	}

	for _, bw := range p.BudgetWindows {
		if bw.ID == "" {
			bw.ID = "budget-" + uuid.New().String()
		}
		if err := normalizeBudgetWindowMoney(&bw); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd) VALUES ($1, $2, $3, $4, $5, $6)",
			bw.ID, p.ID, bw.Name, bw.DurationSeconds, bw.BudgetUSD, bw.BudgetNanoUSD)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (db *DB) DeletePlan(id string) error {
	var users int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM users WHERE plan_id = $1", id).Scan(&users); err != nil {
		return err
	}
	if users > 0 {
		return ErrPlanInUse
	}
	deleteResult, err := db.conn.Exec("DELETE FROM plans WHERE id = $1", id)
	if err != nil {
		return err
	}
	affected, err := deleteResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return err
}

// --- Budget Windows CRUD ---

func (db *DB) GetBudgetWindow(id string) (*BudgetWindow, error) {
	var bw BudgetWindow
	err := db.conn.QueryRow("SELECT id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd, created_at FROM budget_windows WHERE id = $1", id).
		Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.BudgetNanoUSD, &bw.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bw, nil
}

func (db *DB) ListBudgetWindows() ([]BudgetWindow, error) {
	rows, err := db.conn.Query("SELECT id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd, created_at FROM budget_windows ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []BudgetWindow{}
	for rows.Next() {
		var bw BudgetWindow
		if err := rows.Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.BudgetNanoUSD, &bw.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, bw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

func (db *DB) ListBudgetWindowsByPlan(planID string) ([]BudgetWindow, error) {
	rows, err := db.conn.Query("SELECT id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd, created_at FROM budget_windows WHERE plan_id = $1 ORDER BY duration_seconds ASC", planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []BudgetWindow{}
	for rows.Next() {
		var bw BudgetWindow
		if err := rows.Scan(&bw.ID, &bw.PlanID, &bw.Name, &bw.DurationSeconds, &bw.BudgetUSD, &bw.BudgetNanoUSD, &bw.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, bw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

func (db *DB) CreateBudgetWindow(bw BudgetWindow) error {
	if err := normalizeBudgetWindowMoney(&bw); err != nil {
		return err
	}
	_, err := db.conn.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd, budget_nano_usd) VALUES ($1, $2, $3, $4, $5, $6)", bw.ID, bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD, bw.BudgetNanoUSD)
	return err
}

func (db *DB) UpdateBudgetWindow(bw BudgetWindow) error {
	if err := normalizeBudgetWindowMoney(&bw); err != nil {
		return err
	}
	_, err := db.conn.Exec("UPDATE budget_windows SET plan_id = $1, name = $2, duration_seconds = $3, budget_usd = $4, budget_nano_usd = $5 WHERE id = $6", bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD, bw.BudgetNanoUSD, bw.ID)
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
// It also returns the OWNING USER'S status, joined here rather than fetched
// separately. Suspending a user sets users.status but leaves their
// virtual_keys.status active, and there is no cascade — so until this join
// existed, the only place any request path consulted the owner's status was
// inside the rate limiter. That made suspension opt-in per handler, and the
// transcription endpoint (which never calls the limiter) honored it for nobody.
// Joining here puts it in the one place every authenticated route passes
// through, and costs no extra round trip.
func (db *DB) GetVirtualKey(presentedToken string) (*VirtualKey, error) {
	var vk VirtualKey
	err := db.conn.QueryRow(`SELECT k.id, k.name, k.user_id, k.status, k.expires_at, k.created_at, u.status
		FROM virtual_keys k JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = $1`, hashToken(presentedToken)).
		Scan(&vk.ID, &vk.Name, &vk.UserID, &vk.Status, &vk.ExpiresAt, &vk.CreatedAt, &vk.OwnerStatus)
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
	if err := rows.Err(); err != nil {
		return nil, err
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
	if err := rows.Err(); err != nil {
		return nil, err
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
	if err := rows.Err(); err != nil {
		return nil, err
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
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million,
		input_cost_nano_usd_per_million, output_cost_nano_usd_per_million,
		cache_read_cost_nano_usd_per_million, cache_write_cost_nano_usd_per_million, status,
		COALESCE(routing_tier, 'none'), COALESCE(model_type, 'llm'), COALESCE(price_per_minute, 0.0),
		COALESCE(price_per_minute_nano_usd, 0),
		COALESCE(transcribe, FALSE), created_at, COALESCE(context_window, 0), COALESCE(max_output_tokens, 0),
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, ''), COALESCE(supports_vision, FALSE), COALESCE(supports_thinking, FALSE),
		COALESCE(supports_audio, FALSE), COALESCE(supports_video, FALSE), COALESCE(supports_documents, FALSE), COALESCE(max_attachment_mb, 0), COALESCE(accepted_mime_types, ''), COALESCE(muhiyacode_visible, FALSE), COALESCE(prompt_accounting, 'inclusive')
		FROM models
		WHERE status = 'active' AND (name = $1 OR id = $1 OR lower(display_name) = lower($1))
		ORDER BY (name = $1) DESC, (id = $1) DESC
		LIMIT 1`, normalizedName).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion,
			&m.InputCostNanoPerMillion, &m.OutputCostNanoPerMillion, &m.CacheReadCostNanoPerMillion, &m.CacheWriteCostNanoPerMillion,
			&m.Status, &m.RoutingTier, &m.ModelType, &m.PricePerMinute, &m.PricePerMinuteNano,
			&m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy, &m.SupportsVision, &m.SupportsThinking,
			&m.SupportsAudio, &m.SupportsVideo, &m.SupportsDocuments, &m.MaxAttachmentMB, &m.AcceptedMimeTypes, &m.MuhiyaCodeVisible, &m.PromptAccounting)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := db.attachPricingTiers(&m); err != nil {
		return nil, err
	}
	if err := db.attachCatalogMetadata(&m); err != nil {
		return nil, err
	}
	if err := db.attachPricingExtras([]*Model{&m}); err != nil {
		return nil, err
	}
	return &m, nil
}

func (db *DB) GetModel(id string) (*Model, error) {
	var m Model
	err := db.conn.QueryRow(`SELECT id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million,
		input_cost_nano_usd_per_million, output_cost_nano_usd_per_million,
		cache_read_cost_nano_usd_per_million, cache_write_cost_nano_usd_per_million, status,
		COALESCE(routing_tier, 'none'), COALESCE(model_type, 'llm'), COALESCE(price_per_minute, 0.0),
		COALESCE(price_per_minute_nano_usd, 0),
		COALESCE(transcribe, FALSE), created_at, COALESCE(context_window, 0), COALESCE(max_output_tokens, 0),
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, ''), COALESCE(supports_vision, FALSE), COALESCE(supports_thinking, FALSE),
		COALESCE(supports_audio, FALSE), COALESCE(supports_video, FALSE), COALESCE(supports_documents, FALSE), COALESCE(max_attachment_mb, 0), COALESCE(accepted_mime_types, ''), COALESCE(muhiyacode_visible, FALSE), COALESCE(prompt_accounting, 'inclusive')
		FROM models WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion,
			&m.InputCostNanoPerMillion, &m.OutputCostNanoPerMillion, &m.CacheReadCostNanoPerMillion, &m.CacheWriteCostNanoPerMillion,
			&m.Status, &m.RoutingTier, &m.ModelType, &m.PricePerMinute, &m.PricePerMinuteNano,
			&m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy, &m.SupportsVision, &m.SupportsThinking,
			&m.SupportsAudio, &m.SupportsVideo, &m.SupportsDocuments, &m.MaxAttachmentMB, &m.AcceptedMimeTypes, &m.MuhiyaCodeVisible, &m.PromptAccounting)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := db.attachPricingTiers(&m); err != nil {
		return nil, err
	}
	if err := db.attachCatalogMetadata(&m); err != nil {
		return nil, err
	}
	if err := db.attachPricingExtras([]*Model{&m}); err != nil {
		return nil, err
	}
	return &m, nil
}

func (db *DB) ListModels() ([]Model, error) {
	rows, err := db.conn.Query(`SELECT id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million,
		input_cost_nano_usd_per_million, output_cost_nano_usd_per_million,
		cache_read_cost_nano_usd_per_million, cache_write_cost_nano_usd_per_million, status,
		COALESCE(routing_tier, 'none'), COALESCE(model_type, 'llm'), COALESCE(price_per_minute, 0.0),
		COALESCE(price_per_minute_nano_usd, 0),
		COALESCE(transcribe, FALSE), created_at, COALESCE(context_window, 0), COALESCE(max_output_tokens, 0),
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, ''), COALESCE(supports_vision, FALSE), COALESCE(supports_thinking, FALSE),
		COALESCE(supports_audio, FALSE), COALESCE(supports_video, FALSE), COALESCE(supports_documents, FALSE), COALESCE(max_attachment_mb, 0), COALESCE(accepted_mime_types, ''), COALESCE(muhiyacode_visible, FALSE), COALESCE(prompt_accounting, 'inclusive')
		FROM models ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Model{}
	for rows.Next() {
		var m Model
		err := rows.Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion,
			&m.InputCostNanoPerMillion, &m.OutputCostNanoPerMillion, &m.CacheReadCostNanoPerMillion, &m.CacheWriteCostNanoPerMillion,
			&m.Status, &m.RoutingTier, &m.ModelType, &m.PricePerMinute, &m.PricePerMinuteNano,
			&m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy, &m.SupportsVision, &m.SupportsThinking,
			&m.SupportsAudio, &m.SupportsVideo, &m.SupportsDocuments, &m.MaxAttachmentMB, &m.AcceptedMimeTypes, &m.MuhiyaCodeVisible, &m.PromptAccounting)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	pointers := make([]*Model, 0, len(list))
	for i := range list {
		if err := db.attachPricingTiers(&list[i]); err != nil {
			return nil, err
		}
		if err := db.attachCatalogMetadata(&list[i]); err != nil {
			return nil, err
		}
		pointers = append(pointers, &list[i])
	}
	if err := db.attachPricingExtras(pointers); err != nil {
		return nil, err
	}
	return list, nil
}

func (db *DB) attachPricingTiers(model *Model) error {
	rows, err := db.conn.Query(`SELECT min_input_tokens_exclusive,
		input_nano_usd_per_million, output_nano_usd_per_million,
		cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
		FROM model_pricing_tiers
		WHERE model_id = $1 AND enabled = TRUE
		ORDER BY min_input_tokens_exclusive ASC`, model.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	model.PricingTiers = nil
	for rows.Next() {
		var tier pricing.Tier
		if err := rows.Scan(
			&tier.MinInputTokensExclusive,
			&tier.Rates.InputPerMillion,
			&tier.Rates.OutputPerMillion,
			&tier.Rates.CacheReadPerMillion,
			&tier.Rates.CacheWritePerMillion,
		); err != nil {
			return err
		}
		model.PricingTiers = append(model.PricingTiers, tier)
	}
	return rows.Err()
}

// attachPricingExtras loads cache-TTL rates and time-of-day price windows for
// a whole model set in two queries rather than two per model: these run on
// every catalog refresh, and a per-model round trip here would be paid on a
// hot path for tables that are empty for almost every model.
func (db *DB) attachPricingExtras(models []*Model) error {
	if len(models) == 0 {
		return nil
	}
	byID := make(map[string]*Model, len(models))
	ids := make([]string, 0, len(models))
	for _, model := range models {
		model.CacheTTLRates = nil
		model.PriceWindows = nil
		byID[model.ID] = model
		ids = append(ids, model.ID)
	}
	if err := db.loadCacheTTLRates(byID, ids); err != nil {
		return err
	}
	return db.loadPriceWindows(byID, ids)
}

func (db *DB) loadCacheTTLRates(byID map[string]*Model, ids []string) error {
	rows, err := db.conn.Query(`SELECT model_id, min_input_tokens_exclusive, ttl,
		cache_read_nano_usd_per_million, cache_write_nano_usd_per_million
		FROM model_cache_ttl_rates
		WHERE model_id = ANY($1) AND enabled = TRUE
		ORDER BY model_id, min_input_tokens_exclusive NULLS FIRST, ttl`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var modelID string
		var threshold sql.NullInt64
		var rate pricing.TTLRate
		var read, write sql.NullInt64
		if err := rows.Scan(&modelID, &threshold, &rate.TTL, &read, &write); err != nil {
			return err
		}
		if threshold.Valid {
			value := threshold.Int64
			rate.MinInputTokensExclusive = &value
		}
		if read.Valid {
			value := money.NanoUSD(read.Int64)
			rate.CacheReadPerMillion = &value
		}
		if write.Valid {
			value := money.NanoUSD(write.Int64)
			rate.CacheWritePerMillion = &value
		}
		if model, ok := byID[modelID]; ok && pricing.ValidTTL(rate.TTL) {
			model.CacheTTLRates = append(model.CacheTTLRates, rate)
		}
	}
	return rows.Err()
}

func (db *DB) loadPriceWindows(byID map[string]*Model, ids []string) error {
	rows, err := db.conn.Query(`SELECT model_id, id, label, start_minute_utc, end_minute_utc,
		weekday_mask, multiplier_num, multiplier_den, applies_to, priority,
		effective_from, effective_until
		FROM model_price_windows
		WHERE model_id = ANY($1) AND enabled = TRUE
		ORDER BY model_id, priority DESC, id`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var modelID, appliesTo string
		var window pricing.Window
		var from, until sql.NullTime
		if err := rows.Scan(&modelID, &window.ID, &window.Label,
			&window.StartMinuteUTC, &window.EndMinuteUTC, &window.WeekdayMask,
			&window.MultiplierNum, &window.MultiplierDen, &appliesTo,
			&window.Priority, &from, &until); err != nil {
			return err
		}
		window.AppliesTo = parseTokenClasses(appliesTo)
		if from.Valid {
			value := from.Time
			window.EffectiveFrom = &value
		}
		if until.Valid {
			value := until.Time
			window.EffectiveUntil = &value
		}
		model, ok := byID[modelID]
		if !ok {
			continue
		}
		// A malformed row must not take the whole catalog down with it: skip
		// it loudly and keep serving the model at its unscaled rates, which
		// is the conservative direction (no surprise multiplier).
		if err := window.Validate(); err != nil {
			log.Printf("[PRICING] ignoring invalid price window %s on model %s: %v",
				window.ID, modelID, err)
			continue
		}
		model.PriceWindows = append(model.PriceWindows, window)
	}
	return rows.Err()
}

func parseTokenClasses(raw string) []pricing.TokenClass {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var classes []pricing.TokenClass
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			classes = append(classes, pricing.TokenClass(part))
		}
	}
	return classes
}

func (db *DB) attachCatalogMetadata(model *Model) error {
	var deprecated sql.NullTime
	err := db.conn.QueryRow(`SELECT tags, provider_family, adapter_version,
		compatibility_epoch, cache_contract::text, supported_parameters,
		pricing_rule_set_id, health, deprecated_at, deprecation_message
		FROM model_catalog_metadata WHERE model_id = $1`, model.ID).Scan(
		pq.Array(&model.Tags),
		&model.ProviderFamily,
		&model.AdapterVersion,
		&model.CompatibilityEpoch,
		&model.CacheContract,
		pq.Array(&model.SupportedParameters),
		&model.PricingRuleSetID,
		&model.Health,
		&deprecated,
		&model.DeprecationMessage,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// Defense in depth: the models AFTER INSERT trigger (026/031) creates a
		// metadata row for every model inserted through Postgres directly, so
		// this should be unreachable in practice. It stays reachable if a
		// trigger is ever disabled, a row is restored from a pre-026 backup, or
		// a future insert path bypasses triggers — and returning silently here
		// previously left ProviderFamily/CacheContract/SupportedParameters at
		// their zero values, which the request builder and the cache-affinity
		// logic would then read as "no cache contract, no supported params"
		// rather than deriving the same name-based defaults every other path
		// gets.
		return applyModelCatalogDefaults(model)
	}
	if err != nil {
		return err
	}
	if deprecated.Valid {
		model.DeprecatedAt = &deprecated.Time
	}
	return nil
}

func normalizeModelCatalog(model *Model) error {
	if err := applyModelCatalogDefaults(model); err != nil {
		return err
	}
	if !json.Valid([]byte(model.CacheContract)) {
		return fmt.Errorf("%w: cache contract must be valid JSON", ErrInvalidModelConfig)
	}
	model.Tags = normalizedCatalogTags(*model)
	return validateModelPricingRules(*model)
}

func applyModelCatalogDefaults(model *Model) error {
	if model.AdapterVersion == "" {
		model.AdapterVersion = "1"
	}
	if model.CompatibilityEpoch <= 0 {
		model.CompatibilityEpoch = 1
	}
	if model.Health == "" {
		if model.Status == "active" {
			model.Health = "healthy"
		} else {
			model.Health = "unavailable"
		}
	}
	switch model.Health {
	case "healthy", "degraded", "unavailable", "unknown":
	default:
		return fmt.Errorf("%w: invalid model health %q", ErrInvalidModelConfig, model.Health)
	}
	if model.ProviderFamily == "" {
		model.ProviderFamily = defaultProviderFamily(model.Name)
	}
	if model.CacheContract == "" {
		model.CacheContract = defaultCacheContract(model.ProviderFamily)
	}
	if model.PricingRuleSetID == "" {
		model.PricingRuleSetID = "pricing:" + model.ID
	}
	// Default the accounting mode from the provider family rather than leaving
	// it blank: an unset mode would be read as inclusive, which for an
	// Anthropic model silently bills its fresh input tokens at zero.
	if model.PromptAccounting == "" {
		if strings.EqualFold(model.ProviderFamily, "anthropic") {
			model.PromptAccounting = string(pricing.PromptExclusive)
		} else {
			model.PromptAccounting = string(pricing.PromptInclusive)
		}
	}
	if !pricing.ParsePromptAccounting(model.PromptAccounting).Valid() ||
		(model.PromptAccounting != string(pricing.PromptInclusive) &&
			model.PromptAccounting != string(pricing.PromptExclusive)) {
		return fmt.Errorf("%w: invalid prompt accounting %q",
			ErrInvalidModelConfig, model.PromptAccounting)
	}
	if len(model.SupportedParameters) == 0 {
		model.SupportedParameters = []string{
			"model", "messages", "stream", "tools", "tool_choice",
			"max_tokens", "temperature", "top_p",
		}
	}
	return nil
}

func defaultProviderFamily(modelName string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(modelName), "minimax"):
		return "minimax-openrouter"
	case strings.HasPrefix(strings.ToLower(modelName), "deepseek"):
		return "deepseek"
	default:
		return "openai-compatible"
	}
}

func defaultCacheContract(providerFamily string) string {
	supportsCache := providerFamily == "minimax-openrouter" || providerFamily == "deepseek"
	routeScoped := providerFamily == "minimax-openrouter"
	return fmt.Sprintf(
		`{"prefix_order":"system-tools-history-tail","route_scoped":%t,"supports_prompt_cache":%t}`,
		routeScoped, supportsCache,
	)
}

func normalizedCatalogTags(model Model) []string {
	tags := make([]string, 0, len(model.Tags)+1)
	seen := make(map[string]bool, len(model.Tags)+1)
	for _, raw := range model.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || strings.EqualFold(tag, "muhiyacode") || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	if model.MuhiyaCodeVisible {
		tags = append(tags, "muhiyacode")
	}
	return tags
}

func validateModelPricingRules(model Model) error {
	_, err := pricing.NewRuleSet(model.PricingRuleSetID, pricing.Rates{
		InputPerMillion:      model.InputCostNanoPerMillion,
		OutputPerMillion:     model.OutputCostNanoPerMillion,
		CacheReadPerMillion:  model.CacheReadCostNanoPerMillion,
		CacheWritePerMillion: model.CacheWriteCostNanoPerMillion,
	}, model.PricingTiers)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidModelConfig, err)
	}
	return nil
}

func saveModelCatalogMetadata(tx *sql.Tx, model Model) error {
	_, err := tx.Exec(`INSERT INTO model_catalog_metadata (
		model_id, tags, provider_family, adapter_version, compatibility_epoch,
		cache_contract, supported_parameters, pricing_rule_set_id, health,
		deprecated_at, deprecation_message, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,now())
	ON CONFLICT (model_id) DO UPDATE SET
		tags=EXCLUDED.tags, provider_family=EXCLUDED.provider_family,
		adapter_version=EXCLUDED.adapter_version,
		compatibility_epoch=EXCLUDED.compatibility_epoch,
		cache_contract=EXCLUDED.cache_contract,
		supported_parameters=EXCLUDED.supported_parameters,
		pricing_rule_set_id=EXCLUDED.pricing_rule_set_id,
		health=EXCLUDED.health, deprecated_at=EXCLUDED.deprecated_at,
		deprecation_message=EXCLUDED.deprecation_message, updated_at=now()`,
		model.ID, pq.Array(model.Tags), model.ProviderFamily, model.AdapterVersion,
		model.CompatibilityEpoch, model.CacheContract, pq.Array(model.SupportedParameters),
		model.PricingRuleSetID, model.Health, model.DeprecatedAt, model.DeprecationMessage)
	return err
}

func (db *DB) CreateModel(m Model) error {
	if err := normalizeModelMoney(&m); err != nil {
		return err
	}
	m.RoutingTier = "none"
	if m.ModelType == "" {
		m.ModelType = "llm"
	}
	if err := normalizeModelCatalog(&m); err != nil {
		return err
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO models (
		id, name, provider_id, target_model, input_cost_per_million,
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million,
		input_cost_nano_usd_per_million, output_cost_nano_usd_per_million,
		cache_read_cost_nano_usd_per_million, cache_write_cost_nano_usd_per_million, status,
		routing_tier, model_type, price_per_minute, price_per_minute_nano_usd, transcribe, context_window, max_output_tokens,
		display_name, description, owned_by, supports_vision, supports_thinking,
		supports_audio, supports_video, supports_documents, max_attachment_mb, accepted_mime_types,
		muhiyacode_visible, prompt_accounting
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32)`,
		m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
		m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion,
		m.InputCostNanoPerMillion, m.OutputCostNanoPerMillion, m.CacheReadCostNanoPerMillion, m.CacheWriteCostNanoPerMillion,
		m.Status, m.RoutingTier, m.ModelType, m.PricePerMinute, m.PricePerMinuteNano, m.Transcribe, m.ContextWindow, m.MaxOutputTokens,
		m.DisplayName, m.Description, m.OwnedBy, m.SupportsVision, m.SupportsThinking,
		m.SupportsAudio, m.SupportsVideo, m.SupportsDocuments, m.MaxAttachmentMB, m.AcceptedMimeTypes,
		m.MuhiyaCodeVisible, m.PromptAccounting)
	if err != nil {
		return err
	}
	if err := saveModelCatalogMetadata(tx, m); err != nil {
		return err
	}
	if err := replaceModelPricingTiers(tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) UpdateModel(m Model) error {
	if err := normalizeModelMoney(&m); err != nil {
		return err
	}
	m.RoutingTier = "none"
	if m.ModelType == "" {
		m.ModelType = "llm"
	}
	if err := normalizeModelCatalog(&m); err != nil {
		return err
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updateResult, err := tx.Exec(`UPDATE models SET name = $1, provider_id = $2, target_model = $3,
		input_cost_per_million = $4, output_cost_per_million = $5,
		cache_read_cost_per_million = $6, cache_write_cost_per_million = $7,
		input_cost_nano_usd_per_million = $8, output_cost_nano_usd_per_million = $9,
		cache_read_cost_nano_usd_per_million = $10, cache_write_cost_nano_usd_per_million = $11,
		status = $12, routing_tier = $13, model_type = $14, price_per_minute = $15,
		price_per_minute_nano_usd = $16, transcribe = $17, context_window = $18, max_output_tokens = $19,
		display_name = $20, description = $21, owned_by = $22, supports_vision = $23,
		supports_thinking = $24, supports_audio = $25, supports_video = $26, supports_documents = $27,
		max_attachment_mb = $28, accepted_mime_types = $29, muhiyacode_visible = $30, prompt_accounting = $31 WHERE id = $32`,
		m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion, m.OutputCostPerMillion,
		m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion,
		m.InputCostNanoPerMillion, m.OutputCostNanoPerMillion, m.CacheReadCostNanoPerMillion, m.CacheWriteCostNanoPerMillion,
		m.Status, m.RoutingTier, m.ModelType, m.PricePerMinute, m.PricePerMinuteNano,
		m.Transcribe, m.ContextWindow, m.MaxOutputTokens, m.DisplayName, m.Description,
		m.OwnedBy, m.SupportsVision, m.SupportsThinking, m.SupportsAudio, m.SupportsVideo,
		m.SupportsDocuments, m.MaxAttachmentMB, m.AcceptedMimeTypes, m.MuhiyaCodeVisible,
		m.PromptAccounting, m.ID)
	if err != nil {
		return err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	if err := saveModelCatalogMetadata(tx, m); err != nil {
		return err
	}
	if err := replaceModelPricingTiers(tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceModelPricingTiers(tx *sql.Tx, model Model) error {
	if _, err := tx.Exec("DELETE FROM model_pricing_tiers WHERE model_id = $1", model.ID); err != nil {
		return err
	}
	for _, tier := range model.PricingTiers {
		if _, err := tx.Exec(`INSERT INTO model_pricing_tiers (
			id, model_id, min_input_tokens_exclusive,
			input_nano_usd_per_million, output_nano_usd_per_million,
			cache_read_nano_usd_per_million, cache_write_nano_usd_per_million,
			enabled
		) VALUES ($1,$2,$3,$4,$5,$6,$7,TRUE)`,
			"pricing-tier-"+uuid.NewString(), model.ID, tier.MinInputTokensExclusive,
			tier.Rates.InputPerMillion, tier.Rates.OutputPerMillion,
			tier.Rates.CacheReadPerMillion, tier.Rates.CacheWritePerMillion,
		); err != nil {
			return err
		}
	}
	return nil
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
	if err := rows.Err(); err != nil {
		return nil, err
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
	if err := normalizeRequestLogMoney(&log); err != nil {
		return fmt.Errorf("refusing to insert request log %s: %w", log.ID, err)
	}

	// The log row and its pricing lines are written together: a charge whose
	// derivation is missing is not auditable, so the two must never diverge.
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertRequestLog(tx, log); err != nil {
		return err
	}
	return tx.Commit()
}

type requestLogExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertRequestLog(exec requestLogExecer, entry RequestLog) error {
	var modelID, providerID, upstream, budgetWindowID any
	if entry.ModelID != "" {
		modelID = entry.ModelID
	}
	if entry.ProviderID != "" {
		providerID = entry.ProviderID
	}
	if entry.UpstreamProvider != "" {
		upstream = entry.UpstreamProvider
	}
	if entry.BudgetWindowID != "" {
		budgetWindowID = entry.BudgetWindowID
	}
	if entry.AttemptNumber <= 0 {
		entry.AttemptNumber = 1
	}
	if entry.RequestStatus == "" {
		entry.RequestStatus = RequestStatusForHTTP(entry.StatusCode)
	}
	if entry.PriceMultiplierNum <= 0 && entry.PriceMultiplierDen <= 0 {
		entry.PriceMultiplierNum, entry.PriceMultiplierDen = 1, 1
	}
	if entry.PriceMultiplierDen <= 0 {
		entry.PriceMultiplierDen = 1
	}
	if entry.PromptAccounting == "" {
		entry.PromptAccounting = string(pricing.PromptInclusive)
	}
	result, err := exec.Exec(`INSERT INTO request_logs (
		id, virtual_key_id, user_id, session_id, client_request_id, attempt_number,
		model_id, provider_id, request_path, status_code, request_status, streamed, cache_epoch,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cache_miss_tokens,
		cost, cost_nano_usd, latency_ms, error_message, created_at, client_app,
		requested_model, complexity, failover_attempts, thinking_level, usage_estimated, upstream_provider,
		budget_window_id, budget_window_started_at, budget_window_reset_at,
		pricing_rule_set_id, pricing_tier_threshold, price_window_id,
		price_multiplier_num, price_multiplier_den, priced_at, prompt_accounting,
		usage_anomaly, upstream_cost_nano_usd
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		$14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
		$25, $26, $27, $28, $29, $30, $31, $32, $33,
		$34, $35, $36, $37, $38, $39, $40, $41, $42
	) ON CONFLICT (id) DO NOTHING`,
		entry.ID, entry.VirtualKeyID, entry.UserID, entry.SessionID, entry.ClientRequestID, entry.AttemptNumber,
		modelID, providerID, entry.RequestPath, entry.StatusCode, entry.RequestStatus, entry.Streamed, entry.CacheEpoch,
		entry.InputTokens, entry.OutputTokens, entry.CacheReadTokens, entry.CacheWriteTokens, entry.CacheMissTokens,
		entry.Cost, entry.CostNanoUSD, entry.LatencyMS, entry.ErrorMessage, entry.CreatedAt, entry.ClientApp,
		entry.RequestedModel, entry.Complexity, entry.FailoverAttempts, entry.ThinkingLevel, entry.UsageEstimated, upstream,
		budgetWindowID, entry.BudgetWindowStartedAt, entry.BudgetWindowResetAt,
		entry.PricingRuleSetID, entry.PricingTierThreshold, entry.PriceWindowID,
		entry.PriceMultiplierNum, entry.PriceMultiplierDen, entry.PricedAt, entry.PromptAccounting,
		entry.UsageAnomaly, entry.UpstreamCostNanoUSD)
	if err != nil {
		return err
	}
	// ON CONFLICT DO NOTHING above means a retry of an already-logged request
	// inserts nothing; its lines are already present and must not be rewritten.
	if affected, affErr := result.RowsAffected(); affErr == nil && affected == 0 {
		return nil
	}
	return insertRequestPricingLines(exec, entry)
}

func insertRequestPricingLines(exec requestLogExecer, entry RequestLog) error {
	for _, line := range entry.PricingLines {
		if _, err := exec.Exec(`INSERT INTO request_pricing_lines (
			request_log_id, token_class, tokens, rate_nano_usd_per_million, cost_nano_usd
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (request_log_id, token_class) DO NOTHING`,
			entry.ID, line.TokenClass, line.Tokens, line.RatePerMillion, line.Cost); err != nil {
			return fmt.Errorf("failed to record pricing line %s for request %s: %w",
				line.TokenClass, entry.ID, err)
		}
	}
	return nil
}

// GetRequestPricingLines returns the stored derivation of one request's charge.
func (db *DB) GetRequestPricingLines(requestID string) ([]RequestPriceLine, error) {
	rows, err := db.conn.Query(`SELECT token_class, tokens,
		rate_nano_usd_per_million, cost_nano_usd
		FROM request_pricing_lines WHERE request_log_id = $1
		ORDER BY token_class`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []RequestPriceLine
	for rows.Next() {
		var line RequestPriceLine
		if err := rows.Scan(&line.TokenClass, &line.Tokens, &line.RatePerMillion, &line.Cost); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

func (db *DB) ListRequestLogs(limit int, offset int, userID string, keyID string) ([]RequestLog, error) {
	page, err := db.ListRequestLogsPage(RequestLogQuery{
		Limit: limit, Offset: offset, UserID: userID, KeyID: keyID,
	})
	return page.Items, err
}

func (db *DB) ListRequestLogsPage(query RequestLogQuery) (RequestLogPage, error) {
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 50
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	where, args := requestLogFilter(query)
	var total int64
	countSQL := `SELECT COUNT(*) FROM request_logs
		LEFT JOIN models ON request_logs.model_id = models.id
		LEFT JOIN users ON request_logs.user_id = users.id WHERE 1=1` + where
	if err := db.conn.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return RequestLogPage{}, err
	}

	// virtual_key_id / user_id are COALESCEd because migration 005 makes them
	// nullable (ON DELETE SET NULL): a log whose user or key was later deleted
	// survives with a null reference and must still scan into a string.
	selectSQL := `
		SELECT request_logs.id, COALESCE(request_logs.virtual_key_id, ''), COALESCE(request_logs.user_id, ''), COALESCE(users.name, ''),
		       request_logs.session_id, request_logs.client_request_id, request_logs.attempt_number,
		       COALESCE(models.name, request_logs.model_id, ''), COALESCE(request_logs.provider_id, ''), request_logs.request_path, request_logs.status_code,
		       request_logs.request_status, request_logs.streamed, request_logs.cache_epoch,
		       request_logs.input_tokens, request_logs.output_tokens, request_logs.cache_read_tokens, request_logs.cache_write_tokens, request_logs.cache_miss_tokens,
		       request_logs.cost, request_logs.cost_nano_usd, request_logs.credits_consumed::double precision,
		       request_logs.latency_ms, COALESCE(request_logs.error_message, ''), request_logs.created_at, COALESCE(request_logs.client_app, ''),
		       COALESCE(request_logs.requested_model, ''), COALESCE(request_logs.complexity, ''), COALESCE(request_logs.failover_attempts, 0), COALESCE(request_logs.thinking_level, ''), COALESCE(request_logs.usage_estimated, FALSE),
		       COALESCE(request_logs.upstream_provider, ''), COALESCE(request_logs.budget_window_id, ''),
		       request_logs.budget_window_started_at, request_logs.budget_window_reset_at
		FROM request_logs
		LEFT JOIN models ON request_logs.model_id = models.id
		LEFT JOIN users ON request_logs.user_id = users.id
		WHERE 1=1` + where
	selectSQL += fmt.Sprintf(" ORDER BY request_logs.created_at DESC, request_logs.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, query.Limit, query.Offset)
	rows, err := db.conn.Query(selectSQL, args...)
	if err != nil {
		return RequestLogPage{}, err
	}
	defer rows.Close()

	list := []RequestLog{}
	for rows.Next() {
		var r RequestLog
		err := rows.Scan(&r.ID, &r.VirtualKeyID, &r.UserID, &r.OwnerName,
			&r.SessionID, &r.ClientRequestID, &r.AttemptNumber,
			&r.ModelID, &r.ProviderID, &r.RequestPath, &r.StatusCode,
			&r.RequestStatus, &r.Streamed, &r.CacheEpoch,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.CacheMissTokens,
			&r.Cost, &r.CostNanoUSD, &r.CreditsConsumed, &r.LatencyMS, &r.ErrorMessage, &r.CreatedAt, &r.ClientApp,
			&r.RequestedModel, &r.Complexity, &r.FailoverAttempts, &r.ThinkingLevel, &r.UsageEstimated, &r.UpstreamProvider,
			&r.BudgetWindowID, &r.BudgetWindowStartedAt, &r.BudgetWindowResetAt)
		if err != nil {
			return RequestLogPage{}, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return RequestLogPage{}, err
	}
	pageNumber := query.Offset/query.Limit + 1
	return RequestLogPage{Items: list, Total: total, Page: pageNumber, PageSize: query.Limit}, nil
}

func requestLogFilter(query RequestLogQuery) (string, []any) {
	var where strings.Builder
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		where.WriteString(fmt.Sprintf(clause, len(args)))
	}
	if query.UserID != "" {
		add(" AND request_logs.user_id = $%d", query.UserID)
	}
	if query.KeyID != "" {
		add(" AND request_logs.virtual_key_id = $%d", query.KeyID)
	}
	if query.ModelID != "" {
		add(" AND COALESCE(models.name, request_logs.model_id, '') = $%d", query.ModelID)
	}
	if !query.Since.IsZero() {
		add(" AND request_logs.created_at >= $%d", query.Since.UTC())
	}
	switch query.Status {
	case "success":
		where.WriteString(" AND request_logs.status_code BETWEEN 200 AND 299")
	case "error":
		where.WriteString(" AND request_logs.status_code >= 400")
	}
	if query.Search != "" {
		args = append(args, query.Search)
		placeholder := len(args)
		where.WriteString(fmt.Sprintf(` AND (COALESCE(request_logs.virtual_key_id, '') ILIKE '%%' || $%d || '%%'
			OR COALESCE(request_logs.user_id, '') ILIKE '%%' || $%d || '%%'
			OR COALESCE(users.name, '') ILIKE '%%' || $%d || '%%'
			OR request_logs.session_id ILIKE '%%' || $%d || '%%'
			OR request_logs.client_request_id ILIKE '%%' || $%d || '%%'
			OR COALESCE(models.name, request_logs.model_id, '') ILIKE '%%' || $%d || '%%'
			OR request_logs.request_path ILIKE '%%' || $%d || '%%'
			OR COALESCE(request_logs.client_app, '') ILIKE '%%' || $%d || '%%'
			OR COALESCE(request_logs.error_message, '') ILIKE '%%' || $%d || '%%')`,
			placeholder, placeholder, placeholder, placeholder, placeholder, placeholder, placeholder, placeholder, placeholder))
	}
	return where.String(), args
}

func (db *DB) GetRequestUsageSummary(userID string, since time.Time) (RequestUsageSummary, error) {
	var summary RequestUsageSummary
	var cost money.NanoUSD
	err := db.conn.QueryRow(`
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status_code >= 200 AND status_code < 300),
		       COUNT(*) FILTER (WHERE status_code < 200 OR status_code >= 300),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(cache_write_tokens), 0),
		       COALESCE(SUM(cache_miss_tokens), 0),
		       COALESCE(SUM(cost_nano_usd), 0)
		  FROM request_logs
		 WHERE user_id = $1
		   AND ($2::timestamptz IS NULL OR created_at >= $2)`,
		userID, nullableTime(since),
	).Scan(
		&summary.Requests, &summary.Successful, &summary.Failed,
		&summary.InputTokens, &summary.OutputTokens,
		&summary.CacheReadTokens, &summary.CacheWriteTokens,
		&summary.CacheMissTokens, &cost,
	)
	if err != nil {
		return RequestUsageSummary{}, err
	}
	summary.CostNanoUSD = cost
	summary.CostUSD = cost.USD()
	summary.CreditsConsumed = cost.Credits()
	return summary, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func (db *DB) GetUserSpendingInWindow(userID string, durationSeconds int) (float64, error) {
	exact, err := db.GetUserSpendingNanoInWindow(userID, durationSeconds)
	return exact.USD(), err
}

func (db *DB) GetUserSpendingNanoInWindow(userID string, durationSeconds int) (money.NanoUSD, error) {
	var planAssignedAt time.Time
	var usageResetAt sql.NullTime
	err := db.conn.QueryRow("SELECT plan_assigned_at, usage_reset_at FROM users WHERE id = $1", userID).Scan(&planAssignedAt, &usageResetAt)
	if err != nil {
		return 0, err
	}
	floor := effectiveFloor(windowPeriodStart(planAssignedAt, durationSeconds, time.Now()), usageResetAt)
	var total int64
	err = db.conn.QueryRow("SELECT COALESCE(SUM(cost_nano_usd), 0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300", userID, floor).Scan(&total)
	return money.NanoUSD(total), err
}

func (db *DB) GetRemainingExtraCredits(userID string) (float64, error) {
	exact, err := db.GetRemainingExtraNanoUSD(userID)
	return exact.Credits(), err
}

func (db *DB) GetRemainingExtraNanoUSD(userID string) (money.NanoUSD, error) {
	var remaining int64
	err := db.conn.QueryRow("SELECT COALESCE(SUM(amount_nano_usd - used_nano_usd), 0) FROM user_topups WHERE user_id = $1"+activeTopupFilter, userID).Scan(&remaining)
	return money.NanoUSD(remaining), err
}

// GetUserSpendingToday sums a user's cost over the current UTC calendar day
// (003 T005, usage-api.md §2). It uses the same 2xx-only status filter as
// GetUserSpendingInWindow/GetUserBudgetUsage so the "today" figure sums the
// identical row set as the budget windows' current_spent.
func (db *DB) GetUserSpendingToday(userID string) (float64, error) {
	var total int64
	err := db.conn.QueryRow(
		"SELECT COALESCE(SUM(cost_nano_usd), 0) FROM request_logs WHERE user_id = $1 AND status_code >= 200 AND status_code < 300 AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC')",
		userID,
	).Scan(&total)
	return money.NanoUSD(total).USD(), err
}

// ListUserTopups returns a user's top-ups for the ADMIN view, newest first. Unlike
// the user-facing sums it does NOT hide expired/deleted rows — admins keep full
// visibility (badged in the UI); only the money paths apply activeTopupFilter.
func (db *DB) ListUserTopups(userID string) ([]UserTopup, error) {
	rows, err := db.conn.Query("SELECT id, user_id, credits, used_credits, amount_nano_usd, used_nano_usd, created_at, expires_at, deleted_at FROM user_topups WHERE user_id = $1 ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []UserTopup{}
	for rows.Next() {
		var u UserTopup
		var expiresAt, deletedAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.UserID, &u.Credits, &u.UsedCredits, &u.AmountNanoUSD, &u.UsedNanoUSD, &u.CreatedAt, &expiresAt, &deletedAt); err != nil {
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// CreateUserTopup inserts a top-up. When IdemKey is set, a second insert with the
// same key is silently ignored (migration 013 partial unique index) so retries and
// gift-card redemptions can never double-credit. A NULL IdemKey never conflicts.
func (db *DB) CreateUserTopup(t UserTopup) error {
	if err := normalizeTopupMoney(&t); err != nil {
		return err
	}
	_, err := db.conn.Exec(
		"INSERT INTO user_topups (id, user_id, credits, used_credits, amount_nano_usd, used_nano_usd, created_at, expires_at, idem_key) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (idem_key) WHERE idem_key IS NOT NULL DO NOTHING",
		t.ID, t.UserID, t.Credits, t.UsedCredits, t.AmountNanoUSD, t.UsedNanoUSD, t.CreatedAt, t.ExpiresAt, t.IdemKey)
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

		var spentNano int64
		err := db.conn.QueryRow("SELECT COALESCE(SUM(cost_nano_usd), 0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300", userID, floor).Scan(&spentNano)
		if err != nil {
			return nil, err
		}

		usage = append(usage, UserBudgetUsage{
			WindowID:            w.ID,
			Name:                w.Name,
			DurationSeconds:     w.DurationSeconds,
			BudgetUSD:           w.BudgetUSD,
			BudgetNanoUSD:       w.BudgetNanoUSD,
			CurrentSpent:        money.NanoUSD(spentNano).USD(),
			CurrentSpentNanoUSD: money.NanoUSD(spentNano),
			ResetTime:           &resetTime,
		})
	}

	return usage, nil
}

func (db *DB) GetDashboardStats() (*DashboardStats, error) {
	var stats DashboardStats

	err := db.conn.QueryRow(`
		SELECT 
			COUNT(*), 
			COALESCE(SUM(cost_nano_usd), 0),
			COALESCE(SUM(input_tokens + output_tokens), 0),
			COALESCE(AVG(latency_ms), 0.0),
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN 1 ELSE 0 END) * 100.0 / NULLIF(COUNT(*), 0), 100.0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(cache_read_tokens) * 100.0 / NULLIF(SUM(CASE WHEN cache_miss_tokens IS NOT NULL THEN cache_read_tokens + cache_miss_tokens ELSE input_tokens END), 0), 0.0)
		FROM request_logs`).Scan(&stats.TotalRequests, &stats.TotalCostNanoUSD, &stats.TotalTokens, &stats.AvgLatency, &stats.SuccessRate, &stats.CacheReadTokens, &stats.CacheWriteTokens, &stats.CacheHitRate)
	if err != nil {
		return nil, err
	}
	stats.TotalCost = stats.TotalCostNanoUSD.USD()

	daysLimit := time.Now().Add(-7 * 24 * time.Hour)
	rows, err := db.conn.Query(`
		SELECT 
			to_char(created_at, 'YYYY-MM-DD') as day, 
			COUNT(*), 
			SUM(input_tokens + output_tokens), 
			SUM(cost_nano_usd)
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
		var cost sql.NullInt64
		if err := rows.Scan(&ds.Date, &ds.Requests, &tokens, &cost); err != nil {
			return nil, err
		}
		ds.Tokens = int(tokens.Int64)
		ds.CostNanoUSD = money.NanoUSD(cost.Int64)
		ds.Cost = ds.CostNanoUSD.USD()
		stats.DailyStats = append(stats.DailyStats, ds)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	if err := modelRows.Err(); err != nil {
		return nil, err
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
