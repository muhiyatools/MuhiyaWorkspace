# Three-Side Latency and Reliability Plan — 2026-08-02

> **EXECUTION STATUS (2026-08-02).**
> **Done:** Part 1 (reasoning-token + first-byte instrumentation, migration 039),
> Part 3 (live Reasoning/Writing phase + tok/s), Part 4.1 (connection pooling),
> Part 4.3 (verified — see note), Part 6.2 (timeout-pairing test). Both Go suites
> green; `gateway.exe` and `muhiyacode.exe` rebuilt.
>
> **Deliberately not done, with reasons:**
> - **2.2 (default effort High→Medium)** — `EffortHigh` also sets `MaxTurns: 56`
>   vs Medium's `32`. Flipping the default would silently halve the task turn
>   budget, which is a completeness regression, not a speed/quality trade. Needs
>   an explicit decision; the reasoning half of the win is already available via
>   `/effort medium`, which now maps to DeepSeek `low`.
> - **2.3 / 2.4 (output cap, turn granularity)** — blocked on Part 1 data by this
>   plan's own sequencing. Nothing is deployed yet, so no reasoning-token
>   baseline exists to measure them against.
> - **4.2 (gateway catalog cache)** — measured upside is ~30-50 ms against a
>   65 ms handler, against a real staleness risk after admin edits. Poor trade
>   until something else makes it matter.
> - **5.1 (cache-rate CI gate)** — needs the live benchmark harness and real API
>   credits; cannot be built offline.
>
> **Part 4.3 verified from existing data rather than a new probe:** the 311-second
> turn completed without tripping MuhiyaCode's 110 s idle watchdog, which resets
> on every SSE frame. Frames therefore arrived continuously for five minutes —
> proof that nothing between DeepSeek, the gateway and the client is buffering the
> stream. No nginx change needed.

Scope: **MuhiyaCode Agent** (`F:\MuhiyaCode Agent Go`) → **Muhiya Gateway**
(`F:\MuhiyaWorkspace\MuhiyaWorkspace`) → **DeepSeek**, plus the MuhiyaChat path on
the platform (`C:\Users\mydwa\muhiya-marketplace`).

Written after measuring the live system rather than estimating it. Every number
below is from the production gateway on 2026-08-02.

---

## Part 0 — The finding that decides the whole plan

The reported symptom was "MuhiyaCode reads a few files then thinks for minutes and
does nothing." The session it referred to **completed successfully**. Per-turn
measurements from `/api/logs`:

| time | in | cache read | hit | out | latency | tok/s |
|---|---|---|---|---|---|---|
| 13:28:49 | 5,445 | 768 | 14% | 410 | 4.8s | 86 |
| 13:28:54 | 6,865 | 5,760 | 84% | 208 | 2.4s | 88 |
| **13:28:57** | **7,295** | **7,040** | **97%** | **27,255** | **311.9s** | **87** |
| 13:34:09 | 34,992 | 34,432 | 98% | 6,277 | 35.0s | 180 |
| 13:34:44 | 45,884 | 41,216 | 90% | 6,170 | 34.4s | 179 |
| 13:35:19 | 55,762 | 51,968 | 93% | 5,663 | 43.2s | 131 |
| 13:36:03…32 | 63k–68k | 97–100% | | 963/642/880/508/809 | 5–9s | 93–124 |

**11 turns · 467s total model time · 49,785 output tokens · mean 106 tok/s.**

The "hang" was one turn generating **27,255 tokens at 87 tok/s** — the turn that
wrote `tasks.md`, `index.html`, `styles.css` and `app.js` in a single response.
311 seconds is exactly what 27k tokens costs at that rate. Nothing was stuck.

Now the transport, measured five times against the production gateway:

| leg | measured | share of that 311s turn |
|---|---|---|
| TCP connect | 72–83 ms | 0.03% |
| TLS handshake | +70–76 ms | 0.02% |
| Gateway handler + Postgres query | ~65 ms | 0.02% |
| **DeepSeek generation** | **311,900 ms** | **99.93%** |

### What this means

**The MuhiyaCode ↔ Gateway leg is already ultra-low-latency.** Round trip including
a database query is ~65 ms on a warm connection, ~215 ms cold. There is no
meaningful latency to reclaim there: perfect transport tuning would cut a 311-second
turn to 310.8 seconds.

Cache is also already near-optimal on this path — 97–100% hit on every steady-state
turn. There is nothing left to win there either.

**So this plan does not chase transport or cache.** Wall-clock time on this stack is
almost entirely *tokens generated × seconds per token*, and the only levers that move
it are:

1. **Generate fewer tokens** — especially invisible reasoning tokens (§2).
2. **Make the unavoidable wait legible** — the user could not tell thinking from
   hanging, which is the actual reported problem (§3).
3. **Remove the residual per-turn overhead** — irrelevant on a 311s turn, but 5–8%
   of a 2.4s turn, and most turns are short (§4).

Anything promising "ultra-low latency" by tuning the wire on this workload would be
selling a 0.07% improvement.

---

## Part 1 — Instrument before optimizing (blocking; do this first)

We currently **cannot answer the single most important question**: of those 27,255
output tokens, how many were reasoning the user never saw?

DeepSeek reports `completion_tokens_details.reasoning_tokens` inside
`completion_tokens`. The gateway does not parse it — `grep -r reasoning_tokens`
across the gateway returns nothing. MuhiyaCode has the field in
`internal/contract/cache.go` and its benchmark harness, but the gateway's
`request_logs` — the only cross-client record — drops it.

**1.1 Gateway: capture reasoning tokens.**
Add `CompletionTokensDetails.ReasoningTokens` to `OpenAIUsage` (`proxy/translator.go`),
persist it as `request_logs.reasoning_tokens` (migration 039), and surface it on the
`muhiya_log` meta chunk. Every later decision in this plan is measured against it.

**1.2 Gateway: capture time to first byte.**
Record `first_token_ms` — upstream request sent → first SSE data frame forwarded.
That separates *provider queue + prefill* from *generation*, which today are one
opaque `latency_ms`.

**1.3 MuhiyaCode: record TTFT and the largest inter-frame gap** per attempt in the
execution journal (the `SANDBOX_AND_STREAM` plan's Phase 3 already calls for this).
This is what lets us set timeouts from data instead of by feel (§6).

**Acceptance:** for any request id, we can state reasoning tokens, visible tokens,
time to first byte, and total generation time.

---

## Part 2 — Cut generated tokens (the only lever on the 99.9%)

**2.1 Deploy the effort-ladder fix.** *(code written, NOT deployed)*
DeepSeek's ladder is `low|high|max`. The gateway mapped **medium → high**, so
MuhiyaCode's balanced setting bought the deepest non-max reasoning mode. Fixed in
`proxy/thinking.go` to resolve medium **down** to `low`. **The production gateway at
`api.muhiya.com` is still running the old build — this is live only after deploy.**

**2.2 Change MuhiyaCode's default effort from High to Medium.**
`internal/state/config.go:22` sets `settings.Effort = contract.EffortHigh`, so every
default session runs DeepSeek at `reasoning_effort: high`. With 2.1 deployed, Medium
becomes a genuinely cheaper rung (DeepSeek `low`) rather than a relabelled High.
Expected: the largest single reduction in reasoning tokens available to us.
Measure with 1.1 before/after on a fixed task; keep High one keystroke away.

**2.3 Bound the per-turn output.**
MuhiyaCode omits `max_tokens` (`taskRequestPolicy` returns 0), so DeepSeek applies its
own 64K default — visible in the 2026-08-02 10:25 turn that terminated at exactly
65,536 output tokens after 590 seconds. Omitting the key was the right fix for the old
bug (the gateway priced the full requested ceiling against the balance and silently
shrank it), but "unbounded" is not the only alternative to "384,000".

Send a **client-chosen, task-class-sized** cap (proposal: 16k small / 32k medium /
64k max). The gateway's `reasoningVisibleFloor` (8,192) already refuses a budget too
small to answer in, so a modest explicit cap is now safe. A bounded turn fails in one
minute with a clear message instead of running ten.

**2.4 Reduce turn granularity.**
One turn wrote four files and cost 27k tokens and 5 minutes. Four turns of ~7k tokens
each cost the same total but produce visible progress every ~80 seconds, and a failure
costs one file instead of the whole set. Add guidance in the instruction set to emit
one substantial file per turn when creating several.

**Acceptance:** on a fixed benchmark task, reasoning tokens per turn drop measurably
(2.1+2.2), no turn exceeds its class cap (2.3), and no single turn exceeds ~10k output
tokens (2.4) — with task success rate unchanged.

---

## Part 3 — Perceived latency (largest UX win, no provider change)

The literal report was *"I don't know even if it's really thinking or not."* A 311-second
turn that shows only `Thinking · 1m 25s` is indistinguishable from a hang, and that is
a fixable product defect independent of how fast the model is.

**3.1 Three distinct states, driven by real stream events**
`Connecting…` → `Reasoning` (first `reasoning_content` delta) → `Writing` (first
`content` delta). This is Phase 4 of `SANDBOX_AND_STREAM_REMEDIATION_PLAN_2026-08-02.md`,
still unimplemented. It requires no reasoning text to be rendered — only the fact that
frames are arriving.

**3.2 Live throughput**
Show output tokens and tok/s (`Writing · 12,480 tokens · 87 tok/s`). At 87 tok/s a user
watching the counter climb knows the system is healthy; a frozen counter is a real
stall. This alone would have prevented the report.

**3.3 Rate-limit the UI updates** to ~4/s so a token stream does not become a render
storm.

**Acceptance:** a 300-second turn is visibly distinguishable from a stalled one at a
glance, at all times, without exposing reasoning text.

---

## Part 4 — Residual per-turn overhead

Immaterial on long turns; 5–8% on the 2–9 second turns that make up most of a session.

**4.1 Give MuhiyaCode's provider a tuned HTTP client.**
`internal/gateway/provider.go:79` falls back to a bare `&http.Client{}` on
`http.DefaultTransport`: `MaxIdleConnsPerHost: 2`, `IdleConnTimeout: 90s`, no
`ResponseHeaderTimeout`. A gap longer than 90s between turns — routine while a tool
runs or the user reads — drops the pooled connection and costs a fresh TCP+TLS
(measured **~150 ms**) on the next turn. Set `IdleConnTimeout` above the realistic
inter-turn gap (5 min), raise `MaxIdleConnsPerHost`, keep HTTP/2, and set explicit
dial/TLS bounds mirroring the gateway's.

**4.2 Cut admission database round trips in the gateway.**
Each request resolves virtual key → model → provider → budget → rate limit. The model
and provider rows change only on an admin write: cache them in memory with a short TTL
and invalidate on write. Budget and rate limit must stay live. Measured handler+DB
today is ~65 ms; the target is the network floor of ~73 ms round trip.

**4.3 Verify no SSE buffering at the edge.**
`api.muhiya.com` is behind Elestio (nginx). Confirm `proxy_buffering off` and
`X-Accel-Buffering: no` on the streaming path — a buffering proxy would hold frames
and make a healthy stream look stalled. The 13:36 turns returning in 5–9 s indicate
frames flow, but this must be asserted, not assumed.

**Acceptance:** warm-connection turn overhead ≤ 100 ms end to end; no cold TCP+TLS
between consecutive turns of one session.

---

## Part 5 — Cache: protect what is already excellent

MuhiyaCode runs 84–100% cache read (mostly 97–100%). **Do not touch the prefix
construction on this path.** The remaining work is defensive:

**5.1 Regression gate.** `internal/orchestrator/cachehit_guard_test.go` exists; extend
it to fail CI if the session-mean hit rate on the fixture workload drops below 90%.

**5.2 MuhiyaChat window anchor.** The compose-once fix (persisting the per-turn tail on
the message it was sent with) shipped today and makes short chats near-optimal. Long
chats still slide their history window one exchange per turn, which moves the prefix
start and busts the cache every turn. Needs a persisted anchor so the cut point moves
in blocks. Only affects conversations past the token budget or 50 messages.

**5.3 Cold-resume.** `cacheresilience.go` already flags resume cold starts. Once 1.1
lands, confirm the flagged events line up with the measured misses.

---

## Part 6 — Timeouts and reliability, derived from data

**6.1 Re-derive the 110s idle bound.** `provider.go:88` sets `IdleTimeout = 110s`
against the gateway's 120s, so the client — the side that can recover — times out
first. That pairing is correct and must be preserved. Whether 110s is the right
*value* is unknown until 1.3 gives us the p99 inter-frame gap on real DeepSeek
traffic. Do not change it before then; a bound below a legitimate reasoning pause
converts working requests into retries, which cost a full re-run.

**6.2 Keep the pairing invariant explicit.** Add a test asserting
`client.IdleTimeout < gateway.streamIdleTimeout` and
`client.RequestLifetime < gateway.upstreamTotalTimeout`, so a future edit to either
repo cannot silently invert the recovery ownership.

**6.3 Handle `insufficient_system_resource`** as the retryable upstream capacity
failure DeepSeek documents it to be, on both sides.

---

## Part 7 — Deployment gap (must happen before any measurement is meaningful)

Fixes written today are **not live**:

| change | repo | state |
|---|---|---|
| medium → DeepSeek `low` | gateway | built + tested, **not deployed** |
| `muhiyachat_visible` (+ migration 038, auto-applies on boot) | gateway | built + tested, **not deployed** |
| empty `finish_reason=length` terminates instead of looping `[continue]` | MuhiyaCode | built + tested, **binary not rebuilt** |
| compose-once prompt cache | platform | built, needs `supabase db push` |

Deploy order: gateway (migration self-applies) → rebuild `muhiyacode.exe` →
platform + `npx supabase db push`.

---

## Sequencing

1. **Part 7** — deploy what exists, or nothing below is measurable.
2. **Part 1** — instrumentation. Blocking: without reasoning-token counts, Part 2 is guesswork.
3. **Part 3** — perceived latency. Cheapest real win, fixes the actual complaint, independent of everything else.
4. **Part 2** — token reduction, measured against the Part 1 baseline.
5. **Part 4** — overhead trimming.
6. **Parts 5–6** — protection and tuning from the collected data.

## Risks

- **2.2/2.3 trade reasoning depth for speed.** On a coding agent that can cost task
  quality. Both must be gated on a benchmark run showing success rate held, not on
  latency alone — `benchmarks/` already exists for this.
- **2.3 reintroduces `max_tokens`**, the parameter a previous audit removed. It is
  safe only because the gateway now refuses a sub-floor thinking budget; if that floor
  is ever removed, this must be revisited.
- **4.2 caching catalog rows** risks an operator's admin edit not taking effect for
  the TTL. Invalidate on write; keep the TTL short.
- **Part 3 must never render reasoning text** — that is a standing product decision.
