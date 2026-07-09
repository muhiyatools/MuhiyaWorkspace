package db

import (
	"context"
	"database/sql"
	"fmt"
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
	Status                   string    `json:"status"` // active, inactive
	RoutingTier              string    `json:"routing_tier"` // none, simple, medium, hard
	ModelType                string    `json:"model_type"` // llm, transcript
	PricePerMinute           float64   `json:"price_per_minute"`
	Transcribe               bool      `json:"transcribe"`
	ContextWindow            int       `json:"context_window"`
	MaxOutputTokens          int       `json:"max_output_tokens"`
	DisplayName              string    `json:"display_name"`
	Description              string    `json:"description"`
	OwnedBy                  string    `json:"owned_by"`
	CreatedAt                time.Time `json:"created_at"`
}

type RequestLog struct {
	ID               string    `json:"id"`
	VirtualKeyID     string    `json:"virtual_key_id"`
	UserID           string    `json:"user_id"`
	ModelID          string    `json:"model_id"`
	ProviderID       string    `json:"provider_id"`
	RequestPath      string    `json:"request_path"`
	StatusCode       int       `json:"status_code"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	CacheReadTokens  int       `json:"cache_read_tokens"`
	CacheWriteTokens int       `json:"cache_write_tokens"`
	Cost             float64   `json:"cost"`
	LatencyMS        int       `json:"latency_ms"`
	ErrorMessage     string    `json:"error_message"`
	ClientApp        string    `json:"client_app"`
	RequestedModel   string    `json:"requested_model"`
	Complexity       string    `json:"complexity"`
	ThinkingLevel    string    `json:"thinking_level"`
	FailoverAttempts int       `json:"failover_attempts"`
	CreatedAt        time.Time `json:"created_at"`
}

type SystemSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type UserTopup struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Credits     float64   `json:"credits"`
	UsedCredits float64   `json:"used_credits"`
	CreatedAt   time.Time `json:"created_at"`
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
	if err := db.seedDefaults(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to seed defaults: %w", err)
	}

	return db, nil
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
			pr.ID, pr.Name, pr.APIKey, pr.BaseURL, pr.AnthropicBaseURL, pr.Status)
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
	err = db.conn.QueryRow("SELECT COALESCE(SUM(credits), 0.0), COALESCE(SUM(credits - used_credits), 0.0) FROM user_topups WHERE user_id = $1", u.ID).Scan(&extra, &remaining)
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
		err = db.conn.QueryRow("SELECT COALESCE(SUM(credits), 0.0), COALESCE(SUM(credits - used_credits), 0.0) FROM user_topups WHERE user_id = $1", u.ID).Scan(&extra, &remaining)
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

func (db *DB) GetVirtualKey(id string) (*VirtualKey, error) {
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

func (db *DB) CreateVirtualKey(vk VirtualKey) error {
	_, err := db.conn.Exec("INSERT INTO virtual_keys (id, name, user_id, status, expires_at) VALUES ($1, $2, $3, $4, $5)", vk.ID, vk.Name, vk.UserID, vk.Status, vk.ExpiresAt)
	return err
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
		list = append(list, p)
	}
	return list, nil
}

func (db *DB) CreateProvider(p Provider) error {
	_, err := db.conn.Exec("INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status) VALUES ($1, $2, $3, $4, $5, $6)", p.ID, p.Name, p.APIKey, p.BaseURL, p.AnthropicBaseURL, p.Status)
	return err
}

func (db *DB) UpdateProvider(p Provider) error {
	_, err := db.conn.Exec("UPDATE providers SET name = $1, api_key = $2, base_url = $3, anthropic_base_url = $4, status = $5, updated_at = CURRENT_TIMESTAMP WHERE id = $6", p.Name, p.APIKey, p.BaseURL, p.AnthropicBaseURL, p.Status, p.ID)
	return err
}

func (db *DB) DeleteProvider(id string) error {
	_, err := db.conn.Exec("DELETE FROM providers WHERE id = $1", id)
	return err
}

// --- Models CRUD ---

func (db *DB) GetModelByName(name string) (*Model, error) {
	normalizedName := name
	if name == "claude" {
		normalizedName = "claude-3-5-sonnet"
	} else if name == "openai" {
		normalizedName = "gpt-4o"
	} else if name == "deepseek" {
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
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, '')
		FROM models
		WHERE status = 'active' AND (name = $1 OR lower(display_name) = lower($1))
		ORDER BY (name = $1) DESC
		LIMIT 1`, normalizedName).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.RoutingTier, &m.ModelType,
			&m.PricePerMinute, &m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy)
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
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, '') 
		FROM models WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.RoutingTier, &m.ModelType, 
			&m.PricePerMinute, &m.Transcribe, &m.CreatedAt, &m.ContextWindow, &m.MaxOutputTokens,
			&m.DisplayName, &m.Description, &m.OwnedBy)
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
		COALESCE(display_name, ''), COALESCE(description, ''), COALESCE(owned_by, '') 
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
			&m.DisplayName, &m.Description, &m.OwnedBy)
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
		display_name, description, owned_by
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
		m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status, 
		m.RoutingTier, m.ModelType, m.PricePerMinute, m.Transcribe, m.ContextWindow, m.MaxOutputTokens,
		m.DisplayName, m.Description, m.OwnedBy)
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
		display_name = $15, description = $16, owned_by = $17 WHERE id = $18`,
		m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion, m.OutputCostPerMillion,
		m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status, m.RoutingTier, m.ModelType, 
		m.PricePerMinute, m.Transcribe, m.ContextWindow, m.MaxOutputTokens, m.DisplayName, m.Description, 
		m.OwnedBy, m.ID)
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

// --- Logging & Budget Queries ---

func (db *DB) InsertRequestLog(log RequestLog) error {
	var modelID, providerID interface{}
	if log.ModelID != "" {
		modelID = log.ModelID
	}
	if log.ProviderID != "" {
		providerID = log.ProviderID
	}

	_, err := db.conn.Exec(`INSERT INTO request_logs (
		id, virtual_key_id, user_id, model_id, provider_id, request_path, status_code,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, latency_ms, error_message, created_at, client_app,
		requested_model, complexity, failover_attempts, thinking_level
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		log.ID, log.VirtualKeyID, log.UserID, modelID, providerID, log.RequestPath, log.StatusCode,
		log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.CacheWriteTokens, log.Cost, log.LatencyMS, log.ErrorMessage, log.CreatedAt, log.ClientApp,
		log.RequestedModel, log.Complexity, log.FailoverAttempts, log.ThinkingLevel)

	if err == nil && log.StatusCode >= 200 && log.StatusCode < 300 && log.Cost > 0 {
		_ = db.DeductExtraCreditsIfExceeded(log.UserID, log.Cost)
	}
	return err
}

func (db *DB) ListRequestLogs(limit int, offset int, userID string, keyID string) ([]RequestLog, error) {
	// virtual_key_id / user_id are COALESCEd because migration 005 makes them
	// nullable (ON DELETE SET NULL): a log whose user or key was later deleted
	// survives with a null reference and must still scan into a string.
	query := `
		SELECT request_logs.id, COALESCE(request_logs.virtual_key_id, ''), COALESCE(request_logs.user_id, ''), COALESCE(models.name, request_logs.model_id, ''), COALESCE(request_logs.provider_id, ''), request_logs.request_path, request_logs.status_code,
		       request_logs.input_tokens, request_logs.output_tokens, request_logs.cache_read_tokens, request_logs.cache_write_tokens, request_logs.cost, request_logs.latency_ms, COALESCE(request_logs.error_message, ''), request_logs.created_at, COALESCE(request_logs.client_app, ''),
		       COALESCE(request_logs.requested_model, ''), COALESCE(request_logs.complexity, ''), COALESCE(request_logs.failover_attempts, 0), COALESCE(request_logs.thinking_level, '')
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
			&r.RequestedModel, &r.Complexity, &r.FailoverAttempts, &r.ThinkingLevel)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, nil
}

func (db *DB) GetUserSpendingInWindow(userID string, durationSeconds int) (float64, error) {
	var planAssignedAt time.Time
	err := db.conn.QueryRow("SELECT plan_assigned_at FROM users WHERE id = $1", userID).Scan(&planAssignedAt)
	if err != nil {
		return 0, err
	}

	duration := time.Duration(durationSeconds) * time.Second
	now := time.Now()
	elapsed := now.Sub(planAssignedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	periods := int64(elapsed / duration)
	periodStart := planAssignedAt.Add(time.Duration(periods) * duration)

	var total float64
	err = db.conn.QueryRow("SELECT COALESCE(SUM(cost), 0.0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300", userID, periodStart).Scan(&total)
	return total, err
}

func (db *DB) GetRemainingExtraCredits(userID string) (float64, error) {
	var remaining float64
	err := db.conn.QueryRow("SELECT COALESCE(SUM(credits - used_credits), 0.0) FROM user_topups WHERE user_id = $1", userID).Scan(&remaining)
	return remaining, err
}

func (db *DB) DeductExtraCreditsIfExceeded(userID string, costUSD float64) error {
	if costUSD <= 0 {
		return nil
	}
	var planID string
	err := db.conn.QueryRow("SELECT plan_id FROM users WHERE id = $1", userID).Scan(&planID)
	if err != nil {
		return err
	}
	windows, err := db.ListBudgetWindowsByPlan(planID)
	if err != nil {
		return err
	}
	var maxExceeded float64
	for _, w := range windows {
		if w.BudgetUSD <= 0 {
			continue
		}
		currentSpending, err := db.GetUserSpendingInWindow(userID, w.DurationSeconds)
		if err != nil {
			return err
		}
		previousSpending := currentSpending - costUSD
		limit := w.BudgetUSD

		var prevExceeded float64
		if previousSpending > limit {
			prevExceeded = previousSpending
		} else {
			prevExceeded = limit
		}
		exceeded := currentSpending - prevExceeded
		if exceeded < 0 {
			exceeded = 0
		}
		if exceeded > maxExceeded {
			maxExceeded = exceeded
		}
	}

	if maxExceeded <= 0 {
		return nil
	}

	deductCredits := maxExceeded * 100.0

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query("SELECT id, credits, used_credits FROM user_topups WHERE user_id = $1 AND used_credits < credits ORDER BY created_at ASC FOR UPDATE", userID)
	if err != nil {
		return err
	}

	type topupRow struct {
		id          string
		credits     float64
		usedCredits float64
	}
	var activeTopups []topupRow
	for rows.Next() {
		var r topupRow
		if err := rows.Scan(&r.id, &r.credits, &r.usedCredits); err != nil {
			rows.Close()
			return err
		}
		activeTopups = append(activeTopups, r)
	}
	rows.Close()

	for _, r := range activeTopups {
		if deductCredits <= 0 {
			break
		}
		rem := r.credits - r.usedCredits
		var toAdd float64
		if deductCredits <= rem {
			toAdd = deductCredits
			deductCredits = 0
		} else {
			toAdd = rem
			deductCredits -= rem
		}
		_, err = tx.Exec("UPDATE user_topups SET used_credits = used_credits + $1 WHERE id = $2", toAdd, r.id)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (db *DB) ListUserTopups(userID string) ([]UserTopup, error) {
	rows, err := db.conn.Query("SELECT id, user_id, credits, used_credits, created_at FROM user_topups WHERE user_id = $1 ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []UserTopup{}
	for rows.Next() {
		var u UserTopup
		if err := rows.Scan(&u.ID, &u.UserID, &u.Credits, &u.UsedCredits, &u.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, nil
}

func (db *DB) CreateUserTopup(t UserTopup) error {
	_, err := db.conn.Exec("INSERT INTO user_topups (id, user_id, credits, used_credits, created_at) VALUES ($1, $2, $3, $4, $5)", t.ID, t.UserID, t.Credits, t.UsedCredits, t.CreatedAt)
	return err
}

func (db *DB) GetUserBudgetUsage(userID string, planID string) ([]UserBudgetUsage, error) {
	var planAssignedAt time.Time
	err := db.conn.QueryRow("SELECT plan_assigned_at FROM users WHERE id = $1", userID).Scan(&planAssignedAt)
	if err != nil {
		return nil, err
	}

	windows, err := db.ListBudgetWindowsByPlan(planID)
	if err != nil {
		return nil, err
	}

	var usage []UserBudgetUsage
	for _, w := range windows {
		duration := time.Duration(w.DurationSeconds) * time.Second
		now := time.Now()
		elapsed := now.Sub(planAssignedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		periods := int64(elapsed / duration)
		periodStart := planAssignedAt.Add(time.Duration(periods) * duration)
		resetTime := periodStart.Add(duration)

		var spent float64
		err := db.conn.QueryRow("SELECT COALESCE(SUM(cost), 0.0) FROM request_logs WHERE user_id = $1 AND created_at >= $2 AND status_code >= 200 AND status_code < 300", userID, periodStart).Scan(&spent)
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
			COALESCE(SUM(cache_read_tokens) * 100.0 / NULLIF(SUM(input_tokens), 0), 0.0)
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
