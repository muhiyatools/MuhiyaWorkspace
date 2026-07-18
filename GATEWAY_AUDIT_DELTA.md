# Gateway Audit — Delta (verification of the 2026-07-11 findings + this session's changes)

**Date**: 2026-07-19 · **Basis**: re-verifies every G-finding in `GATEWAY_AUDIT.md` §3 against the code as it stands today, and records the stability/caching/discoverability work landed this session. Evidence is `file:line` at time of writing. Status legend: **FIXED** (code now prevents it), **CONFIG** (safe unless misdeployed — an operator action, not a code bug), **OPEN** (still true; tracked below), **BY-DESIGN** (documented tradeoff, not a defect).

---

## 1. Verdicts on the original G-findings

| ID | Sev | Status | Evidence / note |
|---|---|---|---|
| G1 | C | **FIXED** | Router stickiness: `proxy/stickysession.go` pins the chosen model per `X-Muhiya-Session` (24h TTL, evict-on-upstream-failure), so effort/tier changes no longer flip `deepseek-chat ↔ deepseek-reasoner` mid-session. Agent sends the header (`internal/gateway/provider.go`). |
| G2 | H | **FIXED** | OpenAI→OpenAI path now forwards the client's raw body through a `map[string]interface{}` (`proxyOpenAIToOpenAI`, `handler.go:993+`); undeclared fields survive. Comment `translator.go:19-24`. |
| G3 | H | **FIXED** | Same passthrough preserves per-message `reasoning_content` (no typed re-decode on the hot path). |
| G4 | M | **MITIGATED** | Still a map round-trip, but `json.Marshal(map)` is key-sorted/deterministic; pinned by `TestDeepSeekTransformDeterministic` (added this session). Byte-neutral. |
| G5 | M | **FIXED** | Thinking is effort-gated, not applied to bare `deepseek-chat` unconditionally; `feature009_deepseek_capture_test.go` scenarios `chat-absent`/`reasoner-absent` lock the pass-through. |
| G6 | M | **MITIGATED** | Complexity-tier drift is neutralized by G1 stickiness (a pinned session never re-tiers). Tiers remain `none` by default. |
| G7–G9 | L/M | **N/A here** | MuhiyaChat agent-loop / alias-attribution items; not on MuhiyaCode's path. Unchanged. |
| G10 | C | **FIXED** | Predictable seed key `key-admin-test-12345` is commented out in both `setup_database.sql:195,205` and `seed.sql:61` — no longer active/billable. |
| G11 | H | **CONFIG** | `main.go:58` still falls back to `adminpassword` when `ADMIN_PASSWORD` is unset (boots with a warning). Deploy must set `ADMIN_PASSWORD`/`SERVICE_PASSWORD`. Not a code bug; a deployment checklist item. |
| G12 | H | **FIXED** | Graceful shutdown (`srv.Shutdown`, 60s drain) + `ReadHeaderTimeout`/`IdleTimeout` on an explicit `*http.Server` (`main.go`). |
| G13 | H | **FIXED** | Virtual keys stored as SHA-256 (`hashToken`, `db.go:295`); provider keys encrypted at rest (`encryptProviderKey`, `db.go:337`). Boot backfill `backfillVirtualKeyHashes`. |
| G14 | H | **FIXED** | `saveRequestLog` → durable retry `outbox.go`; no `_ =` discards on the billing path. **This session**: drops now increment `proxy.BillingLossCount`, surfaced on `/health` (see §3). |
| G15 | H | **BY-DESIGN** | Redis limiter with in-memory fallback that fails **open** (`limiter.go`). Documented: availability over strict enforcement during a Redis outage. Multi-replica exactness needs Redis present. |
| G16 | H | **FIXED** | Upstream client has `DialContext`/`TLSHandshakeTimeout` (10s), `ResponseHeaderTimeout` (630s), overall 15m; plus the 120s mid-stream idle watchdog (`armIdleWatchdog`, `handler.go:55`). |
| G17 | M | **MITIGATED** | `limiterDefsCache` bounds steady-state limiter-definition queries. Budget `SUM(cost)` per window remains; acceptable at current scale (revisit with partitioning — G18). |
| G18 | M | **PARTIAL** | Retention sweeper exists (`runRetentionSweeper`); no partitioning yet. OPEN as a scale item, not a correctness bug. |
| G19 | M | **FIXED** | Migrations serialize under `pg_advisory_lock` on a pinned conn, each file in its own tx (`db/migrations.go:33-46`). Zero-downtime for additive migrations (incl. 021). |
| G20 | M | **FIXED** | Redis limiter is a single atomic Lua script (`limiter.go:274-316`) — no check-then-commit TOCTOU. |
| G21 | M | **FIXED** | In-memory limiter map is swept (`sweepInMemoryLimiters`, `limiter.go:163`). |
| G22 | M | **FIXED (internal errs)** | Auth/DB errors return generic messages (`internalErrorResponse`), and **this session** DB-outage auth returns 503 not a raw 500 (A2e). Full-body-on-parse-error logging: verify no prompt content is logged (request_logs store none — audit §3 "verified solid"). |
| G23 | M | **FIXED** | `/v1/models`, `/v1/capabilities`, `/v1/usage` all require `authenticateVirtualKey` (`handler.go:312,324,...`). **This session** discovery is additionally filtered per client app (C3). |
| G24 | M | **FIXED** | `recoveryMiddleware` wraps the mux (`main.go:364`) with request-scoped recovery. |
| G25 | M | **CONFIG/HYGIENE** | Tracked binaries / `.dockerignore` / stale SQLite artifacts are repo-hygiene; no runtime effect. OPEN for a cleanup PR. |
| G26 | M | **PARTIAL** | Dockerfile builds `go build -o muhiyallm .` (package build, not single-file), runs non-root, has a HEALTHCHECK. Base-tag pinning still advisable (CONFIG). |
| G27 | L | **FIXED** | Meta chunk is emitted **before** `data: [DONE]` (`finish()` ordering, `handler.go:1061-1065`). |
| G28 | L | **FIXED** | `log.UsageEstimated` marks estimate-derived rows. |
| G29 | L | **FIXED** | `/tools/web_search` body is bounded by `MaxBytesReader` (`handler.go:358`); it is also authenticated now. |

**Net**: every Critical/High from the original audit is FIXED or a deploy-time CONFIG item. Remaining OPEN items (G18 partitioning, G25 hygiene, G26 base-tag pinning) are scale/hygiene, not correctness or security.

---

## 2. Defects found and fixed THIS session (not in the original audit)

| ID | Sev | Fix | Test |
|---|---|---|---|
| A2a | M | `/v1/models/{id}` detail branch was missing the `status=="active"` check the list branches had → could return an inactive model. Now uses the shared `discoverableModel()` helper. | `discovery_visibility_test.go` |
| A2b | M | `proxyOpenAIToOpenAI` / `proxyAnthropicToAnthropic` assigned to a nil map when the body decoded to `null` (reachable: typed decode accepts `null`) → recovered 500. Now a clean 400. | covered by build + guard |
| A2c | L | Sticky map grew unbounded under >10k *live* sessions (the overflow sweep only removed expired entries). Now evicts arbitrary live entries to the cap, sparing the just-set key. | `stickysession_test.go` |
| A2d | L | Per-stream `textAccumulator` was unbounded; capped at 2 MiB (fallback estimator only; billing uses reported usage). | — |
| A2e | M | DB error during auth returned 500 (reads as "don't retry"); now 503 + `Retry-After` so a Postgres blip is a retryable, not a hard sign-in failure. | — |
| A2f | L | Outbox billing-loss now increments `proxy.BillingLossCount`, surfaced on `/health` as `billing_loss` — silent loss is observable with one curl. | `outbox_test.go` |

---

## 3. Sign-in chain verification (plan A4)

Chain: platform `/authorize` (browser, PKCE) → platform `/api/oauth/token` mints an `sk-virt` via the gateway admin/service API (`admin/handlers.go handleKeys` → `db.CreateVirtualKey`, stores only the SHA-256 hash) → agent persists the key → every request `authenticateVirtualKey` (`handler.go:413`, hash lookup `db.go`).

| Check | Status | Evidence / action |
|---|---|---|
| DB-outage auth returns retryable 503, not 500 | **FIXED** | A2e; agent surfaces a friendly retry rather than a hard failure. |
| Expired / revoked keys → 401 with a distinguishable message | **OK** | `handler.go:414` (revoked) vs `:419` (expired) — distinct messages. |
| `/v1/models` requires a valid key before discovery | **OK** | `handler.go:312`. Confirms onboarding must complete before discovery; the agent only calls discovery on key-present paths, so no pre-login 401 spam. |
| Pre-migration-006 keys authenticate | **NEEDS LIVE CHECK** | Run `SELECT count(*) FROM virtual_keys WHERE key_hash IS NULL OR key_hash=''` → must be 0 (boot `backfillVirtualKeyHashes` should have zeroed it). |
| Service-credential allowlist covers exactly the platform's mint flow | **OK (code)** | `main.go serviceAllowlist`; no models/discovery endpoints in it (correct — minting uses `/api/keys`). |
| CSRF guard does not block server-to-server platform calls | **OK (code)** | `main.go` cross-site guard keys on state-changing browser requests with an Origin; server calls send none. Regression-worth a live smoke. |

---

## 4. Caching posture (DeepSeek + OpenRouter) — verified

- **No gateway-side response cache exists** (confirmed again): the gateway's levers are (a) byte-stable transforms, (b) per-session model stickiness, (c) honest usage parsing/billing. DeepSeek/OpenRouter cache automatically upstream.
- **DeepSeek byte-stability** pinned by `TestDeepSeekTransformDeterministic` + `TestDeepSeekTransformPreservesPrefix` (added this session): the transform is deterministic and never mutates prior messages, so the prefix cache keeps hitting.
- **Dual usage dialects** parsed: DeepSeek `prompt_cache_hit/miss_tokens` and OpenAI/OpenRouter `prompt_tokens_details.{cached,cache_write}_tokens` — `TestDeepSeekUsageDialectParsing`, `TestOpenRouterUsageParsingAndBilling` (billing incl. the input-rate write fallback).
- **OpenRouter → Claude** cache breakpoints: `InjectOpenRouterAnthropicCache` (gated to OpenRouter + Claude targets; a strict no-op for DeepSeek and everyone else). Unit-tested; **needs a live two-turn check only once an OR-Claude model row exists** — none today.
- **Anthropic-path injection** now walks back past a trailing thinking block (`lastCacheableBlock`) so a message ending in thinking still gets a breakpoint.

---

## 5. Still requiring live infrastructure / operator action (not code)

1. **Owner**: uncheck "MuhiyaCode Discoverable" on chat-only rows (Gemini 2.5 Flash Lite, etc.) after deploy — the one manual step that makes the flag do its job. Verify with the two `curl` calls in `STABILITY_AND_DISCOVERABILITY_PLAN.md` §7.
2. **Live cache gauntlet**: two-turn DeepSeek (and OR-Claude if/when added) hit-rate + cost-reconciliation against the running gateway.
3. **Pricing-row audit**: confirm `cache_read/write_cost_per_million` on each `openrouter`/`deepseek` model row matches current upstream pricing (data, via the admin panel).
4. **DB-integration tests**: run the suite with `TEST_DATABASE_URL` set (the `model_visibility` round-trip and admin merge-patch tests skip without it).
5. **Deploy**: migration 021 auto-applies under the advisory lock; the backfill makes it behavior-neutral. Confirm `/health` shows the new migration count post-deploy.
