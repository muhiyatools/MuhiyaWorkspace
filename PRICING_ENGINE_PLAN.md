# Advanced Pricing & Usage-Tracking Plan

**Repo:** `F:\MuhiyaWorkspace\MuhiyaWorkspace` (Go gateway proxy)
**Authored:** 2026-07-31
**Status:** plan only — nothing in here has been implemented yet.

---

## 0. What already exists (do not rebuild)

Before proposing anything I read the current pricing path end to end. A lot of
the hard foundation is already correct, and the plan below builds on it rather
than replacing it.

| Capability | Where | State |
|---|---|---|
| Exact integer money (nano-USD, 1 USD = 1e9) | `money/money.go` | **Done.** `big.Int` intermediate, checked overflow, `MulDivCeil` with deliberate ceiling so no billable usage rounds to free. |
| Context-length pricing tiers | `model_pricing_tiers` (migration 025), `pricing.Tier` | **Done.** Data-driven, per-model, tier selected by input-token threshold, each tier carries its own input/output/cache-read/cache-write rate. |
| Separate cache-read vs cache-write vs normal rates | `pricing.Rates` | **Done** (single TTL only — see G2). |
| Immutable price snapshot per request | `pricingRulesForModel` → `sha256` → `pricing:<hash>` | **Partly done.** The ID is computed but **never persisted**, so it proves nothing after the fact (see G4). |
| Admission-time affordability + output clamping | `RuleSet.MaxOutputTokens` binary search, `affordableGeneration` | **Done.** |
| Request-level usage log | `request_logs` (migrations 023, 030) | **Done** for tokens and total cost; no rate/tier/window provenance (G4). |

**The user's four named scenarios, scored against today's code:**

1. **Context-length pricing** (`≤272K $1.25, >272K $2.50`) — **already supported.**
   `model_pricing_tiers` does exactly this, and tiers carry a cache-write rate,
   so the quoted OpenRouter cache-write example is expressible today.
   Only one fix is needed: the tier threshold must compare against *total prompt
   tokens*, which is not currently guaranteed (see G1).
2. **Separate cache create / cache read / normal rates** — **already supported.**
3. **Cache TTL rates** (`Cache Create 5m`, `Cache Read 5m` vs default/1h) — **not
   supported.** One flat cache-read rate and one flat cache-write rate (G2).
4. **Peak / off-peak time-of-day pricing** (DeepSeek 2×) — **not supported at
   all.** There is no time dimension anywhere in pricing (G3).

---

## 1. Findings

Ordered by money-at-risk, not by effort.

### G1 — P0 (correctness, live money bug): prompt-token accounting is provider-dependent, and the pricing engine assumes one convention

`pricing.Quote` computes:

```go
uncachedInput := usage.InputTokens - usage.CacheReadTokens - usage.CacheWriteTokens
if uncachedInput < 0 { uncachedInput = 0 }
```

That subtraction is correct **only** when the provider's prompt-token count is
*inclusive* of cached tokens. Providers disagree:

- **OpenAI / DeepSeek / OpenRouter-OpenAI dialect** — `prompt_tokens` **includes**
  cached tokens. Subtraction is correct.
- **Anthropic dialect** — `usage.input_tokens` **excludes** both
  `cache_read_input_tokens` and `cache_creation_input_tokens`. Subtraction is
  wrong: it deducts tokens that were never in the total.

Both paths feed the same function. `proxy/handler.go:1784-1790` (non-streaming
Anthropic) and `:1706-1710` (streaming Anthropic) assign the raw Anthropic
`input_tokens` straight into the field that `Quote` treats as inclusive.

**Concrete failure.** Anthropic-family request reports
`input_tokens=500, cache_read_input_tokens=40000`:

```
uncachedInput = 500 - 40000 - 0 = -39500  →  clamped to 0
```

The 500 genuinely fresh, full-price tokens are billed **at zero**. On a long
agent session with a warm cache this is a systematic, silent undercharge on
every single turn. The clamp hides it — there is no error, no log line, nothing.

This must be fixed **before** any new pricing dimension is layered on, otherwise
the new dimensions inherit a wrong base.

### G2 — P1: cache TTL is collapsed before pricing can see it

`OpenAIUsage.CacheWriteTokensReported()` (`proxy/translator.go:97`) returns a
single integer. The Anthropic stream parser reads only
`usage.cache_creation_input_tokens` (`proxy/handler.go:1758`) and ignores the
`cache_creation` breakdown object, which carries
`ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens`.

So even if `pricing.Rates` gained TTL-specific rates, **the token counts to
apply them to are already lost** by the time pricing runs. The fix is in
capture first, pricing second — in that order.

### G3 — P1: no time dimension (DeepSeek peak/off-peak 2×)

Nothing in the schema, the `pricing` package, or the admin UI knows about
time-varying prices. Adding it raises a design question the current code has no
answer to:

> A request is admitted at 16:29:58 UTC and settles at 16:30:04 UTC, across a
> price-window boundary. Which rate applies?

The package doc already states the invariant — *"the same snapshot must be used
for admission and settlement"* — but with no clock in the rule set there is
nothing to pin. Without an explicit pinned instant, budget admission and final
settlement can disagree, which is exactly the class of bug that produces
"charged more than the quote" support tickets.

### G4 — P1: charges are not reproducible (the enterprise gap)

`request_logs` stores tokens and one final `cost_nano_usd`. It does **not**
store which rates produced that number. `pricing_rule_set_id` is computed on
every request and then discarded.

Questions the system currently **cannot** answer:

- Was this request billed at peak or off-peak?
- Which context tier fired?
- How much of this month's spend was cache-write vs cache-read vs fresh input?
- The model's price was edited last Tuesday — were requests before that edit
  billed at the old rate, and can I prove it?

For an "enterprise-grade, highly detailed usage tracking" system, **the audit
trail is the product.** A total with no derivation is not auditable.

### G5 — P2: no drift detection against the upstream's own cost

OpenRouter reports actual upstream cost per generation. Nothing fetches or
stores it. If a model's real OpenRouter price changes and the local `models`
row is stale, the gateway bills the wrong amount **indefinitely and silently** —
the exact failure that motivated this whole request. Storing the upstream's
reported cost alongside the computed cost makes drift a detectable, alertable
condition instead of a discovery-by-accident.

### G6 — P2: model prices are mutable in place, with no version history

Editing a price in the admin UI does an `UPDATE models`. Historical
`request_logs` rows then reference a price that no longer exists anywhere.
Re-deriving a past invoice becomes impossible. (G4 mitigates this by snapshotting
rates onto each request; G6 is the stronger form — keep the versions themselves.)

### G7 — P3: rounding policy is per-component, not per-request

`Quote` ceilings **each** component independently, then sums. With the token
classes expanding from 4 to 7 (G2), a request accrues up to 7 sub-nano-USD
round-ups instead of 4. The amounts are trivially small (≤7 nano-USD ≈ $7e-9),
but the policy should be a stated, tested decision rather than an emergent one.

---

## 2. Target architecture

### 2.1 The resolution pipeline

Today `RuleSet.Quote(usage)` is a pure function of tokens. It becomes a function
of tokens **and an instant**, resolved in four ordered layers:

```
                 ┌──────────────────────────────────────────────┐
  models row  →  │ L1  Base rates                               │
                 │     input / output / cache_read / cache_write │
                 └───────────────────┬──────────────────────────┘
                                     │ selected by PROMPT TOTAL
  model_pricing_tiers ────────────►  │ L2  Context tier override
                                     │     (absolute rates, replaces L1)
                                     ▼
  model_cache_ttl_rates ──────────►  L3  TTL cache-rate variants
                                     │     (absolute, per token class)
                                     ▼
  model_price_windows ────────────►  L4  Time-window multiplier
                                     │     (exact rational num/den, by UTC clock)
                                     ▼
                              Effective Rates
                                     │
                                     ▼
                          Quote  +  PriceReceipt
```

**Why this order.** L2 and L3 are *absolute* rates — an OpenRouter row literally
states `>272K = $2.50/M`. L4 is a *multiplier* — DeepSeek's peak pricing is
"2× the normal rate", which composes over whatever L2/L3 selected. Modelling L4
as a multiplier rather than absolute rates means a DeepSeek price change needs
one row edited, not one row per tier per TTL.

### 2.2 Exactness under multiplication

A multiplier must not introduce float or double-rounding. Store it as an exact
rational (`multiplier_num`, `multiplier_den`, both `BIGINT`) and fold it into
the **single** existing ceiling operation:

```go
// money: one rounding step, not two.
// cost = ceil(tokens * ratePerMillion * num / (1_000_000 * den))
func CostForTokensScaled(tokens int64, ratePerMillion NanoUSD, num, den int64) (NanoUSD, error)
```

DeepSeek peak = `2/1`. A 50%-off promotional window = `1/2`. Both exact, no
float anywhere. `CostForTokens` becomes `CostForTokensScaled(t, r, 1, 1)`.

### 2.3 Canonical usage normalization (fixes G1)

Introduce one normalization step ahead of pricing so every provider's numbers
mean the same thing:

```go
// pricing.PromptAccounting describes whether a provider's reported prompt
// token count already includes tokens served from / written to cache.
type PromptAccounting string

const (
    PromptInclusive PromptAccounting = "inclusive" // OpenAI, DeepSeek, OpenRouter
    PromptExclusive PromptAccounting = "exclusive" // Anthropic
)
```

Normalization produces a canonical shape where both conventions agree:

| Reported | `inclusive` | `exclusive` |
|---|---|---|
| `PromptTotal` | `prompt_tokens` as reported | `input_tokens + reads + writes` |
| `FreshInput`  | `prompt_tokens - reads - writes` | `input_tokens` as reported |

**Both** the input-rate charge and the L2 tier threshold then read from the
canonical values — `FreshInput` for the charge, `PromptTotal` for the threshold.
This also fixes the latent half of G1: tier selection must key off total prompt
size (that is what OpenRouter's `≤272K` means), not off the post-subtraction
remainder.

**Anomaly surfacing.** If `FreshInput` computes negative under `inclusive`
accounting, the provider's own numbers are internally inconsistent. Clamp to
zero **and** set a new `usage_anomaly` flag on the request log. Today this
clamps silently — which is precisely how G1 stayed invisible.

Source of truth for the accounting mode: a new `prompt_accounting` column on
`models`, defaulted from `provider_family` during migration (`anthropic` →
`exclusive`, everything else → `inclusive`), operator-overridable.

### 2.4 Token classes

Expands from 4 to 7. Each is priced independently and, per G4, recorded
independently:

| Class | Rate source |
|---|---|
| `input_fresh` | L1/L2 `input` |
| `output` | L1/L2 `output` |
| `cache_read` | L1/L2 `cache_read` (default TTL) |
| `cache_read_5m` | L3, falls back to `cache_read` |
| `cache_write` | L1/L2 `cache_write` (default TTL) |
| `cache_write_5m` | L3, falls back to `cache_write` |
| `cache_write_1h` | L3, falls back to `cache_write` |

**Fallback is mandatory, not optional.** A model with no L3 rows must price
exactly as it does today. This is what keeps the migration non-breaking.

### 2.5 The price receipt (fixes G4)

Every priced request emits a `PriceReceipt` that is persisted, not just logged:

```go
type PriceReceipt struct {
    RuleSetID     string        // sha256 of the full resolved rule set
    PricedAt      time.Time     // the PINNED instant (admission time)
    TierThreshold int64         // which L2 tier fired; -1 = base rates
    WindowID      string        // which L4 window applied; "" = none
    MultiplierNum int64
    MultiplierDen int64
    Accounting    PromptAccounting
    Lines         []PriceLine   // one per non-zero token class
    Total         money.NanoUSD
}

type PriceLine struct {
    Class          string
    Tokens         int64
    RatePerMillion money.NanoUSD // rate BEFORE the L4 multiplier
    Cost           money.NanoUSD // cost AFTER the L4 multiplier
}
```

`sum(Lines[].Cost) == Total` is an invariant, asserted in tests and enforced by
a DB check. This is the artifact that makes a charge reproducible: given the
receipt alone, anyone can re-derive the number without the models table.

### 2.6 Clock pinning (fixes G3's boundary ambiguity)

`PricedAt` is captured **once**, at admission, and carried through settlement on
the request context. Settlement never calls `time.Now()`. Consequences:

- A request straddling a window boundary is billed entirely at the rate quoted
  to it. The quote is always honoured.
- The admission clamp (`MaxOutputTokens`) and the final charge use identical
  rates by construction, not by luck.
- Replaying a request log reproduces the same cost deterministically.

All window boundaries are stored and evaluated in **UTC**, as minutes-of-day, so
there is no DST class of bug. Windows that wrap midnight (DeepSeek's off-peak
runs 16:30→00:30 UTC) are supported explicitly via `start > end` meaning "wraps".

---

## 3. Schema changes

### Migration 032 — prompt accounting + usage anomaly flag (G1)

```sql
ALTER TABLE models
    ADD COLUMN IF NOT EXISTS prompt_accounting VARCHAR(16) NOT NULL DEFAULT 'inclusive';

ALTER TABLE models DROP CONSTRAINT IF EXISTS models_prompt_accounting_valid;
ALTER TABLE models ADD CONSTRAINT models_prompt_accounting_valid
    CHECK (prompt_accounting IN ('inclusive', 'exclusive'));

-- Anthropic-family models report prompt tokens EXCLUDING cached tokens.
UPDATE models SET prompt_accounting = 'exclusive'
 WHERE lower(COALESCE(provider_family, '')) = 'anthropic'
    OR lower(COALESCE(cache_contract, ''))  LIKE 'anthropic%';

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS usage_anomaly VARCHAR(64) NOT NULL DEFAULT '';
```

> **Backfill decision required (see §7 Open questions Q1).** Existing
> Anthropic-family `request_logs` rows were undercharged. This migration does
> **not** retro-bill them; it only stops the bleeding. Retro-billing is an
> explicit business decision, not a technical default.

### Migration 033 — cache TTL rates (G2)

```sql
CREATE TABLE IF NOT EXISTS model_cache_ttl_rates (
    id                          VARCHAR(100) PRIMARY KEY,
    model_id                    VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    -- NULL threshold = applies to base rates; otherwise pairs with the
    -- model_pricing_tiers row at the same threshold.
    min_input_tokens_exclusive  BIGINT,
    ttl                         VARCHAR(16) NOT NULL,   -- '5m' | '1h' | 'default'
    cache_read_nano_usd_per_million   BIGINT,           -- NULL = inherit L1/L2
    cache_write_nano_usd_per_million  BIGINT,           -- NULL = inherit L1/L2
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (model_id, min_input_tokens_exclusive, ttl),
    CHECK (ttl IN ('5m', '1h', 'default')),
    CHECK (cache_read_nano_usd_per_million  IS NULL OR cache_read_nano_usd_per_million  >= 0),
    CHECK (cache_write_nano_usd_per_million IS NULL OR cache_write_nano_usd_per_million >= 0)
);
```

`NULL` rate = inherit from L1/L2. That is what makes every existing model price
identically after this migration.

### Migration 034 — time-of-day price windows (G3)

```sql
CREATE TABLE IF NOT EXISTS model_price_windows (
    id            VARCHAR(100) PRIMARY KEY,
    model_id      VARCHAR(100) NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    label         VARCHAR(64)  NOT NULL,
    -- UTC minutes-of-day, [start, end). start > end means the window wraps midnight.
    start_minute_utc  INTEGER NOT NULL,
    end_minute_utc    INTEGER NOT NULL,
    -- Bitmask of ISO weekdays, Monday = bit 0. 127 = every day.
    weekday_mask      INTEGER NOT NULL DEFAULT 127,
    -- Exact rational multiplier over the L1/L2/L3-resolved rates.
    multiplier_num    BIGINT NOT NULL,
    multiplier_den    BIGINT NOT NULL,
    -- Which token classes it applies to; empty = all.
    applies_to        TEXT NOT NULL DEFAULT '',
    priority          INTEGER NOT NULL DEFAULT 0,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    effective_from    TIMESTAMPTZ,
    effective_until   TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (start_minute_utc BETWEEN 0 AND 1439),
    CHECK (end_minute_utc   BETWEEN 0 AND 1440),
    CHECK (weekday_mask BETWEEN 1 AND 127),
    CHECK (multiplier_num >= 0),
    CHECK (multiplier_den >  0)
);

CREATE INDEX IF NOT EXISTS idx_model_price_windows_model_enabled
    ON model_price_windows (model_id, enabled, priority DESC);
```

**Exactly one window applies** — the enabled, in-effect, matching window with
the highest `priority` (ties broken by `id` for determinism). Windows do not
stack; stacking multipliers is unauditable and invites accidental 4× pricing.

### Migration 035 — the audit trail (G4, G5)

```sql
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS pricing_rule_set_id  VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pricing_tier_threshold BIGINT,
    ADD COLUMN IF NOT EXISTS price_window_id      VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS price_multiplier_num BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS price_multiplier_den BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS priced_at            TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS prompt_accounting    VARCHAR(16) NOT NULL DEFAULT 'inclusive',
    -- G5: the upstream's own reported cost, for drift detection. NULL = not reported.
    ADD COLUMN IF NOT EXISTS upstream_cost_nano_usd BIGINT;

ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_multiplier_valid,
    ADD CONSTRAINT request_logs_multiplier_valid
        CHECK (price_multiplier_num >= 0 AND price_multiplier_den > 0);

CREATE TABLE IF NOT EXISTS request_pricing_lines (
    request_log_id  VARCHAR(100) NOT NULL REFERENCES request_logs(id) ON DELETE CASCADE,
    token_class     VARCHAR(32)  NOT NULL,
    tokens          BIGINT       NOT NULL,
    rate_nano_usd_per_million BIGINT NOT NULL,
    cost_nano_usd   BIGINT       NOT NULL,
    PRIMARY KEY (request_log_id, token_class),
    CHECK (tokens >= 0 AND rate_nano_usd_per_million >= 0 AND cost_nano_usd >= 0),
    CHECK (token_class IN (
        'input_fresh', 'output',
        'cache_read', 'cache_read_5m',
        'cache_write', 'cache_write_5m', 'cache_write_1h'
    ))
);

CREATE INDEX IF NOT EXISTS idx_request_pricing_lines_class
    ON request_pricing_lines (token_class);
```

`request_pricing_lines` is written in the **same transaction** as its
`request_logs` row. A charge without its derivation must be impossible.

---

## 4. Go changes

### 4.1 `money/money.go`

- Add `CostForTokensScaled(tokens int64, ratePerMillion NanoUSD, num, den int64) (NanoUSD, error)`
  — single `big.Int` expression `ceil(tokens*rate*num / (1e6*den))`, one rounding.
- Reimplement `CostForTokens` as `CostForTokensScaled(t, r, 1, 1)` so there is
  one arithmetic path, not two.
- Reject `den <= 0` with `ErrInvalid`; reject negative `num` with `ErrNegative`.

### 4.2 `pricing/pricing.go`

- `Rates` gains `CacheRead5mPerMillion`, `CacheWrite5mPerMillion`,
  `CacheWrite1hPerMillion`. Zero = inherit the default-TTL rate (mirrors the
  existing `writeRate == 0 → InputPerMillion` fallback idiom already in `Quote`).
- `Usage` gains `CacheRead5mTokens`, `CacheWrite5mTokens`, `CacheWrite1hTokens`,
  and `PromptTotalTokens`.
- New `NormalizeUsage(reported ReportedUsage, mode PromptAccounting) (Usage, Anomaly)`
  implementing §2.3.
- New `Window` type + `RuleSet.Windows []Window`; `RuleSet.WindowFor(at time.Time) *Window`
  implementing §2.6 selection (UTC minutes-of-day, midnight wrap, weekday mask,
  effective range, highest priority wins).
- `RuleSet.Quote(usage)` → `RuleSet.QuoteAt(usage Usage, at time.Time) (Quote, error)`.
  Keep `Quote(usage)` as a thin wrapper (`QuoteAt(usage, time.Time{})` = no window)
  so existing call sites and tests compile untouched.
- `Quote` gains the `PriceReceipt` (§2.5).
- `MaxOutputTokens` → `MaxOutputTokensAt(..., at time.Time)`. Monotonicity in
  output tokens still holds — the window is fixed by `at` and the tier by input
  tokens, so binary search remains valid. **Add a test that asserts this**, since
  the search silently produces garbage if monotonicity is ever broken.

### 4.3 `db/db.go`

- `Model` gains `PromptAccounting string`, `CacheTTLRates []pricing.TTLRate`,
  `PriceWindows []pricing.Window`.
- Load TTL rates and windows alongside `PricingTiers` in the existing model-load
  query path. **Batch-load** them (one query per table for the whole model set,
  joined in memory) — a per-model query here would put two extra round-trips on
  every catalog refresh.
- `SaveRequestLog` writes `request_logs` + `request_pricing_lines` in one
  transaction.

### 4.4 `proxy/handler.go`

- `pricingRulesForModel` folds TTL rates + windows into the canonical string that
  feeds the `sha256` rule-set ID, so a window or TTL edit produces a new ID.
  **This is required for correctness of the audit trail**, not cosmetic.
- Pin `PricedAt` at admission; thread it through to `setCalculatedCost`.
- Replace the four Anthropic/OpenAI usage-assignment sites
  (`:1260`, `:1342`, `:1706`, `:1784`) with a single shared
  `normalizedUsageFor(model, reported)` helper. Four hand-rolled copies of
  usage extraction is how G1 got in; consolidating them is what keeps it out.
- Parse the Anthropic `cache_creation` breakdown object
  (`ephemeral_5m_input_tokens`, `ephemeral_1h_input_tokens`) in both the
  streaming and non-streaming paths, falling back to the flat
  `cache_creation_input_tokens` when absent.

### 4.5 `proxy/translator.go`

- `OpenAIUsage.PromptTokensDetails` gains the TTL-split fields OpenRouter passes
  through; `CacheWriteTokensReported()` keeps its current signature for
  compatibility and gains `CacheWriteTokensByTTL() (default, fiveMin, oneHour int)`.

### 4.6 New: `GET /v1/pricing` (effective-rate endpoint)

Returns, per discoverable model: current effective rates, the active window, and
**when the next window change occurs**. This is what lets MuhiyaCode surface
"off-peak pricing active — ends in 2h14m", which is the whole practical point of
peak/off-peak pricing for a user deciding whether to run a big job now or later.

Cache-control must be `no-cache` (matching the fix already made to the catalog
endpoint in GW-6) — a cached response here would show a stale window.

---

## 5. Admin UI (`static/index.html`)

The model editor gains three sections. All three must render correctly when
empty, since almost every model will have no TTL rates and no windows.

1. **Context tiers** — surface the existing `model_pricing_tiers` (currently only
   reachable via SQL, which is why migration 025's capability is effectively
   invisible today).
2. **Cache TTL rates** — a small grid: rows `default / 5m / 1h`, columns
   `cache read / cache write`. Blank cell = inherit, and must say so.
3. **Price windows** — label, UTC time range, weekday checkboxes, multiplier as
   `num/den` with a live preview ("2/1 = 2.00× — peak"). Show a 24-hour strip
   visualising coverage, and **warn on overlap** since only the highest-priority
   window applies.

A "price this request" simulator (tokens + timestamp → full receipt breakdown)
belongs here too. It is the cheapest possible way to verify a pricing config is
right *before* it bills a real customer. Note `plan-simulator.html` already
exists in the repo root as a precedent for this kind of tool.

---

## 6. Execution order

Each phase is independently shippable and leaves the tree green. **Phase 1 is
not optional and must not be reordered** — it is an active money bug, and every
later phase compounds on top of its base rates.

| # | Phase | Fixes | Depends on |
|---|---|---|---|
| 1 | Prompt-accounting normalization + anomaly flag + usage-extraction consolidation | G1 | — |
| 2 | Receipt plumbing: `PriceReceipt`, migration 035, transactional line writes | G4 | 1 |
| 3 | `CostForTokensScaled` exact-rational arithmetic | G7 | — |
| 4 | Cache TTL capture (translator + Anthropic parser), then TTL rates (migration 033) | G2 | 1, 2 |
| 5 | Price windows (migration 034) + clock pinning + `QuoteAt` | G3 | 2, 3 |
| 6 | `GET /v1/pricing` + admin UI (tiers, TTL, windows, simulator) | — | 4, 5 |
| 7 | Upstream cost capture + drift alerting | G5 | 2 |
| 8 | Price version history | G6 | 2 |

Phases 7 and 8 are genuinely optional. Phases 1–6 are the plan.

---

## 7. Verification

### Test matrix (all in-package, no network)

**Phase 1 (G1) — the regression that pays for this whole plan:**
- Anthropic-shaped usage `{input:500, cache_read:40000, cache_write:0}` bills
  500 tokens at the input rate — **not zero**. This test fails on today's code;
  that failure is the proof the bug is real.
- OpenAI-shaped usage `{prompt:40500, cache_read:40000}` bills 500 fresh tokens.
- Both shapes, same underlying reality, produce the **identical** cost. This is
  the strongest available statement of correctness.
- Inconsistent inclusive usage (`prompt < reads`) clamps to zero **and** sets
  `usage_anomaly`.

**Phase 4 (G2):**
- Model with no TTL rows prices byte-identically to pre-migration — golden test.
- 5m write tokens use the 5m rate; 1h use the 1h rate; unspecified inherits.
- Anthropic `cache_creation` breakdown parses in **both** streaming and
  non-streaming paths (two separate tests — these are two separate code paths,
  and the streaming one is where G1 also hides).

**Phase 5 (G3):**
- DeepSeek 2× window: same usage at 12:00 UTC and 20:00 UTC differ by exactly 2×,
  with no rounding drift.
- Midnight-wrapping window (16:30→00:30) matches at 23:00 **and** 01:00-minus,
  and does not match at 12:00.
- Weekday mask excludes the right days.
- Clock pinning: a request pinned at 16:29:58 bills off-peak even when settled
  after the boundary.
- `MaxOutputTokensAt` monotonicity holds under every window/tier combination.

**Phase 2 (G4):**
- `sum(receipt.Lines[].Cost) == receipt.Total` — property test over randomized
  usage.
- A persisted receipt re-derives the original cost with the `models` table
  emptied. This is the actual definition of "auditable" and worth asserting
  literally.

**Exactness (G7):** randomized property test that no quote path produces a
result differing from a `big.Rat` reference computation by more than the single
documented ceiling step.

### Commands

```bash
go build ./... && go vet ./... && go test ./...
```

Run the migrations against a scratch database and confirm every pre-existing
model prices identically before and after — that is the non-breaking guarantee.

> **Note on `-race`:** this environment has `CGO_ENABLED=0` and no C toolchain,
> so `go test -race` cannot run here and will fail immediately with
> *"-race requires cgo"*. Race coverage must be run in CI (or a cgo-enabled
> machine), and should not be reported as passing locally.

---

## 8. Open questions

These need a decision from you; I have not assumed answers.

- **Q1 — Retro-billing.** Anthropic-family requests have been undercharged
  (G1). Do we (a) fix forward only, (b) quantify the gap and report it, or
  (c) retro-bill? I would default to (b) — quantify it so you know the size,
  then decide. The query is cheap; the decision is a business one.
- **Q2 — DeepSeek's actual window.** I have not verified DeepSeek's current
  published peak/off-peak hours or multiplier, and I would rather build the
  mechanism than hardcode numbers I have not confirmed. The schema supports any
  window; the seed values need your confirmation from DeepSeek's live pricing page.
- **Q3 — Does your OpenRouter traffic actually report TTL-split cache tokens?**
  The schema handles it either way, but if the passthrough collapses them, the
  5m rates are unreachable in practice and Phase 4 should be scoped down to
  Anthropic-direct traffic only. One captured response body would settle this.
- **Q4 — Margin.** Everything above prices at *cost*. If MuhiyaCode is ever to
  charge a markup over upstream, the multiplier mechanism (L4) is the natural
  place — but margin and peak-pricing are different concepts and should not
  share a column. Say the word and I will design it as a separate layer.
