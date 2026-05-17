# Changelog

## v0.4.0 — 2026-05-17

### Added — Safety hardening
- **Risk-rated tools + interactive approval gate** — `tools.RiskLevel`
  classifies every tool as `low` / `medium` / `high` based on how much the
  call perturbs the system being observed (STW pauses, attached probes,
  long sampling windows, cluster-wide scans). `RiskRated` is an optional
  interface tools may implement; an `riskByName` allowlist catches the
  curated set (`jvm.async_profile`, `jvm.jcmd_gc`, all `perf.*`, all
  `py.spy_*`, all `ebpf.*`). `agent.ApprovalHook` gates `RiskHigh` calls
  through a pluggable `Approver`; lower-risk calls pass through unchanged.
  The TUI surfaces a `y/N/Esc` banner (`AgentEvent.Approval` +
  `ApprovalRequest{Tool, Args, Reply}`); unrelated keys are swallowed
  while a decision is pending. `agent.DenyApprover` is the non-interactive
  default for `cloudy ask`, so headless entry points refuse high-risk
  calls with a message pointing the operator at the TUI.
- **`LogSummaryHook` for oversized log observations** —
  `agent.LogSummaryHook` compresses `log.*` tool responses larger than
  `Options.MaxLogResponseBytes` (default 64 KB). `SummarizeLog` keeps
  head/tail on newline boundaries plus exception/error context windows
  (`exception|caused by|panic|traceback|stack trace|fatal|error`, ±5
  lines) with adjacent-duplicate dedup. Below the threshold the
  observation passes through unchanged — head/tail clipping is more
  expensive than a flat truncate, so it is reserved for cases where it
  actually pays for itself.
- **Cost guard hook** — `agent.LimitGuardHook` and `agent.CostGuardHook`
  enforce per-session token caps and per-day USD caps, plus profile
  duration / log-line caps. Wired from `config.SafetyConfig` through
  `cmd/main.go`, `cli/ask.go`, and `tui.Deps` to `agent.Options`.
- **LLM circuit breaker + provider fallback** — `llm.Circuit` short-
  circuits a failing provider for a cooldown window; `llm.Fallback`
  composes a primary + secondary `Provider` and switches on persistent
  failure. The wiring layer composes these around the openai_compat
  adapter so vLLM / Ollama / OpenRouter outages degrade gracefully.
- **Cloud identity discovery** — `discovery.cloud_iam` probes AWS /
  GCP / Azure metadata endpoints to surface the apparent IAM identity
  in `/setup` and `cloudy doctor`, helping operators sanity-check which
  account cloudy is running under on a bastion.
- **Conversation timeout** — `agent.Options.ConversationTimeout` bounds
  total wall-clock for a single `Run()`. Hits surface as a clear
  `conversation timeout` rather than an opaque context-deadline error.

### Added — Auto-discovery & TUI integration
- **Backend auto-discovery via `discovery.Detector` package** — `internal/discovery/`
  defines the core abstractions: `Detector` interface, `Finding` result type,
  `Source` enum (Kubernetes Service/Pod heuristics, user hints), and
  `AuthHint` for credential scoping. The `Coordinator` fan-outs every
  registered detector with a 30s deadline, per-detector panic recovery, and
  stable 4-key sort (kind, name, namespace, labels). Backend packages
  (`prom`, `log`, `trace`, `db`, `perf`) self-register their detectors via
  `init()` without coupling to the core.
- **Detectors for prom / log / trace / db / perf** — each backend tool group
  gains a `detector.go` file that recognizes running services from K8s
  Service names (label `app.kubernetes.io/name` match, port number, port
  name heuristics) plus user-supplied external hints from `cloudy.yaml`:
  - `prom` detector recognizes Prometheus endpoints.
  - `log` detector recognizes Loki, Elasticsearch, and their endpoints.
  - `trace` detector recognizes Tempo, Jaeger, and their endpoints.
  - `db` detector recognizes Postgres, MySQL, Redis services.
  - `perf` detector recognizes pprof, V8 Inspector, and rbspy targets.
- **Bastion → in-cluster reachability** — two new transport layers enable
  bastions outside K8s to reach workloads without VPN:
  - `transport.ServiceProxy` constructs apiserver `services/proxy` URLs and
    wraps the apiserver-authenticated http.Client in `ReadOnlyRoundTripper`
    to enforce the GET/HEAD/OPTIONS whitelist end-to-end for every backend
    HTTP call (Prometheus, Loki, Elasticsearch, Tempo, Jaeger, pprof, V8).
  - `transport.OpenPortForward` opens a SPDY port-forward in-process and
    hands back a `localhost:<auto>` address so TCP backends (Postgres,
    MySQL, Redis) can be dialled transparently. `db.BuildClients` recognizes
    a new `k8s://<context>/<ns>/<svc>:<port>` DSN scheme and transparently
    tunnels TCP connections.
- **Atomic registry hot-swap** — `wiring.Current()` and `wiring.Replace()`
  back the live `*tools.Registry` with `sync/atomic.Pointer`. `agent.New`
  gains `Options.RegistryFn func() *tools.Registry` so each `Run()` picks
  up a registry the user just rebuilt via `/setup` without restarting the
  process.
- **7-step `/setup` wizard inside the TUI (Mixed flow)** — the setup wizard
  evolves from 5 steps to 7 and becomes an embeddable `*WizardModel`:
  kubeconfig → scan → discovered → credentials → hints → fill-in → skills
  → save. Steps 3/4/5 are new:
  - `stepDiscovered` is a full-screen group-by-kind checkbox selection over
    the coordinator's `[]Finding` list.
  - `stepCredentials` is a stream-inline Q&A that references existing env
    vars via `$ENV_VAR` or accepts pasted values (echoed as ★) and writes
    them to `~/.cloudy/secrets` with auto-generated keys
    (`CLOUDY_<KIND>_<NAME>_PWD`). `cloudy.yaml` never receives a secret
    directly.
  - `stepHints` accepts ad-hoc external backends via `kind URL [auth]`.
  On save the wizard rebuilds the registry via `wiring.BuildRegistry` and
  `wiring.Replace`. Process restart is not required; the next user question
  uses the updated catalog.
- **`~/.cloudy/secrets`** — new `internal/secrets/` package persists user
  credentials as a dotenv file (mode 0600) and replays them into `os.Setenv`
  at boot so existing `*_env` config fields keep working without source
  changes.
- **`tui.WelcomeModel` + first-launch banner** — `cmd/main.go` no longer
  short-circuits when config is absent. The TUI always opens; on first run
  a `cloudy` ASCII banner with `/setup`, `/help`, `⏎` hints is rendered
  above the empty stream, and on return visits a compact one-liner replaces
  it.
- **Closes the original polyglot profiler list**: Linux `perf record/report`
  wrapper, Go pprof CPU binary decode, and V8 Inspector CPU profile
  capture over the Chrome DevTools Protocol.
  - `perf.linux_perf_record` — runs `perf record -g -p <pid> -F <hz> -- sleep <dur>` to
    a tempdir-scoped `perf.data`, then renders call-graph text via
    `perf report --stdio`. Linux-only; skipped with a clear reason on
    non-Linux hosts.
  - `perf.go_pprof_cpu` — captures `/debug/pprof/profile?seconds=N` and
    decodes the protobuf with `github.com/google/pprof/profile` to a
    top-N flat/cum % table. No more "go tool pprof needed offline" —
    the LLM sees a ranked function list directly.
  - `perf.v8_inspector_cpu_profile` — full CDP exchange
    (`Profiler.enable` → `start` → sleep → `stop`) via
    `gorilla/websocket`. Returns the top-N functions by `hitCount`
    with the V8 profile object available as `Raw`. The websocket
    dial reuses the read-only transport's `DialContext` for
    consistency with the rest of the codebase.
- **`ebpf.*` kernel observability tools** (Linux + CAP_BPF / root only):
  - BCC wrappers: `ebpf.biolatency`, `ebpf.tcptop`, `ebpf.tcprtt`,
    `ebpf.execsnoop`. Each accepts a bounded `duration_seconds`
    (1–60, default 5); the subprocess runs under an additional
    `duration + 5s` context deadline so a misbehaving binary cannot
    exceed the requested window.
  - `ebpf.bpftrace_oneliner` — a single tool that selects from a fixed,
    code-reviewed catalog of read-only one-liners (`syscall_counts`,
    `file_opens`, `tcp_connects`, `vfs_read_lat`). The schema declares
    the catalog keys via `enum` and **never** accepts a free-form
    `program` argument; adding an entry is a deliberate code change.
  - Platform gate: non-Linux hosts mark the `ebpf` group skipped with
    a single reason. Hosts that miss specific binaries (e.g. BCC
    installed but no bpftrace) register what they can and the rest
    surface per-tool reasons.
- **`perf.*` profiler-attach tools** across the polyglot SRE surface:
  - Go: `perf.go_pprof_goroutine` / `_heap` / `_allocs` / `_threadcreate`
    fetch the text-formatted (`?debug=1/2`) variants of
    `/debug/pprof/<kind>` from services configured under the new
    `pprof:` block of `cloudy.yaml`. The binary CPU profile is
    intentionally deferred until a follow-up release adds the
    `google/pprof` parser.
  - Ruby: `perf.rbspy_dump` shells out to `rbspy dump --pid N --time S`
    with a fixed argv vector (no user-controlled string concatenation).
    Always registered; binary lookup happens at call time.
  - Node.js: `perf.v8_inspector_targets` enumerates V8 Inspector
    debug targets via `GET /json/list` against endpoints configured
    under `node_inspectors:`. Deeper CPU/heap capture over CDP is
    deferred.
- `internal/tools/httpapi` now backs `perf.*` too, alongside `log.*` and
  `trace.*` from the wave above.
- **`log.*` and `trace.*` read-only query tools** for Loki, Elasticsearch,
  Tempo, and Jaeger. Two new config blocks — `logs:` and `tracing:` —
  accept named HTTP endpoints with `kind` ∈ {loki, elasticsearch, tempo,
  jaeger}, URL, and optional Basic / Bearer auth. Every call goes through
  `internal/tools/httpapi`, a small read-only HTTP client extracted from
  `prom` so the GET-only transport contract is shared across the four
  backends without duplication.
  - Loki: `log.loki_query_range`, `log.loki_labels`,
    `log.loki_label_values`, `log.loki_series`.
  - Elasticsearch (URI search only, no request body): `log.es_search`,
    `log.es_indices`, `log.es_cluster_health`.
  - Tempo: `trace.tempo_get_trace`, `trace.tempo_search` (TraceQL or tags).
  - Jaeger: `trace.jaeger_services`, `trace.jaeger_operations`,
    `trace.jaeger_search_traces`.
  - Per-endpoint failures roll up into the group skip reason surfaced by
    `cloudy tools` / `/tools`.
- **`db.*` read-only diagnostic tools** for Postgres, MySQL/MariaDB, and
  Redis. The `databases:` section of `cloudy.yaml` defines named endpoints
  with `kind` ∈ {postgres, mysql, redis}, a DSN, and an optional
  `password_env`. cloudy connects with `default_transaction_read_only=on`
  for Postgres and exposes only canonical read queries — there is no
  free-form SQL or arbitrary command surface.
  - Postgres: `db.pg_version`, `db.pg_stat_activity`, `db.pg_stat_database`,
    `db.pg_stat_replication`, `db.pg_locks`, `db.pg_top_table_size`.
  - MySQL/MariaDB: `db.mysql_version`, `db.mysql_processlist`,
    `db.mysql_global_status`, `db.mysql_global_variables`,
    `db.mysql_engine_innodb_status`, `db.mysql_top_table_size`.
  - Redis/Valkey: `db.redis_info`, `db.redis_dbsize`, `db.redis_scan`
    (non-blocking, capped), `db.redis_inspect_key` (TYPE + TTL + MEMORY
    USAGE), `db.redis_slowlog`, `db.redis_client_list`.
  - Per-endpoint dial errors are surfaced through the group-`db` skip
    reason via the harness added in the previous release.
- `cloudy tools` CLI subcommand and TUI `/tools` slash command — list every
  registered tool group (`k8s`, `jvm`, `py`, `gpu`, `prom`, …) plus *skipped*
  groups with a one-line reason. `tools.Registry.MarkSkipped(group, reason)`
  records why a group was unavailable at wire time so the inventory surface
  can show "skipped: no kubeconfig" instead of dropping the group silently.
- Group-aware inventory via `tools.Registry.Inventory()` — groups are
  derived from the tool-name prefix before the first dot (`k8s.list_pods` →
  group `k8s`). Filter now preserves skipped reasons across skill-narrowed
  copies of the registry.

### Changed
- **Alt-guidance on every read-only block** — `transport/readonly.go`
  rejects non-`GET/HEAD/OPTIONS` requests with a per-method alternative
  hint (e.g. `POST → use the GET equivalent`), and `tools.Registry`
  rejects mutator-named tool registration at boot with a panic that
  suggests the read-only verb. The LLM sees these strings, so the
  feedback loop is "use a cheaper / read-only alternative", not "fail".
- **Agent preamble updated** — the system prompt now explicitly tells
  the LLM not to retry on approval-denied or read-only-violation errors
  and to pick a lower-risk alternative instead, matching the alt-guidance
  surfaced by the transport and registry layers.
- `db.BuildClients` signature now takes a `*k8s.Hub` so it can resolve
  `k8s://` DSN schemes into a SPDY tunnel; non-k8s DSNs go through the
  existing direct-dial path unchanged.
- `cmd/main.go` removes early returns when `cfg.DefaultModel == ""` or
  `wiring.BuildProvider` errors; both now surface as a stderr note and the
  TUI opens so the user can reach `/setup` and repair the configuration.
- `tui.Deps` adds `FirstRun bool` and the agent picks up the registry via
  `Options.RegistryFn` rather than a frozen `Options.Registry`.
- `internal/tools/prom`: empty client map now marks group `prom` skipped
  with reason `no prometheus endpoints configured`.
- `internal/wiring/tools.go`: kube-client construction failure marks group
  `k8s` skipped with the underlying error string, in addition to returning
  the existing `*KubeWarning`.

### Security
- Three read-only guards remain intact. The new apiserver-proxy URL still
  flows through `ReadOnlyRoundTripper`, so the GET/HEAD/OPTIONS whitelist
  applies to every Loki / Tempo / pprof / V8 call exactly as before. The
  new RBAC verbs (`services/proxy: get`, `pods/portforward: create`) are
  the minimum required for the new reachability layers and do not widen
  cloudy's mutation surface.
- `manifests/rbac/base/clusterrole.yaml` gains two new verbs: `get` on
  `services/proxy` and `create` on `pods/portforward`. See
  `manifests/rbac/README.md` for a one-paragraph rationale.
- **Approval gate is defense-in-depth, not the primary read-only contract.**
  The HTTP method whitelist, K8s verb whitelist, and ClusterRole RBAC
  already make mutation impossible. The risk-rated approval gate only
  prevents *expensive but legal* read-only calls (STW pauses, multi-second
  profile windows, attached probes) from being dispatched without an
  operator decision. Tool registration also panics at boot if a name
  contains a mutation verb (`tools.Registry` mutator enforcement), closing
  a final gap where a future contributor might add a tool with a
  mutation-sounding name by accident.

## v0.3.0 — 2026-05-13

Architecture deepening pass. No user-facing behaviour change; the public
CLI surface (`cloudy ask / setup / doctor / skills / session / contexts /
profile`) and the Permission Profile / RBAC / read-only contracts are
preserved verbatim. Internal shape changes are the substance of this
release.

### Changed
- **Command layer split** — every subcommand moved from `cmd/cloudy/*.go`
  into `internal/cli`, each owning its own option struct and registering
  itself via `init()` against a tiny `cli.Command` dispatcher. The 12-field
  `commonOptions` grab-bag is gone; new subcommands require one file plus
  one `Register()` call and `cmd/cloudy/main.go` no longer changes.
- **Tool group self-registration** — every subpackage under
  `internal/tools/` (`k8s`, `prom`, `jvm`, `py`, `gpu`) now exposes its own
  `RegisterAll(reg, deps)` helper. `wiring.BuildRegistry` shrinks to
  dependency construction plus one call per group; dead
  `EnableJVM/EnablePython/EnableGPU` flags are removed.
- **Shared generic registry** — new `internal/registry.Map[T]` is the
  storage substrate behind `llm.providers`, `tools.Registry`, and
  `skills.Registry`. Domain methods (Resolve / Filter / Suggest / Validate)
  stay where they were; only storage is unified.
- **Tool interface deepened** — `ReadOnly()` removed from `tools.Tool` (the
  HTTP/K8s transport guards already enforce read-only end-to-end; the
  type-level method was redundant defense). New generics
  `tools.Spec[Args]` and `k8s.ListResourceSpec[T]` absorb the per-tool
  boilerplate: every K8s tool migrated to descriptors with Items +
  ProjectRow callbacks.
- **Agent hook chain** — duplicate-call detection and any other
  cross-cutting policy now live behind `agent.Hook`
  (`BeforeToolCall / AfterToolCall / OnAssistantTurn / OnStop`).
  `agent.Run` becomes a clean loop; cost guard, masking, audit, and
  telemetry are addable without touching it. `DupCallHook` ships as the
  default registered hook.
- **`render.Sink` seam** — `agent.Agent.Run` now takes a `render.Sink`
  instead of a concrete `*render.Stream`. The TUI supplies its own
  `tuiSink` that turns Begin/EndToolCall into structured AgentEvents,
  retiring the previous interceptStream / interceptWriter hack that wrote
  formatted bytes only to parse them back out.
- **`tui.Deps` typed** — Provider / Session / AgentRunner are now properly
  typed (`llm.Provider`, `*session.Session`, `func(<-chan struct{}, ...)`).
  The "to avoid import cycles in tests" `interface{}` comment was wrong
  and has been replaced by direct imports.

### Security
- All three read-only guards remain intact (HTTP method whitelist, K8s
  verb whitelist, ClusterRole RBAC). Removing `Tool.ReadOnly()` is
  defense-in-depth that the transport layer already provided; the kube
  client cannot construct a non-`get/list/watch` request and the HTTP
  transport rejects anything outside `GET/HEAD/OPTIONS`.

## v0.2.0 — 2026-05-13

### Added
- **T2 Bastion deployment** — `CLOUDY_HOME` for per-user state isolation,
  `HTTPS_PROXY` honoured by every LLM adapter via Go's default transport,
  `manifests/bastion/install.sh` and `cloudy@.service` systemd template,
  full guide in `docs/BASTION.md`.
- **Permission Profiles (Layer 2)** — YAML profile schema (`name`,
  `description`, `contexts`, `namespaces.allow/deny`, `tools.allow/deny`,
  `limits`, `masking.key_regex/value_regex`); `cloudy profile new / list /
  show / use / none / cluster` subcommands; active profile resolved from
  `$CLOUDY_PROFILE` env or `~/.cloudy/active_profile`; profile narrows the
  Tool Registry before any skill activation.
- **Field-level masking** — `permission.Masker` redacts secret keys
  (`password`, `token`, `api_key`, …) and value patterns (AWS access keys,
  JWT prefixes, GitHub PATs, OpenAI-style keys) from observations and
  free-form text. `permission.DefaultMaskingPatterns()` ships a sane
  baseline.
- **Multi-cluster K8s Hub (T4)** — `k8s.Hub` holds one read-only client
  per kubeconfig context; every K8s tool now accepts an optional `context`
  arg and prepends a `CONTEXT` column when more than one is configured.
- **Namespace allow/deny middleware** — when a Permission Profile declares
  `namespaces.*`, K8s tools that take a namespace argument reject calls
  outside the allow set or in the deny set; the LLM gets a clear "namespace
  denied by profile" string and continues planning around it.
- **TUI `/scope` command (Layer 3)** — informational session-scope
  narrowing (`/scope ns=payments`, `/scope ctx=prod-eu`, `/scope reset`)
  surfaces a system-prompt addendum and renders an `scope=` segment in
  the TUI header.
- **TUI cost meter** — header `$<cumulative>` slot now reflects real
  cumulative input/output tokens and USD via the LLM provider's `Usage`
  channel.
- **`cloudy doctor` extensions** — additional informational checks for
  `cloudy home` (resolved state directory) and `egress proxy`
  (HTTPS_PROXY / HTTP_PROXY / NO_PROXY) make bastion environments legible.
- **Documentation** — `docs/BASTION.md`, `docs/PERMISSION_PROFILES.md`,
  README "v0.2 highlights" section.

### Changed
- `cloudy profile list/show` now refer to **Permission Profiles**. The
  v0.1 cluster-discovery dump moved to `cloudy profile cluster`.
- `internal/wiring.BuildRegistry` accepts `Contexts []string` and
  `Profile *permission.Profile`; it builds the multi-context Hub and
  applies tool/namespace narrowing in one place. Callers no longer need
  to call `permission.FilterRegistry` manually.
- `config.Path` / `config.ProfilePath` / `session.logsDir` resolution now
  honours `CLOUDY_HOME` ahead of `XDG_CONFIG_HOME` and `~/.cloudy`.

### Security
- All three independent read-only guards remain intact (HTTP transport
  method whitelist, K8s verb whitelist, ClusterRole RBAC). The new
  Permission Profile layer only narrows; it cannot widen Layer 1.

## v0.1.0 — 2026-05-13

Initial baseline. See repository history.
