# MuhiyaLLM Gateway ↔ Muhiya Platform — Integration Contract

This document is the **single source of truth for the seam** between the Go
gateway (GW, this repo) and the Next.js platform (MP, `muhiya-marketplace`).
Read it before changing anything that crosses the boundary. It exists to prevent
the silent drift that a two-repo money system invites.

Integration is **strictly one-directional: MP → GW.** The gateway never calls
back into the platform. Every sync is the platform pushing state to the gateway
over HTTPS.

---

## 1. The seam inventory

| Seam | MP side | GW side |
|------|---------|---------|
| **Key issuance** | `app/api/keys/{init,generate,rotate,reveal,delete}` capture the plaintext `key` (`sk-virt-…`) returned once by `createKey`, hand it to the user, and store the GW handle in `api_keys.gateway_key_id`. | `POST /api/keys` returns `{id, key}` once; auth is `hash(key)`. |
| **Plan sync** | `transitionPlan` (`app/lib/subscription.ts`) → `proxyClient.ensureUserExists` on every grant. | `POST/PUT /api/users` (`plan_id` = plan slug). |
| **Credit top-ups** | `proxyClient.createUserTopup(userId, credits, idemKey)`. Only Part F/G grants call this today. | `POST /api/users/topups` → `user_topups` (idempotent on `idem_key`, migration 013). |
| **Usage reads (admin)** | `/api/usage/*`, admin dashboards. | `GET /api/logs`, `GET /api/stats` (admin/service auth). |
| **Usage reads (self)** | — (MuhiyaCode CLI, not MP) | `GET /v1/usage` (key auth). |
| **Health** | dashboards degrade if GW down. | `GET /health`. |

---

## 2. Single sources of truth (do not duplicate the authority)

- **API credit balance = GW `user_topups` ONLY.** Supabase `wallet_balance` is a
  storefront wallet, **not** API credit. Any flow that sells or grants API
  credit MUST call `proxyClient.createUserTopup` with an idempotency key. (I8)
- **Budget enforcement = GW `budget_windows` ONLY.** MP `plans.budget_usd` is for
  display/ledger; it is never the enforced ceiling. MP does not push budgets to
  GW automatically. (I2)
- **1 credit = $0.01**, both sides (`db/db.go` `creditsPerUSD = 100.0`;
  `app/lib/credits.ts` `USD_PER_CREDIT = 0.01`). Keep them identical. (I4)
- **Gateway base URL** = `GATEWAY_API_URL` exported from `app/lib/proxy-client.ts`.
  All MP server routes import it; never re-derive. (I12)
- **`X-Client-App` gate value** = `"MuhiyaChat"` (GW `agent.go`; MP `chat/messages`).
  If you change it, change both. (I11)

---

## 3. Authentication

- **Admin credential** (`ADMIN_USERNAME`/`ADMIN_PASSWORD`): full access to `/api/*`
  and the HTML admin panel.
- **Scoped service credential** (`SERVICE_USERNAME`/`SERVICE_PASSWORD`, optional,
  migration-free): when set, accepted **only** for the platform's endpoints
  (`/api/users*`, `/api/keys*`, `/api/logs`, `/api/stats`, `/api/plans`,
  `/api/settings`, `/api/health`) and **never** the HTML panel. (I3/T212)

  **To adopt (owner):** set `SERVICE_USERNAME`/`SERVICE_PASSWORD` on the GW, then
  point MP's `ADMIN_USERNAME`/`ADMIN_PASSWORD` env at the service credential. The
  human admin credential then stops living in MP's environment. Rotate either
  credential independently. Until the owner sets these, behaviour is unchanged.

---

## 4. Idempotency & failure modes

- **Top-ups are idempotent** on `idem_key` (migration 013). Gift-card redemption
  uses `gift:<card_id>`; manual-payment ledger uses `manual:<payment_id>`. A retry
  or double-click can never double-credit.
- **`fetchProxy`** (MP `proxy-client.ts`) applies a 10s timeout + one retry on
  transient failures and throws a typed `GatewayError` (`status = 0` ⇒ GW
  unreachable, `retriable` ⇒ timeout/network/5xx). Callers should surface GW-down
  as a 503-with-retry, **never a fake success**. (I15)

---

## 5. Known drift risks & owner decisions (documented-as-intended)

These are audited and intentionally left as-is or pending an owner decision.
They are safe today; the note is the guard against future drift.

- **I1 — plan-slug→GW mapping** is a static 2-entry map (`mapPlanId`:
  `yalla-annual→yalla`, `max-annual→max`). It is correct for current plans. If
  plans are ever renamed, either extend the map or promote it to a
  `plans.gateway_plan_slug` column and have `mapPlanId` read it.
- **I7 — TPM marketing vs enforced.** Some plans advertise "unlimited tokens/min"
  while GW enforces 1.2M/2.5M TPM (`limiter.go`). Recommended: correct the
  marketing copy + `plans.tpm_limit` to the enforced values (honesty; enforcement
  wins). Owner decision — not auto-changed.
- **I9 — `api_usage_logs` is a dead ledger** with no writer, so
  `transitionPlan.oldSpend` and the admin 30-day spend read ~0. Real usage lives
  in GW `request_logs`. Fix path: read spend via `GET /api/logs`/`/api/stats`
  instead. The table stays for now (schema-history stability).
- **I10 — LiteLLM leftovers.** Historical migrations reference LiteLLM endpoints
  that no longer exist; `budget_duration`/`key_budget_duration` columns are unused
  by code. Harmless; left for schema-history stability.
- **I14 — window re-anchor.** GW budget windows re-anchor only on a plan CHANGE,
  not a same-plan renewal. Rolling windows are self-correcting; left as-is.
- **I13 — secret hygiene.** `.env.local` values that were ever committed must be
  rotated by the owner. Names are reconciled with `.env.example`; values are not
  printed here.

---

## 6. When you add a flow that grants API credit or a plan

1. Plans → call `transitionPlan` (never hand-write `subscriptions`). It seeds the
   reset counters, writes the `budget_events` ledger, and syncs GW.
2. Credits → call `proxyClient.createUserTopup(userId, credits, idemKey)` with a
   stable idem key derived from the source row id.
3. On GW failure, surface a retryable error — do not report success.
