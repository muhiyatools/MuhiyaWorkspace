package proxy

import (
	"fmt"
	"sync"
	"time"

	"gateway/db"
)

type reqRecord struct {
	timestamp time.Time
}

type tokenRecord struct {
	timestamp time.Time
	tokens    int
}

type KeyLimiter struct {
	mu       sync.Mutex
	requests []reqRecord
	tokens   []tokenRecord
}

type RateLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*KeyLimiter
	db       *db.DB
}

func NewRateLimiter(database *db.DB) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*KeyLimiter),
		db:       database,
	}
}

func (rl *RateLimiter) getLimiter(keyID string) *KeyLimiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	kl, exists := rl.limiters[keyID]
	if !exists {
		kl = &KeyLimiter{
			requests: make([]reqRecord, 0),
			tokens:   make([]tokenRecord, 0),
		}
		rl.limiters[keyID] = kl
	}
	return kl
}

func (rl *RateLimiter) CheckLimit(key *db.VirtualKey, promptTokens int) error {
	// 1. Fetch User details
	user, err := rl.db.GetUser(key.UserID)
	if err != nil {
		return fmt.Errorf("failed to fetch key user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("owner user not found")
	}
	if user.Status != "active" {
		return fmt.Errorf("user account is suspended")
	}

	// 2. Fetch User's Plan
	plan, err := rl.db.GetPlan(user.PlanID)
	if err != nil {
		return fmt.Errorf("failed to fetch user plan: %w", err)
	}
	if plan == nil {
		return fmt.Errorf("user plan not found")
	}

	// 3. Enforce sliding budget windows
	windows, err := rl.db.ListBudgetWindowsByPlan(plan.ID)
	if err != nil {
		return fmt.Errorf("failed to list budget windows: %w", err)
	}

	for _, w := range windows {
		if w.BudgetUSD > 0 {
			spending, err := rl.db.GetUserSpendingInWindow(user.ID, w.DurationSeconds)
			if err != nil {
				return fmt.Errorf("failed to calculate window spending: %w", err)
			}
			if spending >= w.BudgetUSD {
				return fmt.Errorf("budget limit of $%.2f exceeded for window '%s' (current spending: $%.4f)", w.BudgetUSD, w.Name, spending)
			}
		}
	}

	// 4. In-Memory RPM & TPM checks (Still tracked at key level)
	kl := rl.getLimiter(key.ID)
	kl.mu.Lock()
	defer kl.mu.Unlock()

	now := time.Now()
	oneMinAgo := now.Add(-60 * time.Second)

	// Prune request records older than 1 minute
	reqIdx := 0
	for i, r := range kl.requests {
		if r.timestamp.After(oneMinAgo) {
			reqIdx = i
			break
		}
		if i == len(kl.requests)-1 {
			reqIdx = len(kl.requests)
		}
	}
	kl.requests = kl.requests[reqIdx:]

	// Prune token records older than 1 minute
	tokIdx := 0
	for i, t := range kl.tokens {
		if t.timestamp.After(oneMinAgo) {
			tokIdx = i
			break
		}
		if i == len(kl.tokens)-1 {
			tokIdx = len(kl.tokens)
		}
	}
	kl.tokens = kl.tokens[tokIdx:]

	// RPM check
	if plan.RPMLimit > 0 && len(kl.requests) >= plan.RPMLimit {
		return fmt.Errorf("requests per minute (RPM) limit of %d exceeded", plan.RPMLimit)
	}

	// TPM check
	currentTPM := 0
	for _, t := range kl.tokens {
		currentTPM += t.tokens
	}
	if plan.TPMLimit > 0 && currentTPM+promptTokens > plan.TPMLimit {
		return fmt.Errorf("tokens per minute (TPM) limit of %d exceeded (current sliding TPM: %d, requested: %d)", plan.TPMLimit, currentTPM, promptTokens)
	}

	// Admission accepted
	kl.requests = append(kl.requests, reqRecord{timestamp: now})
	if promptTokens > 0 {
		kl.tokens = append(kl.tokens, tokenRecord{timestamp: now, tokens: promptTokens})
	}

	return nil
}

func (rl *RateLimiter) RecordTokens(keyID string, tokens int) {
	if tokens <= 0 {
		return
	}
	kl := rl.getLimiter(keyID)
	kl.mu.Lock()
	defer kl.mu.Unlock()

	kl.tokens = append(kl.tokens, tokenRecord{
		timestamp: time.Now(),
		tokens:    tokens,
	})
}
