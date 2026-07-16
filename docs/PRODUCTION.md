# Production Runbook — MuhiyaLLM Gateway + Platform

Covers the release that adds the money-path overhaul (Parts A–D), platform bug
fixes (E, incl. the critical key-auth fix I6), manual Vodafone-Cash payments (F),
gift cards (G), and integration hardening (H). Read `docs/INTEGRATION.md` for the
seam contract.

---

## 1. Database migrations — apply IN ORDER

### Gateway Postgres (owner applies manually)
```
db/migrations/010_credit_charge_watermark.sql
db/migrations/011_usage_reset.sql
db/migrations/012_topup_lifecycle.sql
db/migrations/013_topup_idempotency.sql
```
Apply 010→013 in sequence before deploying the new gateway binary. Each is
idempotent (`IF NOT EXISTS`). None drop data.

### Platform Supabase (already applied via migration tooling)
- `manual_vodafone_payments` — `manual_payments` table, `payment-proofs` private
  bucket, RLS, settings seed (`payment_gateway_manual`, `manual_payment_wallet_number`).
- `gift_cards` — `gift_cards` + `gift_card_batches`, RLS.

Verify (Supabase SQL): RLS is `true` on `manual_payments`, `gift_cards`,
`gift_card_batches`; `storage.buckets` `payment-proofs` is `public=false`.

---

## 2. Required environment (names only)

### Gateway
- `DATABASE_URL` — Postgres DSN.
- `ADMIN_USERNAME`, `ADMIN_PASSWORD` — admin panel + full `/api`. Required (no
  guessable default; boot refuses without it unless `DEV_MODE=1`).
- `SERVICE_USERNAME`, `SERVICE_PASSWORD` — **optional** scoped credential for the
  platform's `/api` surface (see INTEGRATION.md §3). Set these to stop shipping
  the human admin credential to the platform.
- `IDENTITY_SECRET`, provider API keys (DeepSeek etc.) — as today.

### Platform
- `GATEWAY_API_URL` — gateway base URL.
- `ADMIN_USERNAME`, `ADMIN_PASSWORD` — gateway credential the platform uses. Point
  these at the **service** credential once it is configured on the gateway.
- `SUPABASE_URL`, `SUPABASE_ANON_KEY`, `SUPABASE_SERVICE_ROLE_KEY` — as today.
- Manual payments: no new env — the wallet number and enable flag live in
  `platform_settings` (admin → Settings → Payment).

**Rotation:** rotate `ADMIN_PASSWORD` and `SERVICE_PASSWORD` independently; both
are compared constant-time. Rotating the service credential does not touch admin
access. Rotate any secret that was ever committed to `.env.local` (I13).

---

## 3. Health & monitoring

- Gateway health: `GET /health`.
- Money-path health: `request_logs` carry per-request status + cost; the credit
  deduction outbox depth is the signal to watch for stuck deductions.
- Platform: dashboards degrade gracefully when the gateway is unreachable
  (`fetchProxy` 10s timeout + typed `GatewayError`); GW-down surfaces as retryable,
  never fake success.

---

## 4. Deploy order

1. Apply gateway migrations 010→013.
2. Deploy the gateway binary (`go build ./...`; run behind TLS/reverse-proxy; admin
   panel behind a strong password and, ideally, an IP allowlist).
3. Deploy the platform (OpenNext → Cloudflare, `wrangler deploy`).
4. Smoke-test §5 on staging first.

---

## 5. E2E staging matrix (run manually; record pass/fail)

1. **Signup → key → chat.** New user → key provisioned (`api_keys.gateway_key_id`
   set) → chat via MuhiyaCode returns 200 → usage visible on both sides.
   **I6 regression:** create key → call `/v1/chat/completions` → 200; rotate → old
   key 401, new key 200.
2. **Budget lifecycle.** Exhaust budget → admin top-up grant → continues → set
   top-up expiry in the past → blocked again (expired top-up invisible to user).
3. **Bonus reset-all.** Admin "Reset All Usage" → every window usage 0, scheduled
   reset anchors unmoved (`plan_assigned_at` unchanged).
4. **Manual VF-Cash.** Enable in Settings + set wallet number → user submits
   (phone + screenshot) → admin queue shows it with proof → approve → plan active
   at approval instant, gateway enforces new limits; reject path shows the reason;
   second approve of the same row is a no-op (409).
5. **Gift cards.** Generate a plan batch + a credits batch → redeem one of each →
   plan activates / credits appear on the gateway; redeem the same code twice →
   second attempt blocked ("already redeemed").
6. **Paymob happy path** unchanged (regression).
7. **Gateway down.** Stop gateway → platform dashboards degrade gracefully;
   manual-approve surfaces a retryable error (does not mark approved); on gateway
   return, nothing double-applies (idempotent top-ups / guarded claims).

---

## 6. Owner action checklist for this release

- [ ] Apply gateway migrations 010→013 (in order).
- [ ] (Optional) set `SERVICE_USERNAME`/`SERVICE_PASSWORD` on the gateway; point
      the platform's gateway credential at it.
- [ ] Rotate any secret previously committed to `.env.local` (I13).
- [ ] In admin → Settings → Payment: enable Vodafone Cash + set the wallet number.
- [ ] Run the §5 matrix on staging; record results.
