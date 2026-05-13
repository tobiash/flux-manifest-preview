# ADR 0005: AI assessment adds provenance-aware classifications

## Status

Accepted

## Context

`fmp` already has deterministic policy classifications that can appear in reports, drive labels, and participate in `fail-on` behavior. Adding generated review output creates a determinism and trust boundary problem: users want AI-generated summaries and classifications, but consumers must still know whether a classification came from policy or from an AI assessment.

## Decision

AI output is modeled as an optional **AI Assessment** that may add classifications to the same classification set used elsewhere, but every classification carries `provenance` with either `policy` or `ai`. AI assessment is explicitly enabled through top-level `ai.enabled` config or invocation overrides, uses provider credentials from standard environment variables, and may emit free-form classification IDs unless `ai.allowed-classifications` is configured.

Policy effects such as labels and `fail-on` match classification IDs regardless of provenance after policy and AI classifications are merged. This deliberately lets opted-in AI classifications influence the same downstream behavior as policy classifications while preserving provenance for consumers that need to distinguish deterministic and generated review findings.

Provider selection auto-detects only when exactly one supported token is present; ambiguous or missing credentials warn and skip AI assessment by default. OpenAI, Anthropic, Z.AI, MiniMax, and OpenRouter are supported provider names. Opencode support is deferred until there is a stable provider contract. Implementation should spike LangChainGo as the single provider abstraction and fall back only if it cannot satisfy strict JSON response and provider support requirements.

## Consequences

Reports and GitHub Action outputs must preserve both the nested AI assessment metadata and the unified provenance-aware classification list. The render/diff workflow remains the orchestration owner: adapters choose settings and output formats, but do not duplicate AI assessment ordering or classification merging.

AI assessment creates a third-party data boundary, so inputs are bounded by AI-specific limits, reuse existing rendered diff filters, and get a baseline redaction pass before provider calls. Provider failures, missing tokens, ambiguous providers, and invalid responses warn by default rather than failing the core manifest preview workflow unless users opt into stricter behavior.

Reports expose both nested AI assessment metadata and the unified classifications list. GitHub Action outputs include `ai-assessment-json` and `ai-summary`; existing `classifications-json` becomes the unified provenance-aware list. Markdown and HTML render AI output in a clearly labeled **AI Assessment** section, while classification summaries preserve provenance.
