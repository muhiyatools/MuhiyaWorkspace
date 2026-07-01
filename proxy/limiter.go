package proxy

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gateway/db"
	"github.com/redis/go-redis/v9"
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
	mu          sync.RWMutex
	limiters    map[string]*KeyLimiter
	db          *db.DB
	redisClient *redis.Client
	useRedis    bool
}

func NewRateLimiter(database *db.DB) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*KeyLimiter),
		db:       database,
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err == nil {
			rl.redisClient = redis.NewClient(opt)
			// Ping Redis to verify connection
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := rl.redisClient.Ping(ctx).Err(); err == nil {
				rl.useRedis = true
				log.Println("[LIMITER] Connected to Redis for distributed rate limiting.")
			} else {
				log.Printf("[LIMITER-WARNING] Failed to ping Redis: %v. Falling back to in-memory.", err)
			}
		} else {
			log.Printf("[LIMITER-WARNING] Failed to parse REDIS_URL %s: %v. Falling back to in-memory.", redisURL, err)
		}
	}

	return rl
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

	budgetExceeded := false
	var limitErr error
	for _, w := range windows {
		if w.BudgetUSD > 0 {
			spending, err := rl.db.GetUserSpendingInWindow(user.ID, w.DurationSeconds)
			if err != nil {
				return fmt.Errorf("failed to calculate window spending: %w", err)
			}
			if spending >= w.BudgetUSD {
				budgetExceeded = true
				limitErr = fmt.Errorf("budget limit of $%.2f exceeded for window '%s' (current spending: $%.4f)", w.BudgetUSD, w.Name, spending)
				break
			}
		}
	}

	if budgetExceeded {
		remainingCredits, err := rl.db.GetRemainingExtraCredits(user.ID)
		if err != nil {
			return fmt.Errorf("failed to check extra credits: %w", err)
		}
		if remainingCredits <= 0 {
			return limitErr
		}
	}

	if rl.useRedis {
		return rl.checkRedisLimits(key.ID, plan.RPMLimit, plan.TPMLimit, promptTokens)
	}

	return rl.checkInMemoryLimits(key.ID, plan.RPMLimit, plan.TPMLimit, promptTokens)
}

func (rl *RateLimiter) checkRedisLimits(keyID string, rpmLimit, tpmLimit, promptTokens int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	now := time.Now()
	nowMs := now.UnixMilli()
	oneMinAgoMs := nowMs - 60000

	rpmKey := fmt.Sprintf("ratelimit:rpm:%s", keyID)
	tpmKey := fmt.Sprintf("ratelimit:tpm:%s", keyID)

	pipe := rl.redisClient.Pipeline()

	// RPM check: prune old requests, add current request, get count
	pipe.ZRemRangeByScore(ctx, rpmKey, "0", strconv.FormatInt(oneMinAgoMs, 10))
	pipe.ZCard(ctx, rpmKey)

	// TPM check: prune old tokens, get all current tokens
	pipe.ZRemRangeByScore(ctx, tpmKey, "0", strconv.FormatInt(oneMinAgoMs, 10))
	pipe.ZRangeWithScores(ctx, tpmKey, 0, -1)

	cmds, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("redis operation failed: %w", err)
	}

	// 1. RPM check
	rpmCount, _ := cmds[1].(*redis.IntCmd).Result()
	if rpmLimit > 0 && int(rpmCount) >= rpmLimit {
		return fmt.Errorf("requests per minute (RPM) limit of %d exceeded", rpmLimit)
	}

	// 2. TPM check
	tpmRanges, _ := cmds[3].(*redis.ZSliceCmd).Result()
	currentTPM := 0
	for _, z := range tpmRanges {
		memberStr, ok := z.Member.(string)
		if ok {
			parts := strings.Split(memberStr, ":")
			if len(parts) >= 2 {
				if tokens, err := strconv.Atoi(parts[1]); err == nil {
					currentTPM += tokens
				}
			}
		}
	}

	if tpmLimit > 0 && currentTPM+promptTokens > tpmLimit {
		return fmt.Errorf("tokens per minute (TPM) limit of %d exceeded (current sliding TPM: %d, requested: %d)", tpmLimit, currentTPM, promptTokens)
	}

	// 3. Commit new request and token records to Redis
	pipe2 := rl.redisClient.Pipeline()
	pipe2.ZAdd(ctx, rpmKey, redis.Z{
		Score:  float64(nowMs),
		Member: fmt.Sprintf("%d", now.UnixNano()),
	})
	pipe2.Expire(ctx, rpmKey, 75*time.Second)

	if promptTokens > 0 {
		pipe2.ZAdd(ctx, tpmKey, redis.Z{
			Score:  float64(nowMs),
			Member: fmt.Sprintf("%d:%d", now.UnixNano(), promptTokens),
		})
		pipe2.Expire(ctx, tpmKey, 75*time.Second)
	}

	_, err = pipe2.Exec(ctx)
	if err != nil {
		log.Printf("[LIMITER-WARNING] Failed to commit limits to Redis: %v", err)
	}

	return nil
}

func (rl *RateLimiter) checkInMemoryLimits(keyID string, rpmLimit, tpmLimit, promptTokens int) error {
	kl := rl.getLimiter(keyID)
	kl.mu.Lock()
	defer kl.mu.Unlock()

	now := time.Now()
	oneMinAgo := now.Add(-60 * time.Second)

	// Prune requests older than 1 minute
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

	// Prune tokens older than 1 minute
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
	if rpmLimit > 0 && len(kl.requests) >= rpmLimit {
		return fmt.Errorf("requests per minute (RPM) limit of %d exceeded", rpmLimit)
	}

	// TPM check
	currentTPM := 0
	for _, t := range kl.tokens {
		currentTPM += t.tokens
	}
	if tpmLimit > 0 && currentTPM+promptTokens > tpmLimit {
		return fmt.Errorf("tokens per minute (TPM) limit of %d exceeded (current sliding TPM: %d, requested: %d)", tpmLimit, currentTPM, promptTokens)
	}

	// Record request and tokens
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

	if rl.useRedis {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tpmKey := fmt.Sprintf("ratelimit:tpm:%s", keyID)
		now := time.Now()
		rl.redisClient.ZAdd(ctx, tpmKey, redis.Z{
			Score:  float64(now.UnixMilli()),
			Member: fmt.Sprintf("%d:%d", now.UnixNano(), tokens),
		})
		rl.redisClient.Expire(ctx, tpmKey, 75*time.Second)
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
