# Changelog

## Unreleased

- Removed monetary budget reservations and their reconciler, schema links, and Admin UI. Budget windows now use completed request logs only; affordable output limits are calculated before inference, per-user generations queue behind a non-monetary Redis guard, and actual usage/top-up/ledger writes settle atomically.
- Added idempotent user top-ups (migration `013_topup_idempotency.sql`): `user_topups.idem_key` + partial unique index; `CreateUserTopup` uses `ON CONFLICT DO NOTHING`, so a retried or gift-card-driven top-up can never double-credit. Ordinary admin top-ups (no key) are unaffected.
- Added an optional scoped service credential (`SERVICE_USERNAME`/`SERVICE_PASSWORD`) accepted only for the platform's `/api/{users,keys,logs,stats,plans,settings,health}` endpoints, never the HTML admin panel. Backward-compatible: unset ⇒ behaviour unchanged; the human admin credential still works everywhere.
- Added `docs/INTEGRATION.md` (gateway↔platform seam contract, single-sources-of-truth, drift-risk notes) and `docs/PRODUCTION.md` (migration order 010→013, required env, deploy, E2E staging matrix, owner checklist).
- Added inactive-by-default MiniMax provider/model seeds for MiniMax M3 and M2.x. A deployment without a MiniMax API key keeps the provider disabled.
- Added OpenAI-compatible MiniMax proxy handling with Bearer authentication, tool-call preservation, `reasoning_split=true`, streamed `reasoning_details`, provider-reported token usage, and passive cached-token accounting above MiniMax's 512-token threshold.
- Added MiniMax M3 tiered billing at the 512k prompt boundary, cached-input discounts, and flat M2.x rates.
- Added a 15-request multi-turn conformance suite covering text, reasoning, tools, usage, cache warmth, and cost boundaries. MiniMax verification is currently **SIMULATED via an authored upstream fixture**; live MiniMax verification is pending API funding. DeepSeek request captures remain byte-identical to the pre-feature baseline.
