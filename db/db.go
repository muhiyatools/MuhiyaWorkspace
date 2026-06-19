package db

import (
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
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Email          string            `json:"email"`
	PlanID         string            `json:"plan_id"`
	Status         string            `json:"status"` // active, suspended
	CreatedAt      time.Time         `json:"created_at"`
	PlanAssignedAt time.Time         `json:"plan_assigned_at"`
	BudgetUsage    []UserBudgetUsage `json:"budget_usage,omitempty"`
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
	CreatedAt        time.Time `json:"created_at"`
}

type SystemSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
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

	// Test the database connection
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to ping postgres database: %w", err)
	}

	// Create tables matching the new relational user-budget schema
	schemas := []string{
		`CREATE TABLE IF NOT EXISTS plans (
			id VARCHAR(100) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			rpm_limit INTEGER NOT NULL DEFAULT 0,
			tpm_limit INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS budget_windows (
			id VARCHAR(100) PRIMARY KEY,
			plan_id VARCHAR(100) NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
			name VARCHAR(255) NOT NULL,
			duration_seconds INTEGER NOT NULL,
			budget_usd DOUBLE PRECISION NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS users (
			id VARCHAR(100) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) NOT NULL UNIQUE,
			plan_id VARCHAR(100) NOT NULL REFERENCES plans(id),
			status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'suspended')),
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
			plan_assigned_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS virtual_keys (
			id VARCHAR(100) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'revoked')),
			expires_at TIMESTAMP WITH TIME ZONE,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS providers (
			id VARCHAR(100) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			api_key TEXT NOT NULL,
			base_url TEXT NOT NULL,
			anthropic_base_url TEXT NOT NULL DEFAULT '',
			status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'inactive')),
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS models (
			id VARCHAR(100) PRIMARY KEY,
			name VARCHAR(255) NOT NULL UNIQUE,
			provider_id VARCHAR(100) NOT NULL REFERENCES providers(id),
			target_model VARCHAR(255) NOT NULL,
			input_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			output_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			cache_read_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			cache_write_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			status VARCHAR(50) NOT NULL CHECK (status IN ('active', 'inactive')),
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS request_logs (
			id VARCHAR(100) PRIMARY KEY,
			virtual_key_id VARCHAR(100) NOT NULL REFERENCES virtual_keys(id) ON DELETE CASCADE,
			user_id VARCHAR(100) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			model_id VARCHAR(100) REFERENCES models(id),
			provider_id VARCHAR(100) REFERENCES providers(id),
			request_path VARCHAR(255) NOT NULL,
			status_code INTEGER NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			cost DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			latency_ms INTEGER NOT NULL,
			error_message TEXT,
			client_app VARCHAR(255),
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS system_settings (
			key VARCHAR(255) PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}

	for _, schema := range schemas {
		if _, err := conn.Exec(schema); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to execute schema: %w", err)
		}
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

func (db *DB) seedDefaults() error {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM plans WHERE id = 'free'").Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil // database already seeded with free plan
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear old plans/budgets if any to avoid dirty states
	_, _ = tx.Exec("DELETE FROM budget_windows")
	_, _ = tx.Exec("DELETE FROM virtual_keys")
	_, _ = tx.Exec("DELETE FROM users")
	_, _ = tx.Exec("DELETE FROM plans")

	// Seed system settings
	_, _ = tx.Exec("INSERT INTO system_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", "gateway_name", "MuhiyaLLM Gateway")
	_, _ = tx.Exec("INSERT INTO system_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", "theme_accent", "emerald")

	// Seed plans
	plans := []Plan{
		{ID: "free", Name: "MuhiyaCode Free", RPMLimit: 10, TPMLimit: 1000},
		{ID: "yalla", Name: "Yalla Monthly", RPMLimit: 60, TPMLimit: 10000},
		{ID: "max", Name: "Max Monthly", RPMLimit: 300, TPMLimit: 50000},
		{ID: "yalla-annual", Name: "Yalla Annual", RPMLimit: 60, TPMLimit: 10000},
		{ID: "max-annual", Name: "Max Annual", RPMLimit: 300, TPMLimit: 50000},
	}
	for _, p := range plans {
		_, err := tx.Exec("INSERT INTO plans (id, name, rpm_limit, tpm_limit) VALUES ($1, $2, $3, $4)", p.ID, p.Name, p.RPMLimit, p.TPMLimit)
		if err != nil {
			return err
		}
	}

	// Seed budget windows for plans
	budgetWindows := []BudgetWindow{
		// Free: $0.10 rolling 5-hour burst, $1.00 rolling 31-day Money Ceiling
		{ID: "budget-free-5h", PlanID: "free", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 0.10},
		{ID: "budget-free-31d", PlanID: "free", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 1.00},

		// Yalla: $2.00 rolling 5-hour burst, $10.00 rolling 31-day Money Ceiling
		{ID: "budget-yalla-5h", PlanID: "yalla", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 2.00},
		{ID: "budget-yalla-31d", PlanID: "yalla", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 10.00},

		// Max: $10.00 rolling 5-hour burst, $50.00 rolling 31-day Money Ceiling
		{ID: "budget-max-5h", PlanID: "max", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 10.00},
		{ID: "budget-max-31d", PlanID: "max", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 50.00},

		// Yalla Annual: $2.00 rolling 5-hour burst, $10.00 rolling 31-day Money Ceiling
		{ID: "budget-yalla-annual-5h", PlanID: "yalla-annual", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 2.00},
		{ID: "budget-yalla-annual-31d", PlanID: "yalla-annual", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 10.00},

		// Max Annual: $10.00 rolling 5-hour burst, $50.00 rolling 31-day Money Ceiling
		{ID: "budget-max-annual-5h", PlanID: "max-annual", Name: "Short Term (5 Hours)", DurationSeconds: 18000, BudgetUSD: 10.00},
		{ID: "budget-max-annual-31d", PlanID: "max-annual", Name: "Monthly Budget (31 Days)", DurationSeconds: 2678400, BudgetUSD: 50.00},
	}
	for _, bw := range budgetWindows {
		_, err := tx.Exec("INSERT INTO budget_windows (id, plan_id, name, duration_seconds, budget_usd) VALUES ($1, $2, $3, $4, $5)", bw.ID, bw.PlanID, bw.Name, bw.DurationSeconds, bw.BudgetUSD)
		if err != nil {
			return err
		}
	}

	// Seed Users
	users := []User{
		{ID: "user-dev", Name: "Demo Developer", Email: "dev@muhiyallm.local", PlanID: "free", Status: "active"},
		{ID: "user-prod", Name: "Corporate Client A", Email: "enterprise@client.com", PlanID: "max", Status: "active"},
	}
	for _, u := range users {
		_, err := tx.Exec("INSERT INTO users (id, name, email, plan_id, status) VALUES ($1, $2, $3, $4, $5)", u.ID, u.Name, u.Email, u.PlanID, u.Status)
		if err != nil {
			return err
		}
	}

	// Seed virtual keys
	_, err = tx.Exec("INSERT INTO virtual_keys (id, name, user_id, status) VALUES ($1, $2, $3, $4)", "sk-virt-devkey", "Default Dev Key", "user-dev", "active")
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO virtual_keys (id, name, user_id, status) VALUES ($1, $2, $3, $4)", "sk-virt-prodkey", "Enterprise Production Key", "user-prod", "active")
	if err != nil {
		return err
	}

	// Seed providers
	providers := []Provider{
		{ID: "openai", Name: "OpenAI", APIKey: "mock-openai-key", BaseURL: "https://api.openai.com/v1", AnthropicBaseURL: "", Status: "active"},
		{ID: "anthropic", Name: "Anthropic", APIKey: "mock-anthropic-key", BaseURL: "", AnthropicBaseURL: "https://api.anthropic.com", Status: "active"},
		{ID: "deepseek", Name: "DeepSeek", APIKey: "mock-deepseek-key", BaseURL: "https://api.deepseek.com", AnthropicBaseURL: "https://api.deepseek.com/anthropic", Status: "active"},
	}
	for _, pr := range providers {
		_, err := tx.Exec("INSERT INTO providers (id, name, api_key, base_url, anthropic_base_url, status) VALUES ($1, $2, $3, $4, $5, $6)", pr.ID, pr.Name, pr.APIKey, pr.BaseURL, pr.AnthropicBaseURL, pr.Status)
		if err != nil {
			return err
		}
	}

	// Seed models (with cache pricing details)
	models := []Model{
		{ID: "model-gpt4o", Name: "gpt-4o", ProviderID: "openai", TargetModel: "gpt-4o", InputCostPerMillion: 2.50, OutputCostPerMillion: 10.00, CacheReadCostPerMillion: 1.25, CacheWriteCostPerMillion: 2.50, Status: "active"},
		{ID: "model-claude", Name: "claude-3-5-sonnet", ProviderID: "anthropic", TargetModel: "claude-3-5-sonnet-20241022", InputCostPerMillion: 3.00, OutputCostPerMillion: 15.00, CacheReadCostPerMillion: 0.30, CacheWriteCostPerMillion: 3.75, Status: "active"},
		{ID: "model-deepseek", Name: "deepseek-chat", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "active"},
		// Aliases
		{ID: "model-claude-alias", Name: "claude", ProviderID: "anthropic", TargetModel: "claude-3-5-sonnet-20241022", InputCostPerMillion: 3.00, OutputCostPerMillion: 15.00, CacheReadCostPerMillion: 0.30, CacheWriteCostPerMillion: 3.75, Status: "active"},
		{ID: "model-openai-alias", Name: "openai", ProviderID: "openai", TargetModel: "gpt-4o", InputCostPerMillion: 2.50, OutputCostPerMillion: 10.00, CacheReadCostPerMillion: 1.25, CacheWriteCostPerMillion: 2.50, Status: "active"},
		{ID: "model-deepseek-alias", Name: "deepseek", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "active"},
		{ID: "model-deepseek-flash", Name: "deepseek-v4-flash", ProviderID: "deepseek", TargetModel: "deepseek-chat", InputCostPerMillion: 0.14, OutputCostPerMillion: 0.28, CacheReadCostPerMillion: 0.07, CacheWriteCostPerMillion: 0.14, Status: "active"},
	}
	for _, m := range models {
		_, err := tx.Exec(`INSERT INTO models (
			id, name, provider_id, target_model, input_cost_per_million, 
			output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
			m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status)
		if err != nil {
			return err
		}
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
	return &u, nil
}

func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.conn.Query("SELECT id, name, email, plan_id, status, created_at, plan_assigned_at FROM users ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.PlanID, &u.Status, &u.CreatedAt, &u.PlanAssignedAt); err != nil {
			return nil, err
		}
		usage, err := db.GetUserBudgetUsage(u.ID, u.PlanID)
		if err == nil {
			u.BudgetUsage = usage
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

	// Load budget windows for this plan
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

	var list []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.RPMLimit, &p.TPMLimit, &p.CreatedAt); err != nil {
			return nil, err
		}

		// Load budget windows for this plan
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

	// Insert budget windows
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

	// Delete existing budget windows
	_, err = tx.Exec("DELETE FROM budget_windows WHERE plan_id = $1", p.ID)
	if err != nil {
		return err
	}

	// Insert new budget windows
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

	var list []BudgetWindow
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

	var list []BudgetWindow
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

	var list []VirtualKey
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

	var list []VirtualKey
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

	var list []Provider
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
	var m Model
	err := db.conn.QueryRow(`SELECT id, name, provider_id, target_model, input_cost_per_million, 
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status, created_at 
		FROM models WHERE name = $1 AND status = 'active'`, name).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.CreatedAt)
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
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status, created_at 
		FROM models WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.CreatedAt)
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
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status, created_at 
		FROM models ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Model
	for rows.Next() {
		var m Model
		err := rows.Scan(&m.ID, &m.Name, &m.ProviderID, &m.TargetModel, &m.InputCostPerMillion, &m.OutputCostPerMillion,
			&m.CacheReadCostPerMillion, &m.CacheWriteCostPerMillion, &m.Status, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (db *DB) CreateModel(m Model) error {
	_, err := db.conn.Exec(`INSERT INTO models (
		id, name, provider_id, target_model, input_cost_per_million, 
		output_cost_per_million, cache_read_cost_per_million, cache_write_cost_per_million, status
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		m.ID, m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion,
		m.OutputCostPerMillion, m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status)
	return err
}

func (db *DB) UpdateModel(m Model) error {
	_, err := db.conn.Exec(`UPDATE models SET name = $1, provider_id = $2, target_model = $3, 
		input_cost_per_million = $4, output_cost_per_million = $5, 
		cache_read_cost_per_million = $6, cache_write_cost_per_million = $7, status = $8 WHERE id = $9`,
		m.Name, m.ProviderID, m.TargetModel, m.InputCostPerMillion, m.OutputCostPerMillion,
		m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion, m.Status, m.ID)
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

	var list []SystemSetting
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
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, latency_ms, error_message, created_at, client_app
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		log.ID, log.VirtualKeyID, log.UserID, modelID, providerID, log.RequestPath, log.StatusCode,
		log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.CacheWriteTokens, log.Cost, log.LatencyMS, log.ErrorMessage, log.CreatedAt, log.ClientApp)
	return err
}

func (db *DB) ListRequestLogs(limit int, offset int, userID string, keyID string) ([]RequestLog, error) {
	query := `
		SELECT request_logs.id, request_logs.virtual_key_id, request_logs.user_id, COALESCE(models.name, request_logs.model_id, ''), COALESCE(request_logs.provider_id, ''), request_logs.request_path, request_logs.status_code, 
		       request_logs.input_tokens, request_logs.output_tokens, request_logs.cache_read_tokens, request_logs.cache_write_tokens, request_logs.cost, request_logs.latency_ms, COALESCE(request_logs.error_message, ''), request_logs.created_at, COALESCE(request_logs.client_app, '') 
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

	var list []RequestLog
	for rows.Next() {
		var r RequestLog
		err := rows.Scan(&r.ID, &r.VirtualKeyID, &r.UserID, &r.ModelID, &r.ProviderID, &r.RequestPath, &r.StatusCode,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.Cost, &r.LatencyMS, &r.ErrorMessage, &r.CreatedAt, &r.ClientApp)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, nil
}

// Get spending of a user inside a fixed duration window starting from plan assignment
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
