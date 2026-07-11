# MuhiyaLLM Gateway — Full Audit (Architecture, Prompt Caching, Production Readiness)

**Date**: 2026-07-11 | **Auditor**: Claude (read-only; no code was modified)
**Repo**: `F:\MuhiyaWorkspace\MuhiyaWorkspace` | **Related client repo**: `F:\MuhiyaCode Agent Go`
**Executor**: This document is written to be implemented directly by **Sonnet 5.0**. Every
finding carries file:line evidence, an exact fix, and verification steps. Work Phase 0 first
(§6). Do not begin a fix without reading its acceptance criteria (§7).

---

## 1. Architecture overview

**Stack**: Go 1.22, stdlib `net/http` (no framework). Dependencies (`go.mod:5-14`):
`lib/pq` (PostgreSQL), `redis/go-redis/v9`, `google/uuid`.

**Components**

| Component | Files | Role |
|---|---|---|
| HTTP server + middleware | `main.go` | Routing table, path normalization, logger, CORS, admin Basic auth, DB watchdog/readiness |
| Proxy core | `proxy/handler.go` | Auth (virtual keys), body parsing, model resolution, upstream calls, SSE streaming, usage extraction, cost + logging |
| Translation | `proxy/translator.go` | OpenAI↔Anthropic type mapping; the typed structs the OpenAI path decodes into |
| Routing | `proxy/router.go` | `muhiya-ai-router` heuristic, failover candidates, `BufferedResponseWriter` |
| Thinking/effort | `proxy/thinking.go` | `X-Muhiya-Effort` → per-provider `thinking`/`reasoning_effort` mapping |
| Anthropic cache breakpoints | `proxy/cache.go` | Injects `cache_control` for `claude*` models only — **not a response cache**; no-op for DeepSeek |
| MuhiyaChat agent loop | `proxy/agent.go`, `proxy/tools.go`, `proxy/websearch.go`, `proxy/skills/` | Web-search/skills injection — **only** for `X-Client-App: MuhiyaChat` with zero tools; never on MuhiyaCode's path |
| Rate limiting | `proxy/limiter.go` | RPM/TPM/budget; Redis if `REDIS_URL` set, else per-process in-memory |
| Data layer | `db/db.go`, `db/migrations/` | PostgreSQL: plans, budget_windows, users, virtual_keys, providers, models, request_logs, system_settings, user_topups |
| Admin | `admin/handlers.go`, `static/` | Dashboard + CRUD API on the same public port |

**Data-store reality** (corrects assumptions in the audit request):

- **PostgreSQL is the only persistent store** (`db/db.go:161`; default DSN `main.go:27`).
  There is **no SQLite driver** in `go.mod` — the `gateway.db`/`-wal`/`-shm` files in the repo
  root are stale artifacts of a retired backend (untracked; see G25).
- **Redis is optional and used only for distributed rate limiting**
  (`proxy/limiter.go:46-63`). It is not a cache of prompts, responses, or usage. Absent/
  unreachable Redis silently falls back to in-memory limiting (`limiter.go:57-63,136-141`).
- **There is no gateway-side prompt/response cache of any kind** (verified by grep across
  `proxy/`: no `sync.Map`/LRU/keyed response store). The only caching in the system is
  **DeepSeek's upstream prefix cache**; the gateway reads its token counts for billing.
  Consequence: there is no cache-key generation to audit — and no risk of the gateway serving
  a stale completion — but also no gateway-side lever to *create* cache hits. Cache-hit rate
  is determined entirely by (client byte stability) × (gateway transformation fidelity) ×
  (DeepSeek template/eviction behavior).
- `docker-compose.yml` defines **no** postgres/redis services; both are external, injected via
  `DATABASE_URL`/`REDIS_URL` (`docker-compose.yml:16,21`).

---

## 2. Complete request flow: MuhiyaCode → Gateway → DeepSeek

Byte-affecting operations in execution order (the numbered ops matter for §4):

1. `pathNormalizationMiddleware` → `loggerMiddleware` → mux (`main.go:158`); `/v1/chat/completions`
   passes the `dbReady` gate (`main.go:125-133`) and CORS (`main.go:228`).
2. **Auth**: virtual key via `Bearer` or `x-api-key` (`proxy/handler.go:268,302-309`) — a
   primary-key lookup in Postgres (`handler.go:316`, `db/db.go:569-571`).
3. **Body read**: `MaxBytesReader` 24 MiB (`handler.go:276`).
4. **[Op-1, lossy] Decode into typed struct** `OpenAIRequest` (`handler.go:388`,
   `translator.go:53-66`). Any field not declared is dropped — including per-message
   `reasoning_content` (`OpenAIMessage` has only role/content/tool_calls/tool_call_id,
   `translator.go:14-19`) and top-level `top_p, stop, frequency_penalty, presence_penalty,
   seed, n, logprobs, logit_bias, response_format, parallel_tool_calls, user, …` (G2, G3).
5. **Agent-loop gate**: skipped for MuhiyaCode (`handler.go:403-407`, `agent.go:43-51` —
   requires `X-Client-App: MuhiyaChat` AND zero tools). No injection of any kind on this path.
6. **Effort resolution**: `X-Muhiya-Effort` header wins over body `reasoning_effort`
   (`handler.go:411`, `thinking.go:95-109`); invalid values normalize to `""`.
7. **Model resolution** (`handler.go:413-456`): concrete model → `GetModelByName`;
   `muhiya-ai-router` → complexity heuristic + `RouteToModel` (**per request, no
   stickiness** — G1). Rate limit check (4–5 DB round trips — G17) at `handler.go:471-475`.
8. **[Op-2] Struct re-marshal** (`handler.go:482-483,545`): emits struct field order; unknown
   fields already gone; Go's encoder HTML-escapes `<>&`.
9. **[Op-3] Map round-trip + mutation** (`proxyOpenAIToOpenAI`, `handler.go:754-772`):
   unmarshal to `map[string]any`; set `model` = target; `delete(web_search)`;
   `ApplyThinkingOpenAI` adds `thinking:{type:"enabled"}` + `reasoning_effort:"high"|"max"`
   for any `deepseek*` target (G5); force `stream_options.include_usage:true`; final
   `json.Marshal` (map keys emitted **alphabetically sorted** — deterministic).
10. **POST** `{provider.BaseURL}/chat/completions`, `Authorization: Bearer provider.APIKey`
    (`handler.go:773-784`), shared pooled `http.Client` with a single coarse 15-minute
    timeout (G16). Failover is pre-stream only via `BufferedResponseWriter`
    (`router.go:249-296`) — no mid-stream retry, no duplication (correct).
11. **Streaming back**: each upstream SSE line is written to the client **verbatim**
    (`handler.go:815`) and separately parsed for usage. DeepSeek's
    `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens` and nested
    `prompt_tokens_details.cached_tokens` reach MuhiyaCode **unmodified** (matches the client's
    recorded raw payloads). Client disconnect cancels upstream via request context
    (`handler.go:778`).
12. **Accounting**: usage extracted → `calculateCost` (`handler.go:1519-1534`;
    `standardInput = input − cacheRead − cacheWrite`, cache reads billed at
    `cache_read_cost_per_million` — correct and unit-tested) → synchronous
    `InsertRequestLog` + `RecordTokens` with **errors discarded** (G14).

**Determinism verdict**: no timestamps, UUIDs, randomness, or session markers enter the
forwarded body; map-key sorting is stable; the transform is a pure function of the client
body + catalog. Request N+1's forwarded prefix extends request N's — which is why fresh
sessions share the 512-token system-prompt head across sessions in live traffic.

---

## 3. All identified issues and risks

Severity: **C**ritical / **H**igh / **M**edium / **L**ow. "Cache" = affects cache-hit rate.

| ID | Sev | Area | Title | Evidence |
|---|---|---|---|---|
| G1 | C | Cache/Routing | Router re-selects the upstream model per request; effort/thinking flag flips `deepseek-chat` ↔ `deepseek-reasoner` (disjoint cache namespaces) | `router.go:105,136-144`; `handler.go:419-445,438`; all seeded `routing_tier='none'` (`seed.sql:43-47`, `setup_database.sql:177-182`) so tier passes never match |
| G2 | H | Cache/Fidelity | Typed-struct round-trip silently drops all undeclared OpenAI fields (`stop`, `response_format`, `seed`, `parallel_tool_calls`, `top_p`, `user`, …) | `translator.go:53-66`; `handler.go:388,545` |
| G3 | H | Cache/Fidelity | Per-message `reasoning_content` silently stripped — the client's DeepSeek thinking-mode empty-key protocol never reaches the provider | `translator.go:14-19`; `handler.go:388` |
| G4 | M | Fidelity | Double re-serialization (struct→map) reorders keys + HTML-escapes `<>&`; deterministic (cache-neutral) but client bytes don't survive and the redundant hop invites future nondeterminism | `handler.go:545,756,772` |
| G5 | M | Correctness | `ApplyThinkingOpenAI` injects `thinking` + `reasoning_effort` for **any** `deepseek*` target, including non-thinking `deepseek-chat` (400/ignored risk) | `thinking.go:160,224-233` |
| G6 | M | Cache/Routing | Complexity heuristic sums the whole growing transcript (400/1500-char thresholds) — mid-session tier drift will flip models if any tier is ever configured | `router.go:33-65` |
| G7 | M | Cache (MuhiyaChat) | Agent loop sends tools on iteration 1 then `nil` afterwards (tail re-render per iteration); guidance/skill text injected into the system message is runtime-editable mid-session | `agent.go:123-128,381-400,402-435` |
| G8 | L | MuhiyaChat | `injectToolGuidance` prepends a new system message when content is non-string (array-shape change) | `agent.go:390-399` |
| G9 | L | Billing analytics | `deepseek-v4-flash` alias duplicates `deepseek-chat` target at identical price — router tie-break picks either; same cache namespace (no cache impact), attribution differs | `seed.sql:46`; `router.go:170-178` |
| G10 | C | Security | Predictable seeded virtual key `key-admin-test-12345` active in `setup_database.sql` — anyone can bill `usr-admin` | `setup_database.sql:196-198` |
| G11 | H | Security | Default admin password `"adminpassword"` boots with only a warning; admin dashboard + CRUD API share the public port | `main.go:41-45`; `docker-compose.yml:9-10,18` |
| G12 | H | Reliability | No graceful shutdown, no `http.Server` timeouts (`ListenAndServe` + `select{}`): SIGTERM severs streams and loses their billing rows; Slowloris exposure | `main.go:156-161,266` |
| G13 | H | Security | Virtual keys are their own plaintext primary key; provider (DeepSeek) API keys plaintext at rest | `db/migrations/001_initial.sql:44-51,59`; `db.go:569-571` |
| G14 | H | Billing | Every `InsertRequestLog`/credit-deduction error is discarded (`_ =`) — silent revenue loss on any DB blip | `handler.go:857,886,988,1009,1105,1124,1236,1254`; `db.go:867-870` |
| G15 | H | Scale | Rate limiter is per-process unless Redis configured; fails **open** to in-memory on Redis outage; N replicas ⇒ ~N× limits | `limiter.go:32-38,57-66,136-141,165-170` |
| G16 | H | Reliability | Upstream client: single 15-min timeout; no dial/TLS/response-header timeouts; no mid-stream idle detection (silent stall pins a goroutine for 15 min) | `handler.go:20-27` |
| G17 | M | Performance | 4–5 serialized DB round-trips per request incl. `SUM(cost)` over `request_logs` per budget window — hot path is DB-bound and degrades as the table grows | `limiter.go:82-141`; `db.go:315-335,923-942` |
| G18 | M | Performance | `request_logs` unbounded (no retention/partitioning); dashboard runs whole-table `COUNT/SUM/AVG` | `db.go:1111-1124` |
| G19 | M | Reliability | Migrations run on every boot with no advisory lock and no per-file transaction — concurrent replica cold-start can double-apply / crash | `db/migrations.go:45-74`; `db.go:176` |
| G20 | M | Scale | Redis limiter check-then-commit across two pipelines (TOCTOU) — bursts exceed RPM/TPM | `limiter.go:154-221` |
| G21 | M | Reliability | In-memory limiter map never evicts — unbounded growth with key churn | `limiter.go:68-80` |
| G22 | M | Privacy | Full request body (user code/prompts) logged on JSON parse errors; internal DB error strings returned to clients | `handler.go:389,573,318,449`; `admin/handlers.go` (err.Error() throughout) |
| G23 | M | Security | `GET /v1/models` is unauthenticated and does a DB `ListModels` per hit | `handler.go:225-228,1271` |
| G24 | M | Reliability | No panic-recovery middleware (stdlib per-conn recover only; streams truncate with no log/request ID) | `main.go:352-415` (chain) |
| G25 | M | Hygiene | 12 MB Linux binary `gateway` is git-tracked with `* text=auto` (corruption risk); no `.dockerignore` (build context ships binaries/zips/db); stale SQLite artifacts in root | `.gitattributes:2`; `Dockerfile:11`; repo root |
| G26 | M | Deployment | Docker: mutable base tags, container runs as root, `go build ./main.go` single-file build is fragile | `Dockerfile:2,15,18` |
| G27 | L | Protocol | Extra usage/meta chunk emitted **after** `data: [DONE]` for `X-Client-App: MuhiyaChat` (strict SSE clients would choke) | `handler.go:860,1702-1729` |
| G28 | L | Billing | Missing upstream usage falls back to a word-count token estimate with no `estimated` flag on the row | `handler.go:841-849,1476-1517` |
| G29 | L | Security | `/tools/web_search` body read unbounded (`io.ReadAll`, no `MaxBytesReader`) | `handler.go:252` |

**Verified solid (do not change)**: verbatim SSE passthrough incl. dual cache-token dialects
(`handler.go:815`, `translator.go:87-99`); pre-stream-only failover (no duplication,
`router.go:249-296`); context-propagated client aborts; single pooled upstream client;
parameterized SQL everywhere; cache-aware cost math (`handler.go:1519-1534`); request logs
store **no prompt content**; readiness gate + DB watchdog; constant-time admin compare;
provider-key redaction in the admin API.

---

## 4. Deep prompt-caching audit

### 4.1 How caching actually works in this pipeline

DeepSeek's implicit prefix cache operates on the **rendered prompt** (post chat-template,
including however the template renders `tools`), in 64-token blocks, **per upstream model**.
The gateway holds no cache and injects nothing on the MuhiyaCode path; its cache
responsibility is purely **transformation fidelity + model-namespace stability**.

### 4.2 Verdicts on the two live signatures (from MuhiyaCode benchmark evidence)

Both are **DeepSeek chat-template behavior faithfully passed through** — the gateway neither
causes nor can unilaterally prevent them:

1. **`tool_choice:"none"` shrink** — the gateway forwards `tools` + `tool_choice` verbatim
   (`translator.go:61-62`); nothing drops the tools array on `"none"`
   (`handler.go:754-772` contains no such logic). The ~500–900-token shrink with divergence in
   the latter prompt region ⇒ DeepSeek's template omits the rendered tool block when
   `tool_choice=none`. *(Client already fixed: `ToolChoice:"auto"` is now constant —
   `MuhiyaCode internal/orchestrator/engine.go:578`.)*
2. **Mid-task user-message plateau** — the gateway preserves message order and injects
   nothing; the evidence geometry shows DeepSeek anchors the rendered tool block **adjacent to
   the last user message**. A new user-role message mid-task (MuhiyaCode's `[governor]`
   notices) relocates the block; every rendered byte after the old anchor changes; reads
   plateau until the task ends. *(Note: "anchored to last user message" is an evidence-based
   inference from the shrink geometry and plateaus, not documented DeepSeek behavior — the
   §7 A-2 experiment verifies it.)*

### 4.3 Gateway-side cache risks (in impact order)

1. **G1 — the router is the largest gateway-side cache lever.** With the seeded catalog, the
   only discriminating router input is the thinking flag: effort < medium ⇒ `deepseek-chat`,
   effort ≥ medium (or a client `thinking` object) ⇒ `deepseek-reasoner`
   (`router.go:105,136-144`; `thinking.go:120-122`). A mid-session `/reasoning` change through
   `muhiya-ai-router` therefore **switches upstream models and wipes the entire prefix
   cache**. Routing runs per request with no memory.
2. **G2/G3/G4 — the lossy struct hop breaks the byte contract.** The client guarantees its
   request N+1 extends request N byte-for-byte; the gateway then re-defines the upstream
   body. Today the redefinition is deterministic (so not a per-request bust), but it (a)
   silently drops behavior-relevant params, (b) strips the `reasoning_content` protocol, and
   (c) is one refactor away from becoming nondeterministic. Fidelity is a cache-correctness
   precondition, not a nicety.
3. **G5** — spurious `thinking`/`reasoning_effort` on `deepseek-chat` risks 400s (a 400 forces
   client-side repairs/retries — indirect cache churn) and is simply wrong.
4. **G6** — dormant tier-drift landmine; becomes live the moment any model gets a real
   `routing_tier`.
5. **G7/G8** — MuhiyaChat-path busts (tool block toggling per iteration; runtime-editable
   system injection). Not on MuhiyaCode's path but same product.

### 4.4 Comparison with the Reasonix approach

Reasonix achieves ~99% by making every rendered byte position-stable on the **client**:
compose-once prefix, append-only history, session-pinned tool surface, single model per
session, params never mutating messages. The gateway equivalent of each invariant:

| Reasonix invariant (client) | Gateway equivalent | Status |
|---|---|---|
| Byte-identical prefix per request | Forward client bytes faithfully; deterministic transform | Deterministic ✅ / faithful ❌ (G2–G4) |
| Single model per session | Model-namespace stability per conversation | ❌ router (G1) |
| Session-pinned tool surface | Never add/drop/reposition tools or tool_choice | ✅ passthrough (client's duty) |
| Params never touch messages | Param mapping must not inject/alter message content | ✅ (thinking mapper touches only top-level fields) |
| Attributable invalidation | Surface upstream cache counts verbatim | ✅ (`handler.go:815`) |

### 4.5 The cache-safe transformation contract (adopt as gateway policy, enforce with tests)

1. Decode the client body **once** into `map[string]any`; mutate only: `model` (→ target),
   strip gateway-only keys (`web_search`), and apply the thinking mapping; re-encode **once**
   with `json.Encoder.SetEscapeHTML(false)`. No closed typed struct on the passthrough path.
2. The `messages` array is forwarded **verbatim** (all keys preserved, order untouched).
3. The upstream **model is pinned per conversation** — never re-selected by effort,
   complexity, or router re-runs mid-session.
4. `tools`/`tool_choice` forwarded verbatim; the gateway never adds, drops, reorders, or
   rewrites them.
5. No timestamps/UUIDs/randomness/session markers anywhere in the forwarded body (already
   true — keep it tested).
6. Generation params (`reasoning_effort`, `thinking`, sampling) live at top level only and
   are stable given stable client input.

---

## 5. Required gateway and MuhiyaCode integration changes

### Gateway (implemented by Sonnet 5.0 — details per finding in §3, plan in §6)

1. **G1 router stickiness**: pin the routed model per conversation. Mechanism: honor a client
   session header (`X-Muhiya-Session: <opaque id>`) as the stickiness key — memoize
   `session→model.ID` (in-process LRU + optional Redis when configured, TTL ~24 h) and reuse
   it for every subsequent `muhiya-ai-router` request in that session; absent the header,
   fall back to hashing the first user message. Never let effort/thinking re-route an
   existing session (log a warning instead; params may still change).
2. **G2/G3/G4 faithful forwarding**: raw-map passthrough per §4.5 items 1–2. Keep a small
   typed *view* (model, stream, tools-presence, effort) for routing decisions only.
3. **G5**: gate DeepSeek thinking injection on reasoner-class targets only.
4. **G6**: complexity from the first user message only (prep for any future tiering), and
   always subordinate to stickiness.
5. **Security/reliability set**: G10, G11, G12, G13, G14, G16, G23, G24, G29 as specified in
   §3 — these don't move cache numbers but block production use.

### MuhiyaCode (hand to the client implementer; complements its landed F1–F5 fixes)

1. **Governor/steering notices must stop being mid-task user-role messages.** DeepSeek
   anchors the tool block to the last user message, so each mid-task user-role notice
   relocates it (the plateau signature). Change `internal/orchestrator/engine.go:505-513` to
   emit governor notices as `role:"system"` messages (verify via the §7 A-2 experiment that
   mid-history system messages don't themselves relocate the anchor; if they do, fold the
   notice text into the *next* request's tail differently — e.g., prepend to the following
   assistant-visible tool result content — and document the residual one-bust-per-steer).
2. **Pin a concrete model per session; treat `muhiya-ai-router` as incompatible with
   cache-sensitive sessions** until Gateway G1 ships; after G1, send `X-Muhiya-Session` with
   the session ID on every request.
3. **Do not change `/reasoning` mid-session when targeting the router** (post-G1 the gateway
   will hold the model; the param still changes freely).
4. **After Gateway G2/G3 ship**, the client's empty `reasoning_content` keys will start
   reaching DeepSeek. This is a one-time rendered-prompt change at deploy time (expect one
   cache re-warm), then stable. Coordinate the deploy with a note in the session log.

---

## 6. Prioritized implementation plan (for Sonnet 5.0)

Each phase is independently shippable; do not reorder across phases without cause.

- **Phase 0 — same-day security stops (G10, G11, G23):** delete the seeded key from
  `setup_database.sql` (and revoke it in any live DB: `UPDATE virtual_keys SET
  status='revoked' WHERE id='key-admin-test-12345'`); make the server refuse to start with
  the default admin password outside an explicit `DEV_MODE=1`; require auth on `/v1/models`
  (or serve a 60s in-memory snapshot).
- **Phase 1 — cache outcome (G1, G2, G3, G4, G5):** faithful raw-map forwarding; router
  stickiness + `X-Muhiya-Session`; reasoner-gated thinking injection. Add the §4.5 contract
  as table-driven tests (golden upstream bodies; byte-prefix assertions across simulated
  turns).
- **Phase 2 — reliability (G12, G16, G14, G24, G29):** explicit `http.Server` with
  header/idle timeouts + signal-driven graceful shutdown draining streams; upstream transport
  timeouts + 60s stream-idle watchdog; checked + retried (outbox) billing writes; recovery
  middleware; bounded web-search body.
- **Phase 3 — scale & performance (G15, G20, G17, G18, G21, G19):** Redis-mandatory flag for
  multi-instance; Lua-atomic limiter; cached plan/user/budget definitions + rolling spend
  counters; request_logs retention/partitioning + bounded dashboard queries; limiter-map
  eviction; advisory-locked transactional migrations.
- **Phase 4 — hardening & hygiene (G13, G22, G25, G26, G27, G28, G6–G9):** hashed virtual
  keys + encrypted provider keys; log/PII scrubbing + generic client errors; repo/Docker
  hygiene; pre-`[DONE]` meta chunk; `usage_estimated` flag; first-message complexity;
  MuhiyaChat loop stability (tools constant per loop, guidance snapshot per conversation).

---

## 7. Acceptance criteria and verification steps

**Global regression gates (run after every phase):** `go vet ./...`,
`go test ./... -count=1`, `go test -race ./proxy/ -count=1`, plus the live A-1 probe below.

### A. Cache verification (Phase 1 exit criteria)

- **A-1 Passthrough fidelity**: capture the exact upstream body (add a
  `DEBUG_DUMP_UPSTREAM=1` temp env or a httptest upstream) for a scripted 10-turn session.
  Assert: every client message key survives (incl. `reasoning_content`, `stop`,
  `response_format`); request N's `messages` serialization is a byte-prefix of request
  N+1's; no `<>&` escaping differences vs client bytes.
- **A-2 Anchor experiment (settles the DeepSeek template inference)**: three live runs
  against DeepSeek, same 6-turn session: (a) control; (b) insert one extra `user` message
  mid-task; (c) insert one extra `system` message mid-task. Compare
  `prompt_cache_hit_tokens` trajectories: (b) reproduces the plateau; if (c) does not, the
  MuhiyaCode governor-notice role change is confirmed safe — record both outcomes in this
  file's changelog.
- **A-3 Router stickiness**: with `muhiya-ai-router` + `X-Muhiya-Session: test1`, send 5
  requests alternating `X-Muhiya-Effort: low`/`high`. Assert all 5 hit the same upstream
  `model` (log assertion), and a warning is logged for the ignored re-route.
- **A-4 End-to-end rate**: rerun MuhiyaCode's `benchmarks/cachebench` (3 runs) through the
  patched gateway with a pinned model. Acceptance: `prefix_stability_rate ≥ 0.99`
  steady-state; zero `agent-suspect` records attributable to the gateway (no prompt-shrink
  events while the client history is append-only).
- **A-5 Thinking gating**: unit test — `deepseek-chat` upstream body contains **no**
  `thinking`/`reasoning_effort` keys; `deepseek-reasoner` body contains both with the mapped
  ladder (`low/medium→high`, `high/max→max` per `thinking.go` mapping).

### B. Security (Phase 0/4)

- `setup_database.sql` contains no `INSERT INTO virtual_keys`; fresh install boots with zero
  active keys; `curl -H 'x-api-key: key-admin-test-12345' /v1/chat/completions` → 401 on a
  patched live deployment.
- Start with `ADMIN_PASSWORD` unset and no `DEV_MODE` → process exits non-zero with a clear
  message. `GET /v1/models` without a key → 401 (or cached, DB-free 200 if the snapshot
  option was chosen — state which).
- After G13: `virtual_keys` stores only hashes (verify by inspection); creating a key returns
  the plaintext once; auth still passes (integration test); provider `api_key` column
  ciphertext round-trips.

### C. Reliability (Phase 2)

- `kill -TERM` during an active SSE stream: stream completes (or drains ≤ deadline), its
  `request_logs` row exists, process exits 0. Slowloris probe (open socket, send headers
  byte-per-second) is cut at the header timeout.
- Point the gateway at a mock upstream that accepts the connection then sends nothing:
  request fails at ~60s (idle watchdog), not 15 minutes; goroutine count returns to baseline.
- Stop Postgres for 10s mid-traffic: requests still stream; billing writes queue and flush
  on recovery (outbox row count returns to 0); an ERROR log with request IDs exists for the
  window.

### D. Scale/perf (Phase 3)

- Two gateway instances + Redis: aggregate RPM across both ≤ configured limit (load test);
  with `REQUIRE_REDIS=1` and no `REDIS_URL`, refuse to start.
- Burst 50 parallel requests at a 10 RPM key through the Lua limiter: ≤ 10 succeed.
- p50 gateway-added latency on the hot path measured before/after definition-caching shows
  the DB round-trips per request reduced to ≤ 1 (spend counter) and no `SUM(request_logs)`
  in the request path (verify via `pg_stat_statements`).

### E. Protocol/billing (Phase 4)

- Strict SSE client test: no data frames after `[DONE]` for any `X-Client-App`.
- Kill the upstream before its usage chunk: the logged row has `usage_estimated=true` and
  the estimate; normal rows have it false.

---

*Audit complete. No repository files other than this document were created or modified.*
