# Contributing

cloudy is a read-only multi-cluster SRE monitoring AI CLI agent. The bar for
a change is twofold:

1. **Read-only end-to-end.** Every backend cloudy touches is reached through
   one of three guards: an HTTP transport that rejects everything outside
   `GET / HEAD / OPTIONS`, a Kubernetes verb whitelist on top of it, and an
   exec layer that uses fixed argv vectors (no concatenation from LLM input)
   for CLI tools. New tools must fit one of those guards. Free-form SQL,
   arbitrary command strings, and free-form `bpftrace` programs are
   intentionally out of scope.
2. **Self-describing through the harness.** Every tool group registers via
   `RegisterAll(reg, deps)` and either lights up tools or records why it
   could not (`reg.MarkSkipped("group", "reason")`). The user runs
   `cloudy tools` or hits `/tools` in the TUI and sees a single coherent
   inventory — what is on, what is off, and why.

Quick links: [adding a tool group](#adding-a-new-tool-group) ·
[local dev](#local-dev-setup) · [conventions](#conventions) ·
[tests](#tests-and-ci) · [CHANGELOG + DCO](#changelog-and-commit-signing).

## Local dev setup

```
go 1.24+
golangci-lint v1.64 (optional locally, required in CI)
```

```sh
git clone https://github.com/rlaope/cloudy
cd cloudy
make build           # builds ./cmd → ./cloudy
make test            # go test -race -count=1 ./...
make lint            # golangci-lint or `go vet` as a fallback
./cloudy tools       # see what wired in on your machine
```

There is no required `cloudy.yaml` to build or run unit tests — only
integration paths consult one.

## Adding a new tool group

The cleanest contribution is a brand-new group (e.g. Kafka, etcd, Vault). The
template is intentionally short. Pick an existing similar group to copy from:

| Pattern | Reference |
|---|---|
| HTTP query API (Prom-like) | `internal/tools/log` (Loki/ES), `internal/tools/trace` (Tempo/Jaeger) |
| HTTP profile capture | `internal/tools/perf` (Go pprof, V8 Inspector) |
| Read-only database driver | `internal/tools/db` (Postgres/MySQL/Redis) |
| Local exec with argv allow-list | `internal/tools/jvm`, `internal/tools/py`, `internal/tools/perf/rbspy.go` |
| Linux-only / capability-gated exec | `internal/tools/ebpf` |
| Catalog (no free-form input) | `internal/tools/ebpf/bpftrace.go` |

### Steps

1. **File a `[tool] …` issue** using the *Tool group proposal* template.
   The proposal answers the read-only question per tool. Wait for the
   "approved" label before writing code.

2. **Create the package.** Most groups live in `internal/tools/<group>/`:
   ```
   internal/tools/<group>/
     register.go     # BuildClients + RegisterAll(reg, deps, skipReasons)
     client.go       # connection/dialing — optional, may live in register.go
     <tool>.go       # one file per logical sub-area
     <group>_test.go # tests
   ```

3. **Use `tools.Spec[Args]`.** Each tool is a small descriptor:
   ```go
   tools.Spec[args]{
     Name:        "kafka.topics_describe",
     Description: "Return per-topic partition and replica counts.",
     Schema:      mustJSON(map[string]any{...}),
     Run: func(ctx context.Context, a args) (tools.Observation, error) { ... },
   }.Build()
   ```
   `Spec` handles JSON unmarshalling and Name/Description/Schema for you.

4. **Wire `RegisterAll`.** It takes the registry and any per-group deps,
   registers tools that have working backends, and marks skipped groups
   when nothing is registered:
   ```go
   func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
     if c.Empty() {
       reg.MarkSkipped("<group>", composeReason(...))
       return
     }
     reg.MustRegister(newFooTool(...), newBarTool(...))
   }
   ```

5. **Plumb through `internal/wiring/tools.go`.** Add to `Options`, add a
   `BuildClients` call, call `RegisterAll`. Keep the wiring shallow — `wiring`
   does not know what tools exist inside a group, only that the group exists.

6. **Add CLI flow-through.** `internal/cli/ask.go` and `internal/cli/tools.go`
   call `wiring.BuildRegistry({...})` — extend the option struct fields.

7. **Tests:**
   - `BuildClients_*` for the dispatch table: no endpoints, missing fields,
     unknown kind.
   - `RegisterAll_*` for the registration paths: empty marks group skipped,
     each subkind registers the expected tools.
   - Catalog tools: unknown key returns an explicit error, stubbed runner /
     httpapi confirms argv / request shape.
   - **Never** add tests that require external network or running daemons —
     stub the runner / client.

8. **Update `CHANGELOG.md`** under `## Unreleased`.

### Read-only patterns by tool type

- **HTTP**: wrap `internal/tools/httpapi.Client`. The transport already
  rejects non-GET. Use `RawGet` and parse on the way back.
- **SQL**: connect with a read-only user; expose only fixed queries (named
  helpers). Never pipe LLM input into the SQL text.
- **Exec**: use a fixed argv vector. Make the runner a package variable so
  it can be stubbed in tests. Cap stdout with a `limitWriter` and apply a
  `context.WithTimeout` ceiling on top of the tool's own duration arg.
- **eBPF**: only kernel-observation probes. No `system()`. No mutating
  kfunc attach. For `bpftrace`, expose only a catalog entry, never a
  free-form `program` field.

### Required probe behavior

If the group can fail to wire (binary missing, endpoint unreachable, OS
not supported, capability not granted), surface the reason:

```go
reg.MarkSkipped("kafka", "kafka-topics binary not on PATH")
// or
reg.MarkSkipped("kafka", fmt.Sprintf("AdminClient connect %s: %v", url, err))
```

`/tools` will show:
```
kafka     skipped  kafka-topics binary not on PATH
```

## Conventions

- **Naming**: tool names are `<group>.<verb_noun>`, snake_case. The first
  segment (before the first dot) is the group key used by `Inventory()`.
- **Schemas**: use `mustJSON` (per-package helper) for the JSON Schema. Add
  `default`, `minimum`, `maximum` to numeric fields; mark required arguments
  in the schema's `required` array.
- **Observation**: `Text` is the LLM-facing summary; `Table` is the
  TUI-friendly rendering; `Raw` is the post-processing payload.
- **Comments**: write WHY a non-obvious decision was made (e.g. "session-
  level `default_transaction_read_only=on` as belt-and-suspenders alongside
  the configured RO user"). Skip WHAT — the code tells you that.
- **Project content (READMEs, CHANGELOGs, commit messages, doc strings)
  uses English and 평어 (`-다` form for Korean prose), never 존댓말 or
  emojis.** Conversation in issues / PR discussions is at your discretion.

## Tests and CI

```
make test          # go test -race -count=1 ./...
make vet           # go vet ./...
make fmt           # gofmt -s -w .
make lint          # golangci-lint or go vet fallback
```

CI runs `build / test / lint` on every PR plus the DCO check. PRs cannot
merge until both pass. golangci-lint v1.64 is what runs in CI — keep the
`go` directive in `go.mod` at the latest version it supports (currently
`1.24`).

If a Go module dep forces `go 1.25+`, downgrade to an older patch version
of the dep or open an issue first. We pin a known-good range; see
`go.mod`.

## CHANGELOG and commit signing

- Update `CHANGELOG.md` under the `## Unreleased` section with a short
  bullet per change. Group by `### Added` / `### Changed` / `### Fixed` /
  `### Security`. Reference tool names where applicable.
- **Sign every commit** with `git commit -s` (DCO is enforced).
- **Do NOT add `Co-Authored-By:` / `Generated-with:` / any AI-author
  trailer.** Commits are by the human running the keyboard.
- **No `--no-verify` / no `--no-gpg-sign`** unless explicitly requested in
  the PR thread.

## Reviewing a PR

If you review someone else's PR, the read-only contract is the load-bearing
check: walk through every new tool's `Run` and confirm it cannot mutate
the backend, then walk through every new exec path and confirm the argv
vector is fixed. Everything else (style, comments, test shape) is
secondary.
