# cloudy — Project Conventions

Read-only multi-cluster SRE monitoring AI CLI. Go module rooted at
`github.com/rlaope/cloudy`; full-screen TUI at `cloudy`, one-shot at
`cloudy ask "..."`. Internal layout is tiered:
- `internal/core/{agent,llm/{anthropic,openai,moonshot,openai_compat,google},tools/{k8s,prom,log,trace,db,gpu,jvm,…},skills}` — domain.
- `internal/clients/{httpapi,k8s,prom}` — shared HTTP/k8s/prom adapters
  (package names `httpapi`, `k8sclient`, `promclient`).
- `internal/ui/{tui,cli}` — terminal and one-shot CLI surfaces.
- Top-level support: `internal/{config,permission,registry,render,
  session,setup,discovery,secrets,selfupdate,transport,wiring,
  buildinfo}`.

## Build / test / lint

- `go build -trimpath -ldflags "-s -w -X github.com/rlaope/cloudy/internal/buildinfo.Version=$(git describe --tags --always --dirty)" -o ~/.local/bin/cloudy ./cmd`
  installs to the path the user QAs from.
- `go test ./...` and `golangci-lint v2.12 run --timeout=5m ./...` are
  what CI runs. `go.mod` targets `go 1.25.0`; the lint config is v2 and
  restricts `staticcheck` to `SA*` checks (matches v1 behaviour).

## Commit / PR workflow

- Conventional commits in English (`fix(scope): ...`, `feat(scope): ...`),
  even when the conversation is Korean.
- DCO sign-off is required by CI — every commit needs
  `Signed-off-by: rlaope <piyrw9754@gmail.com>` (use `git commit -s`).
- Do NOT add `Co-Authored-By: Claude` to commits.
- Default flow is per-feature branch + `gh pr create` + `/code-review`
  + apply findings + `gh pr merge --merge --delete-branch` + sync
  master + rebuild + reinstall. Direct master pushes only when the
  user explicitly says so.

## Runtime invariants worth knowing

- **Tool-call arguments must be a JSON object on every wire.** All five
  provider adapters (anthropic, openai, moonshot, openai_compat,
  google) MUST run the inbound parseSSE flush and outbound buildRequest
  through `llm.NormalizeArguments` (`internal/core/llm/args.go`) — the
  parameter-less call class (`k8s_list_nodes` etc.) silently 4xx'd the
  next turn before this rule was in place. The helper collapses
  nil/empty/null/whitespace/partial/non-object inputs to `{}`.
- **`tools.Registry.MarkSkipped` keys are NOT always the dot-prefix of
  the tool name.** The `perf` provider registers `perf.*` tools but
  skips with sub-group keys `perf-pprof` / `perf-v8` / `perf-linux`.
  Any consumer that maps a tool name to a skipped group via
  `groupPrefix` MUST also match `<group>-*` sub-keys — see
  `internal/wiring/skills.go: isInSkippedGroup`.

  Note: `internal/wiring` consumes `internal/core/tools.Registry`; the
  invariant is about that registry's key shape, not the file path.
- **`render.Sink` writes to the session log via `tuiSink` in
  `internal/ui/tui/run.go`.** Tool args, observation text, and the raw
  user prompt are deliberately NOT mirrored to disk yet — the
  AfterToolCall masker isn't reachable from this seam, so persisting
  them would re-open the v0.5 M-1 redaction gap. Tool name +
  KindError + KindUsage(modelID) are persisted; rest is TODO.

## Debugging hooks

- Session log: `~/.cloudy/logs/<id>.jsonl`. `tail -1 $(ls -t … | head -1)`
  surfaces the latest agent error verbatim — that's how the Anthropic
  `tool_use.input: Field required` 400 was diagnosed.

## Korean responses

User prefers Korean responses ending in 존댓말 (`-습니다`, `-세요`, …).
Code, comments, commit messages, and PR bodies stay English.

## Coding discipline

Drawn from `andrej-karpathy-skills:karpathy-guidelines`. Bias toward
caution over speed.

1. **Think before coding.** State assumptions; if multiple
   interpretations exist, surface them instead of silently picking. If
   something is confusing, name it and ask rather than guess.
2. **Simplicity first.** Smallest code that solves the problem; no
   features, abstractions, or configurability beyond the ask. No error
   handling for impossible scenarios. If a senior engineer would call
   it overcomplicated, simplify.
3. **Surgical changes.** Touch only what the request requires. Match
   existing style. Clean up only orphans your own change created. If
   you notice unrelated dead code, mention it — don't delete it.
4. **Goal-driven execution.** Turn the ask into a verifiable success
   criterion (failing test → passing test, broken behaviour → repro
   then fix) and loop until verified. Weak criteria ("make it work")
   require clarification; surface that early.
