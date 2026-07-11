package proxy

import (
	"context"
	"fmt"
	"log"
	"os"
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

// limiterDefsEntry caches the near-static per-user lookups CheckLimit needs
// on every request: the user record, their plan, and the plan's budget
// window definitions. Spending itself is NEVER cached here - only these
// definitions, which change only when an admin edits a plan/user.
type limiterDefsEntry struct {
	user    *db.User
	plan    *db.Plan
	windows []db.BudgetWindow
	expires time.Time
}

// limiterDefsTTL bounds how stale a cached definition can be. Short enough
// that an admin suspending a user or changing a plan takes effect within
// one request's worth of staleness; long enough to collapse 3-4 serialized
// DB round trips per proxied request down to about one on a cache hit.
const limiterDefsTTL = 5 * time.Second

type limiterDefsCache struct {
	mu      sync.RWMutex
	entries map[string]limiterDefsEntry
}

func (c *limiterDefsCache) get(userID string) (limiterDefsEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[userID]
	if !ok || time.Now().After(e.expires) {
		return limiterDefsEntry{}, false
	}
	return e, true
}

func (c *limiterDefsCache) set(userID string, e limiterDefsEntry) {
	e.expires = time.Now().Add(limiterDefsTTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]limiterDefsEntry)
	}
	c.entries[userID] = e
}

type RateLimiter struct {
	mu          sync.RWMutex
	limiters    map[string]*KeyLimiter
	db          *db.DB
	redisClient *redis.Client
	useRedis    bool
	scriptMu    sync.RWMutex // guards scriptSHA: read on every request, written on the rare NOSCRIPT-retry path
	scriptSHA   string
	defs        *limiterDefsCache
}

func (rl *RateLimiter) getScriptSHA() string {
	rl.scriptMu.RLock()
	defer rl.scriptMu.RUnlock()
	return rl.scriptSHA
}

func (rl *RateLimiter) setScriptSHA(sha string) {
	rl.scriptMu.Lock()
	defer rl.scriptMu.Unlock()
	rl.scriptSHA = sha
}

func NewRateLimiter(database *db.DB) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*KeyLimiter),
		db:       database,
		defs:     &limiterDefsCache{},
	}

	requireRedis := isTruthy(os.Getenv("REQUIRE_REDIS"))
	redisURL := os.Getenv("REDIS_URL")

	if redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err == nil {
			rl.redisClient = redis.NewClient(opt)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			pingErr := rl.redisClient.Ping(ctx).Err()
			cancel()
			if pingErr == nil {
				rl.useRedis = true
				rl.loadScript()
				log.Println("[LIMITER] Connected to Redis for distributed rate limiting.")
			} else if requireRedis {
				log.Fatalf("[LIMITER] REQUIRE_REDIS is set but Redis is unreachable: %v", pingErr)
			} else {
				log.Printf("[LIMITER-WARNING] Failed to ping Redis: %v. Falling back to in-memory (single-instance only - rate limits will not synchronize across replicas).", pingErr)
			}
		} else if requireRedis {
			log.Fatalf("[LIMITER] REQUIRE_REDIS is set but REDIS_URL is invalid: %v", err)
		} else {
			log.Printf("[LIMITER-WARNING] Failed to parse REDIS_URL %s: %v. Falling back to in-memory.", redisURL, err)
		}
	} else if requireRedis {
		log.Fatalf("[LIMITER] REQUIRE_REDIS is set but REDIS_URL is not configured. Set REDIS_URL, or unset REQUIRE_REDIS for a single-instance deployment (in-memory rate limits do not synchronize across replicas).")
	}

	go rl.sweepInMemoryLimiters()

	return rl
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
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

// sweepInMemoryLimiters periodically drops KeyLimiter entries that have seen
// no activity recently, so a long-running single-instance deployment does
// not accumulate one map entry per distinct key forever. Sized generously
// relative to the 1-minute sliding windows the limiter itself tracks.
func (rl *RateLimiter) sweepInMemoryLimiters() {
	const interval = 5 * time.Minute
	const idleAfter = 5 * time.Minute
	for {
		time.Sleep(interval)
		cutoff := time.Now().Add(-idleAfter)
		rl.mu.Lock()
		for id, kl := range rl.limiters {
			kl.mu.Lock()
			var lastActivity time.Time
			if n := len(kl.requests); n > 0 {
				lastActivity = kl.requests[n-1].timestamp
			}
			if n := len(kl.tokens); n > 0 {
				if t := kl.tokens[n-1].timestamp; t.After(lastActivity) {
					lastActivity = t
				}
			}
			kl.mu.Unlock()
			if lastActivity.Before(cutoff) {
				delete(rl.limiters, id)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) CheckLimit(key *db.VirtualKey, promptTokens int) error {
	// 1-3. User, plan, and budget window DEFINITIONS - cached briefly
	// (limiterDefsTTL) since they rarely change; spending itself (below) is
	// always read fresh regardless of this cache.
	var user *db.User
	var plan *db.Plan
	var windows []db.BudgetWindow

	if cached, ok := rl.defs.get(key.UserID); ok {
		user, plan, windows = cached.user, cached.plan, cached.windows
	} else {
		var err error
		user, err = rl.db.GetUser(key.UserID)
		if err != nil {
			return fmt.Errorf("failed to fetch key user: %w", err)
		}
		if user == nil {
			return fmt.Errorf("owner user not found")
		}

		plan, err = rl.db.GetPlan(user.PlanID)
		if err != nil {
			return fmt.Errorf("failed to fetch user plan: %w", err)
		}
		if plan == nil {
			return fmt.Errorf("user plan not found")
		}

		windows, err = rl.db.ListBudgetWindowsByPlan(plan.ID)
		if err != nil {
			return fmt.Errorf("failed to list budget windows: %w", err)
		}

		rl.defs.set(key.UserID, limiterDefsEntry{user: user, plan: plan, windows: windows})
	}

	if user.Status != "active" {
		return fmt.Errorf("user account is suspended")
	}

	// Enforce sliding budget windows - spending is queried fresh every time.
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

// atomicLimitScript performs the entire RPM/TPM check-and-commit as one
// Redis-side atomic operation. The previous implementation ran the read
// (pipeline 1) and the write (pipeline 2) as two separate round trips with a
// window between them in which concurrent requests for the same key could
// all pass the read and all commit, letting a burst exceed the configured
// limit. Redis executes a single Lua script atomically (no other command
// interleaves), which closes that window entirely.
//
// Returns {code, rpm_count, tpm_after}: code 0 = allowed (and committed),
// 1 = RPM exceeded (not committed), 2 = TPM exceeded (not committed).
const atomicLimitScript = `
local rpm_key = KEYS[1]
local tpm_key = KEYS[2]
local now_ms = tonumber(ARGV[1])
local one_min_ago_ms = tonumber(ARGV[2])
local rpm_limit = tonumber(ARGV[3])
local tpm_limit = tonumber(ARGV[4])
local prompt_tokens = tonumber(ARGV[5])
local req_member = ARGV[6]
local tok_member = ARGV[7]
local expire_seconds = tonumber(ARGV[8])

redis.call('ZREMRANGEBYSCORE', rpm_key, '0', one_min_ago_ms)
redis.call('ZREMRANGEBYSCORE', tpm_key, '0', one_min_ago_ms)

local rpm_count = redis.call('ZCARD', rpm_key)
if rpm_limit > 0 and rpm_count >= rpm_limit then
  return {1, rpm_count, 0}
end

local members = redis.call('ZRANGE', tpm_key, 0, -1)
local current_tpm = 0
for _, m in ipairs(members) do
  local sep = string.find(m, ':')
  if sep then
    local tok = tonumber(string.sub(m, sep + 1))
    if tok then current_tpm = current_tpm + tok end
  end
end

if tpm_limit > 0 and (current_tpm + prompt_tokens) > tpm_limit then
  return {2, rpm_count, current_tpm}
end

redis.call('ZADD', rpm_key, now_ms, req_member)
redis.call('EXPIRE', rpm_key, expire_seconds)
if prompt_tokens > 0 then
  redis.call('ZADD', tpm_key, now_ms, tok_member)
  redis.call('EXPIRE', tpm_key, expire_seconds)
end

return {0, rpm_count + 1, current_tpm + prompt_tokens}
`

func (rl *RateLimiter) loadScript() {
	if rl.redisClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sha, err := rl.redisClient.ScriptLoad(ctx, atomicLimitScript).Result()
	if err != nil {
		log.Printf("[LIMITER-WARNING] failed to load atomic limit script (will EVAL inline instead): %v", err)
		return
	}
	rl.setScriptSHA(sha)
}

func (rl *RateLimiter) evalAtomicLimit(ctx context.Context, keys []string, args []interface{}) (interface{}, error) {
	if sha := rl.getScriptSHA(); sha != "" {
		res, err := rl.redisClient.EvalSha(ctx, sha, keys, args...).Result()
		if err == nil {
			return res, nil
		}
		if !strings.Contains(err.Error(), "NOSCRIPT") {
			return nil, err
		}
		// Script cache was flushed server-side (e.g. Redis restart) - fall
		// through to a full EVAL and re-cache the SHA for next time.
	}
	res, err := rl.redisClient.Eval(ctx, atomicLimitScript, keys, args...).Result()
	if err == nil {
		rl.loadScript()
	}
	return res, err
}

func (rl *RateLimiter) checkRedisLimits(keyID string, rpmLimit, tpmLimit, promptTokens int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	now := time.Now()
	nowMs := now.UnixMilli()
	oneMinAgoMs := nowMs - 60000

	rpmKey := fmt.Sprintf("ratelimit:rpm:%s", keyID)
	tpmKey := fmt.Sprintf("ratelimit:tpm:%s", keyID)
	reqMember := fmt.Sprintf("%d", now.UnixNano())
	tokMember := fmt.Sprintf("%d:%d", now.UnixNano(), promptTokens)

	res, err := rl.evalAtomicLimit(ctx, []string{rpmKey, tpmKey}, []interface{}{
		nowMs, oneMinAgoMs, rpmLimit, tpmLimit, promptTokens, reqMember, tokMember, 75,
	})
	if err != nil {
		// A transient Redis failure must not take down all traffic. Fail
		// open to the per-process in-memory limiter instead of erroring.
		log.Printf("[LIMITER-WARNING] Redis unavailable, falling back to in-memory limits: %v", err)
		return rl.checkInMemoryLimits(keyID, rpmLimit, tpmLimit, promptTokens)
	}

	vals, ok := res.([]interface{})
	if !ok || len(vals) < 3 {
		log.Printf("[LIMITER-WARNING] Unexpected Redis script result, falling back to in-memory limits")
		return rl.checkInMemoryLimits(keyID, rpmLimit, tpmLimit, promptTokens)
	}
	code, _ := vals[0].(int64)
	switch code {
	case 1:
		rpmCount, _ := vals[1].(int64)
		_ = rpmCount
		return fmt.Errorf("requests per minute (RPM) limit of %d exceeded", rpmLimit)
	case 2:
		curTPM, _ := vals[2].(int64)
		return fmt.Errorf("tokens per minute (TPM) limit of %d exceeded (current sliding TPM: %d, requested: %d)", tpmLimit, curTPM, promptTokens)
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
