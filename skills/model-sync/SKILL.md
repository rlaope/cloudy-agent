---
name: model-sync
description: >-
  Keep cloudy's curated LLM model lists current and free of deprecated /
  404ing model IDs. Use when a provider (Anthropic, OpenAI, Google,
  Moonshot) ships a new flagship, retires a model, or when the /login
  picker / setup wizard offers a stale default. Verifies real API model
  IDs against each provider's docs before editing.
---

# model-sync

cloudy curates a short, latest-first list of model IDs per provider. Model
IDs deprecate and start returning 404 (this has bitten the project before —
`claude-3-5-sonnet-20241022`, `kimi-k2-instruct`), so this skill keeps the
lists current and routable.

## Source of truth (edit these)

| What | File | Symbol |
|------|------|--------|
| Curated per-provider model lists (the /login picker) | `internal/ui/tui/loginchat.go` | `loginProviders` |
| Setup wizard model placeholder + default value | `internal/setup/wizard.go` | `initFillIn` (Step 6) |
| Model-string → provider routing | `internal/core/llm/provider.go` | `prefixMap` / `Resolve` |

Tests that pin the curated default (`models[0]`) and routing — update in lockstep:
- `internal/ui/tui/loginchat_test.go`
- `internal/ui/tui/model_picker_test.go`
- `internal/core/llm/provider_test.go`

`internal/config/config.go` `DefaultModel` is intentionally empty — do NOT
hard-code a default there. `/login` owns model selection end-to-end.

## Procedure

1. **Verify current IDs.** For each provider, confirm the exact API model
   string (not the marketing name) and its deprecation status from the
   official docs — never guess an ID:
   - Anthropic: <https://docs.anthropic.com/en/docs/about-claude/models>
   - OpenAI: <https://developers.openai.com/api/docs/models/all>
   - Google: <https://ai.google.dev/gemini-api/docs/models>
   - Moonshot/Kimi: <https://platform.kimi.ai/docs/models>
2. **Edit `loginProviders`.** Latest-released model first (it becomes the
   default cursor). Keep ~4 entries: current-and-useful, not exhaustive.
   Drop any ID the docs mark deprecated/discontinued. Update the provider
   `hint:` string to match.
3. **Check routing.** Every new ID must match a `prefixMap` entry so
   `Resolve` routes it. Watch for IDs that don't share the family prefix —
   e.g. OpenAI's `o3`/`o4-mini` do NOT match `o1-`; they needed their own
   `o3`/`o4` prefix entries.
4. **Refresh the wizard.** If `initFillIn`'s placeholder/default is stale,
   point it at the new flagship.
5. **Update the pinned tests** so `models[0]` expectations match the new
   default.
6. **Verify:** `go build ./...` then
   `go test ./internal/ui/tui/... ./internal/setup/... ./internal/core/llm/...`.
7. **Commit:** `git commit -s -m "feat(models): ..."` (DCO required; no
   `Co-Authored-By`). Then the standard PR + `/code-review` + merge flow.
