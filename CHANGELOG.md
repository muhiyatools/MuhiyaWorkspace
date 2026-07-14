# Changelog

## Unreleased

- Added inactive-by-default MiniMax provider/model seeds for MiniMax M3 and M2.x. A deployment without a MiniMax API key keeps the provider disabled.
- Added OpenAI-compatible MiniMax proxy handling with Bearer authentication, tool-call preservation, `reasoning_split=true`, streamed `reasoning_details`, provider-reported token usage, and passive cached-token accounting above MiniMax's 512-token threshold.
- Added MiniMax M3 tiered billing at the 512k prompt boundary, cached-input discounts, and flat M2.x rates.
- Added a 15-request multi-turn conformance suite covering text, reasoning, tools, usage, cache warmth, and cost boundaries. MiniMax verification is currently **SIMULATED via an authored upstream fixture**; live MiniMax verification is pending API funding. DeepSeek request captures remain byte-identical to the pre-feature baseline.
