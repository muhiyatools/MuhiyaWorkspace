# DeepSeek Wiring Audit & Remediation Plan — 2026-08-02

> **STATUS: EXECUTED 2026-08-02.** All phases applied to both repos. Build, vet,
> staticcheck (0) and full test suites green on each; `gateway.exe` rebuilt.
>
> Phase 0 needed no data change: the live catalog was already correct
> (`deepseek-v4-flash` → `deepseek-v4-flash`, thinking on, 1M / 384k). The seed
> files and migration 036 were still fixed so a fresh deployment cannot
> reintroduce the retired alias.
>
> Findings P0-1 (seeds), P0-2, P0-3, P0-4, P1-5, P1-6, P1-7 and P2-9 are all
> resolved. P1-8 is resolved via the `X-Muhiya-Output-Limit` response header.

Scope: end-to-end DeepSeek path across **MuhiyaCode Agent** (`F:\MuhiyaCode Agent Go`)
and **Muhiya Go Gateway** (`F:\MuhiyaWorkspace\MuhiyaWorkspace`), checked against
DeepSeek's live API documentation as of 2026-08-02.

Reported symptoms:
1. Requests "keep thinking" and never produce output.
2. Reasoning text is billed/shown as output tokens.
3. The agent completes the first turns and then stops progressing.

All three are explained by the findings below. They are not one bug; they are four
independent defects that compound, plus a retired upstream model name.

---

## Part 0 — Ground truth from DeepSeek's documentation

Verified today against `api-docs.deepseek.com`:

| Fact | Value | Consequence for us |
|---|---|---|
| Current models | `deepseek-v4-pro`, `deepseek-v4-flash` | `deepseek-chat` / `deepseek-reasoner` **retired 2026-07-24 15:59 UTC** — 9 days ago |
| Thinking toggle (OpenAI dialect) | `{"thinking": {"type": "enabled"\|"disabled"}}` | Gateway shape is correct |
| Effort ladder | `reasoning_effort`: `low` \| `high` \| `max` | **v4-flash supports all three**; v4-pro is `high`/`max` only |
| Default effort | `high`, thinking enabled | We are escalating past the default |
| Thinking dialect (Anthropic endpoint) | `{"reasoning": {"effort": "none\|low\|high\|max"}}` | Gateway emits `output_config` — that is the **Responses API** shape |
| Reasoning output field | `reasoning_content`, sibling of `content` | Correct on both sides |
| Reasoning token accounting | `completion_tokens_details.reasoning_tokens` is **inside** `completion_tokens` | **`max_tokens` is consumed by reasoning** |
| `max_tokens` ceiling | 384,000 | Agent profile `OutputTokenLimit: 384_000` is correct |
| Context window | 1,000,000 | Migration 007 is correct |
| Ignored in thinking mode | `temperature`, `top_p`, `presence_penalty`, `frequency_penalty` — no-ops, no error | We still send two of them |
| **Tool calls + reasoning** | `reasoning_content` **must be passed back** in every later turn, or the API returns **400** | We send an empty string |
| `finish_reason` values | `stop`, `length`, `content_filter`, `tool_calls`, `insufficient_system_resource` | Last one is unhandled |

Sources are listed at the end.

---

## Part 1 — Findings

### P0-1 — `deepseek-v4-flash` targets a retired model name

`seed.sql:56`

```sql
('model-deepseek-flash', 'deepseek-v4-flash', 'deepseek', 'deepseek-chat', ...)
--                        ^ catalog name                   ^ target_model
```

`proxy/handler.go:1144` sends `bodyMap["model"] = model.TargetModel`, so the bytes
that reach DeepSeek say `"model": "deepseek-chat"`.

Two failures at once:

- `deepseek-chat` was **fully retired on 2026-07-24**. Every request is now hitting a
  model name DeepSeek no longer serves.
- Even while it lived, `deepseek-chat` was the **non-thinking** alias of v4-flash.
  Ticking `supports_thinking` on that row makes the gateway inject
  `thinking:{type:"enabled"}` at a non-thinking alias. A non-thinking model returns
  its whole chain of thought in `content` — **this is the most likely cause of
  symptom 2 ("shows the full thinking output as output")**.

No migration ever repairs `target_model`; migration 007 only touches
`context_window` / `max_output_tokens`. Unless the row was edited by hand in the
admin panel, it is still wrong.

**This must be verified first — it can invalidate the diagnosis of everything else.**

```sql
SELECT id, name, target_model, status, supports_thinking,
       context_window, max_output_tokens, muhiyacode_visible
FROM models
WHERE name LIKE 'deepseek%' OR target_model LIKE 'deepseek%'
ORDER BY name;
```

Expected for a working setup: `name = 'deepseek-v4-flash'`,
`target_model = 'deepseek-v4-flash'`, `supports_thinking = true`,
`context_window = 1000000`, `max_output_tokens = 384000`.

---

### P0-2 — The gateway force-injects `max_tokens`, then silently shrinks it to the remaining credit

This is the direct cause of symptoms 1 and 3.

`proxy/handler.go:885` runs **unconditionally**, whether or not the client sent a limit:

```go
bodyBytes, err = rewriteOpenAIOutputLimit(bodyBytes, allowedOutput)
```

and `rewriteOpenAIOutputLimit` (`handler.go:2601`) always writes the key:

```go
payload["max_tokens"] = limit
```

`allowedOutput` comes from `affordableGeneration` (`handler.go:2500`), which quotes the
**entire requested output** against the caller's available balance and, when it does
not fit, reduces it:

```go
allowed, maxErr := rules.MaxOutputTokensAt(..., available, request.pricedAt)
```

The agent asks for the full ceiling every turn — `taskRequestPolicy` returns
`activeModelMaxOutput()`, i.e. **384,000**. So on every single request the gateway
prices 384,000 output tokens against the remaining balance and, whenever that quote
exceeds the balance, quietly rewrites `max_tokens` down to whatever fits.

Because **reasoning tokens are counted inside `completion_tokens`**, a shrunken cap is
spent on `reasoning_content` before a single visible token is produced. The response
comes back as:

```
finish_reason: "length",  content: "",  tool_calls: []
```

The agent then does exactly what it is designed to do (`completion.go:12`): notices
`length` with no tool calls, emits *"The model's reply reached the output limit and was
cut off"*, injects `[continue]`, retries twice, and terminates. **That notice is the
"strange something related to output or token limit" from the screenshot** — it is the
symptom, not the disease.

This also explains why the first turns work and later ones do not: as the conversation
grows, `conservativeInputTokenBound` rises, the input side of the quote grows, and
`allowedOutput` shrinks turn over turn until reasoning alone exhausts it.

Three compounding faults here:

- The gateway invents a `max_tokens` the client never asked for. Omitting the key
  entirely is a legal request and lets DeepSeek use its own default.
- The reduction is **silent** — no header, no SSE notice. The agent cannot distinguish
  "you are out of credit" from "the model rambled".
- Nothing accounts for reasoning consuming the cap on a thinking model.

---

### P0-3 — Effort inflation: `high` is sent upstream as `max`

`proxy/thinking.go:298-308`:

```go
if rank >= 3 {
    bodyMap["reasoning_effort"] = "max"
    return "max"
}
bodyMap["reasoning_effort"] = "high"
```

The mapping table in the file header states *"DeepSeek supports only high|max"*. That
was true for the reasoner-era API. It is **no longer true for v4-flash**, which
supports `low`, `high`, and `max`.

The agent's default session effort is **High** (`effort.go:33-39` →
`contract.ReasoningHigh` → `X-Muhiya-Effort: high`), which the gateway ranks 3 and
therefore upgrades to `reasoning_effort: "max"` — the longest and most expensive
reasoning mode DeepSeek offers, on every turn, forever.

Combined with P0-2 this is lethal: maximum reasoning length against a
credit-shrunken output cap guarantees `finish_reason: length` with empty content.

Correct mapping for v4-flash:

| Requested | Current | Should be |
|---|---|---|
| minimal / off | `disabled` | `disabled` ✓ |
| low | `high` | **`low`** |
| medium | `high` | `high` ✓ |
| high | **`max`** | **`high`** |
| max | `max` | `max` ✓ |

v4-pro keeps the `high` floor (it genuinely supports only `high`/`max`).

---

### P0-4 — `reasoning_content` is blanked on tool-call turns

DeepSeek's documentation is explicit: **when `tools` are present, `reasoning_content`
must be passed back in every subsequent turn, or the API returns 400.**

MuhiyaCode is an agent. Every turn carries tools. And
`internal/gateway/provider.go:521-538` does this:

```go
if profile.ReasoningReplay != ReasoningReplayPreserve {
    result[index].ReasoningContent = nil
    result[index].ReasoningDetails = nil
}
if profile.Family == "deepseek" && result[index].Role == contract.RoleAssistant && len(result[index].ToolCalls) > 0 {
    empty := ""
    result[index].ReasoningContent = &empty
}
```

The DeepSeek profile is `ReasoningReplay: ReasoningReplayStrip`
(`internal/gateway/model.go:128`), so the real reasoning is discarded, then the key is
re-added as `""`.

The empty string may satisfy a presence check, but the content is gone. The model loses
its own reasoning chain across every tool call, so each turn re-derives the plan from
scratch. **That is the "makes first turns then doesn't continue" behaviour** — it is not
continuing, it is restarting.

The machinery to do this correctly already exists and is already exercised:
`assistantReplayMessage` (`completion.go:45`) attaches the real reasoning, and MiniMax
uses `ReasoningReplayPreserve` for exactly this purpose.

---

### P1-5 — The Anthropic path still carries the bug the OpenAI path already fixed

`proxy/thinking.go:455`:

```go
if classifyUpstream(baseURL, targetModel) == famDeepseek && isDeepseekReasoner(model) {
```

`isDeepseekReasoner` is the `strings.Contains(model, "reasoner") || "r1"` heuristic that
was replaced on the OpenAI path with the operator `supports_thinking` flag. `ApplyThinkingAnthropic`
never received the flag, so `deepseek-v4-flash` matches neither token and falls through
to `anthropicSupportsThinking` → `"unsupported"` → thinking silently stripped.

Second defect in the same block (`thinking.go:463-464`):

```go
bodyMap["thinking"] = map[string]interface{}{"type": "enabled"}
bodyMap["output_config"] = map[string]interface{}{"effort": effort}
```

`output_config` is the **Responses API** shape. DeepSeek's Anthropic-compatible endpoint
documents `{"reasoning": {"effort": "none|low|high|max"}}`.

Lower priority only because the agent uses the OpenAI dialect — but any Anthropic-format
client (Claude Code pointed at the gateway) hits it.

---

### P1-6 — The rate limiter charges the full requested output

`proxy/handler.go:830`:

```go
if err := h.limiter.CheckLimit(key, promptTokens+requestedOutput); err != nil {
```

With `requestedOutput = 384_000`, every request debits 384k tokens against the key's
token-per-window allowance regardless of what is actually generated. Any TPM limit
trips almost immediately.

---

### P1-7 — Sampling parameters are sent into thinking mode

`internal/gateway/request_body.go:27-38` sends `temperature: 0.1` and `top_p: 0.95`;
both are listed in `deepSeekSupportedParams`. DeepSeek documents both as **ignored in
thinking mode**. No error, but they are dead bytes in the cached prefix region and they
misrepresent what the model is actually doing.

---

### P1-8 — Credit-driven output reduction is invisible to the client

When `affordableGeneration` shrinks the cap there is no response header, no SSE meta
field, and no log line the agent can read. The agent's only observable is
`finish_reason: length`, which it correctly but uselessly reports as "the model rambled".

---

### P2-9 — `insufficient_system_resource` is unhandled

DeepSeek documents this `finish_reason`. Neither `turn_outcomes.go` nor the gateway
recognises it; it falls through as an ordinary completion, so a capacity failure is
presented to the user as a valid empty answer.

---

## Part 2 — Remediation plan

Ordered so that each phase is independently verifiable and the highest-value fix lands
first.

### Phase 0 — Verify the catalog row (blocking, no code)

Run the `SELECT` in P0-1. If `target_model` is `deepseek-chat` or `deepseek-reasoner`:

```sql
UPDATE models
   SET target_model      = 'deepseek-v4-flash',
       supports_thinking = TRUE,
       context_window    = 1000000,
       max_output_tokens = 384000
 WHERE name = 'deepseek-v4-flash';
```

Add a migration `036_deepseek_retired_aliases.sql` that repairs any row still pointing
at a retired alias and refuses to leave one behind, so a fresh deployment cannot
reintroduce it. Fix `seed.sql:55-56` and `add_base_models.sql` at the same time.

**Expected effect:** if this alone fixes it, symptoms 2 and 3 disappear immediately.

### Phase 1 — Stop the gateway inventing and silently shrinking `max_tokens`

1. `rewriteOpenAIOutputLimit` becomes conditional: rewrite only when the client
   actually sent `max_tokens` / `max_completion_tokens`, **or** when the budget forced a
   reduction below what the client would otherwise get. A request with no limit stays
   without one.
2. When a reduction does happen, emit it: a `X-Muhiya-Output-Limit` response header and
   a field on the existing `muhiya_log` SSE meta chunk, so the agent can say "you are
   near your credit limit" instead of "the model was cut off".
3. Reserve headroom for reasoning: when `supports_thinking` is set and thinking is not
   disabled, the admission quote must treat the cap as reasoning + content, and refuse
   with a budget error rather than issue a cap so small that the model cannot answer.
   A floor (proposal: 4,096 visible tokens) below which the request is rejected outright.

### Phase 2 — Correct the effort ladder

`ApplyThinkingOpenAI`, DeepSeek branch: map `low → low` for v4-flash, `high → high`,
`max → max`, keeping the `high` floor for v4-pro. Update the file-header table and
`deepseek_strip_test.go`, whose current matrix (`reasoner+high → max`) encodes the wrong
behaviour and will otherwise fail correctly.

### Phase 3 — Replay real reasoning on DeepSeek tool turns

Flip the DeepSeek profile to `ReasoningReplay: ReasoningReplayPreserve` and delete the
`empty := ""` special case in `provider.go:532`, so the block reduces to the general
rule. Preservation must be scoped to **assistant turns that carry tool calls**, which is
what the documentation requires; plain conversational turns can still be stripped to
keep the prefix small.

Cover it with a test asserting that a DeepSeek tool-call turn round-trips its actual
reasoning text, and that a tool-less turn does not.

### Phase 4 — Anthropic-path parity

Give `ApplyThinkingAnthropic` the `supportsThinking` flag (same signature change already
made to `ApplyThinkingOpenAI`), and emit `{"reasoning": {"effort": ...}}` for DeepSeek
instead of `output_config`.

### Phase 5 — Admission and hygiene

- Rate-limit against a realistic output estimate, not the 384k ceiling.
- Drop `temperature` / `top_p` from the DeepSeek request when thinking is enabled.
- Recognise `insufficient_system_resource` as a retryable upstream capacity failure in
  both repos.

### Phase 6 — Prove it on the wire

The one check that settles everything: capture the exact bytes the gateway sends to
DeepSeek for one agent turn, and the first response chunk. Assert `model`,
`thinking`, `reasoning_effort`, presence/absence of `max_tokens`, and that assistant
tool-call messages carry non-empty `reasoning_content`. This belongs in
`wire_goldens_test.go` next to the existing golden fixtures.

---

## Risk notes

- Phase 1.3 changes admission behaviour: some requests that previously ran with a tiny
  cap will now be rejected with a budget error. That is the correct outcome — a rejected
  request is honest, a cap that only fits the reasoning is not — but it is user-visible.
- Phase 3 increases request size and will shift the cached-prefix boundary on DeepSeek.
  Expect a one-time cache-hit dip.
- `deepseek_strip_test.go` currently asserts the P0-3 behaviour as correct. It must be
  updated deliberately, not deleted.

---

## Sources

- [Thinking Mode | DeepSeek API Docs](https://api-docs.deepseek.com/guides/thinking_mode/)
- [Create Chat Completion | DeepSeek API Docs](https://api-docs.deepseek.com/api/create-chat-completion)
- [DeepSeek V4 Preview Release | DeepSeek API Docs](https://api-docs.deepseek.com/news/news260424/)
- [Change Log | DeepSeek API Docs](https://api-docs.deepseek.com/updates/)
- [Your First API Call | DeepSeek API Docs](https://api-docs.deepseek.com/)
- [Streaming reasoning example | DeepSeek API Docs](https://api-docs.deepseek.com/api_samples/thinking_mode_api_example_streaming)
