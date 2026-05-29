---
name: tools-sync
description: >-
  Add or update a cloudy read-only tool (or a whole tool group/provider)
  without breaking the wire-format and skip-routing invariants. Use when
  introducing a new k8s/prom/log/trace/db/gpu/jvm/… tool, wiring a new
  tool group, or changing tool registration. Enforces NormalizeArguments
  on every provider adapter and correct MarkSkipped group keys.
---

# tools-sync

cloudy exposes read-only SRE tools to the LLM. Two runtime invariants break
silently if a new tool/provider ignores them — this skill keeps them intact.

## Where tools live

- Tool implementations: `internal/core/tools/{k8s,prom,log,trace,db,gpu,jvm,…}`.
- Shared adapters: `internal/clients/{httpapi,k8s,prom}`
  (packages `httpapi`, `k8sclient`, `promclient`).
- Registry + skip routing: `internal/core/tools` (`Registry`, `MarkSkipped`).
- Wiring (group enable/skip → registered tools): `internal/wiring/skills.go`.
- Provider adapters: `internal/core/llm/{anthropic,openai,moonshot,openai_compat,google}`.

## Invariants you MUST preserve

1. **Tool-call arguments are a JSON object on every wire.** All five
   provider adapters run inbound `parseSSE` flush and outbound
   `buildRequest` through `llm.NormalizeArguments`
   (`internal/core/llm/args.go`). The parameter-less tool class
   (`k8s_list_nodes` etc.) silently 4xx'd the next turn before this rule
   existed. Any new adapter / request path must call it; the helper
   collapses nil/empty/null/whitespace/partial/non-object inputs to `{}`.
2. **`MarkSkipped` keys are NOT always the dot-prefix of the tool name.**
   The `perf` provider registers `perf.*` tools but skips with sub-group
   keys `perf-pprof` / `perf-v8` / `perf-linux`. Any consumer mapping a
   tool name to a skipped group via `groupPrefix` MUST also match
   `<group>-*` sub-keys — see `isInSkippedGroup` in
   `internal/wiring/skills.go`.

## Procedure

1. **Implement the tool** in the right `internal/core/tools/<group>`
   package, matching the existing read-only style (no mutation, bounded
   output — dotted-path projection for large objects, see `list_cr`).
2. **Register + wire** it; if it's a new group, add the enable/skip path
   in `internal/wiring/skills.go` and respect the sub-key skip shape above.
3. **If you touched a provider adapter or request path**, confirm it still
   routes through `NormalizeArguments` on both the inbound flush and
   outbound build.
4. **Update docs** (delegate to `docs-sync`): bump the tool/group counts in
   `README.md` and the `CHANGELOG.md` Unreleased section.
5. **Verify:** `go build ./...`, `go test ./...`,
   `golangci-lint run --timeout=5m ./...`. Add a regression test for any
   new skip-routing or arg-normalization path.
6. **Commit:** `git commit -s -m "feat(<group>): ..."` then the standard
   PR + `/code-review` + merge flow.
