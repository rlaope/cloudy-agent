# Changelog

## v0.5.0 — Unreleased

### Added — Operator skill workflows
- **`service-health` broad triage skill** — maps vague service/user-impact
  symptoms onto golden signals, telemetry correlation, runtime state, recent
  changes, queues, synthetic probes, and managed-cloud evidence, then routes to
  the one deep read-only specialist. `triage-orchestrator` now hands broad
  health cases to this skill and is recommended only when setup finds a
  reachable Kubernetes context.
- **`app-runtime-health` application-layer triage skill** — maps service p95/p99,
  framework latency, and language-runtime symptoms onto HTTP RED metrics,
  traces, logs, Kubernetes state, and existing Go/Node/Python/JVM/.NET/Ruby/native
  runtime playbooks. Setup recommends it automatically when Prometheus and a
  reachable Kubernetes context are discovered.
- **`frontend-web-health` frontend/web-app UX triage skill** — maps webpage and
  browser-facing symptoms onto Core Web Vitals (LCP/INP/CLS), browser
  JavaScript/hydration errors, asset delivery, CDN/cache, synthetic reachability,
  SSR/API traces, and backend p95/p99 handoff. Setup now detects frontend-ish
  pods and Ingress hosts, then recommends this skill only when a reachable web
  surface also has Prometheus or OpenTelemetry evidence.

### Added — Tool surface (45 → 112, 10 → 16 groups)
- **Cloud observability group (`cloud`, 24 tools)** — read-only AWS + Azure + GCP
  telemetry via the operator's already-configured `aws` / `az` / `gcloud` CLIs.
  cloudy stores no cloud secrets; credentials resolve from the CLI's own chain.
  Read-only is re-established for the shell-out path (which bypasses the HTTP
  transport guard) by an argv-only `cloudexec` sub-command allowlist.
  Configured via `cloud_aws:` / `cloud_gcp:` / `cloud_azure:`.
  - **Metrics (Phase 1)** — `cloud.aws_cw_list_metrics`,
    `cloud.aws_cw_get_metric_statistics` (CloudWatch);
    `cloud.azure_monitor_metric_definitions`, `cloud.azure_monitor_metrics`
    (Azure Monitor).
  - **Logs (Phase 2)** — `cloud.aws_logs_describe_groups`,
    `cloud.aws_logs_filter_events`, `cloud.aws_logs_insights_query`
    (CloudWatch Logs + Logs Insights start/poll);
    `cloud.azure_log_analytics_query` (Azure Log Analytics KQL);
    `cloud.gcp_logging_read` (`gcloud logging read`, Logging query language).
  - **Traces (Phase 3)** — `cloud.aws_xray_trace_summaries`,
    `cloud.aws_xray_batch_get_traces`, `cloud.aws_xray_service_graph` (X-Ray
    trace summaries, full segments, service-dependency graph);
    `cloud.azure_appinsights_query` (Application Insights KQL over
    requests/dependencies/traces).
  - **Inventory / managed-service health (Phase 4)** —
    `cloud.aws_rds_describe_instances`, `cloud.aws_lambda_list_functions`,
    `cloud.aws_eks_list_clusters` (AWS);
    `cloud.azure_sql_server_list`, `cloud.azure_functionapp_list`,
    `cloud.azure_aks_list` (Azure);
    `cloud.gcp_sql_instances_list`, `cloud.gcp_run_services_list`,
    `cloud.gcp_container_clusters_list` (GCP). Managed databases, serverless
    functions, and managed-Kubernetes inventory across all three providers via
    read-only Describe/List verbs. GCP inventory list commands are first-class
    read-only `gcloud` verbs (unlike its absent metric/trace reads).
  - **FinOps / cost (Phase 5)** — `cloud.aws_ce_cost_and_usage` (Cost Explorer
    `get-cost-and-usage`: date window, granularity, metrics, optional group-by
    dimension); `cloud.azure_consumption_usage` (`az consumption usage list`:
    per-resource pre-tax cost with a summed total). GCP cost is deferred — like
    its metric/trace reads, `gcloud` has no clean cost-data read (billing data
    lives in the Billing API / BigQuery export).
  - **GCP path locked** — Cloud Logging is the only clean read-only `gcloud`
    *telemetry* signal; metric, trace, and cost reads stay deferred (gcloud
    exposes no time-series / trace / cost-data read command). Inventory list
    verbs (`sql/run/container … list`) are available and wired. See §9–§12 of
    the RFC.
  - Phases 0–5 of docs/RFC-CLOUD-OBSERVABILITY.md (AWS + Azure traces and cost;
    GCP trace + cost deferred).
  - **Cross-cutting: `correlate.workload` cloud-trace symptoms** — AWS X-Ray
    error/slow traces (scoped to the workload via the `service(...)` filter) now
    fold onto the correlate evidence timeline as `trace_error` / `trace_slow`
    symptoms (Source `cloud_trace`), built by the wiring layer as a plain
    `change.ChangeSource` so `correlate` stays decoupled from `cloud`. Azure App
    Insights (needs an app id) and GCP Cloud Trace (no gcloud command) deferred.
  - **Cross-cutting: `change.recent` cloud audit events** — AWS CloudTrail
    (`cloudtrail lookup-events`, ResourceName-filtered), GCP Cloud Audit Logs
    (reuses `gcloud logging read` with a cloudaudit + resourceName filter), and
    Azure Activity Log (`monitor activity-log list`, client-side workload
    filter) now fold onto the change timeline as `cloud_audit` events with a
    provider-tagged source, built by the wiring layer as a plain
    `change.ChangeSource` so `change` stays decoupled from `cloud`. New
    read-only allowlist entries: `cloudtrail lookup-events`,
    `monitor activity-log list`.
  - **Cross-cutting: `correlate.workload` cloud-audit causes** — the same cloud
    control-plane audit events now also feed `correlate.workload` as ranked
    candidate *causes*: `cloud_audit` is registered in `changeKinds` with weight
    `0.8` (above `scale`/restart, below a workload deploy or Argo `sync`), and the
    wiring layer passes `cloud.NewAuditChangeSource` into `correlate.RegisterAll`.
    A cloud-audit-only setup (GCP/Azure with no X-Ray) now lights up `correlate`.
    Review follow-ups: the correlate registration guard now also recognises
    Elasticsearch and Tempo symptom-only setups (previously skipped despite
    `RegisterAll` accepting them); candidate-cause `share %` is computed among the
    shown top-N so a burst of `cloud_audit` events can no longer dilute the
    leader's percentage; exact-duplicate changes are de-duplicated before ranking;
    and the `context` arg is documented as not selecting the cloud audit account.
- **K8s workload tools (10)** — `list_deployments`, `list_statefulsets`,
  `list_daemonsets`, `list_jobs`, `list_cronjobs`, `list_services`,
  `list_ingresses`, `list_hpa`, `list_pdbs`, `list_networkpolicies`.
  Covers `apps/v1`, `batch/v1`, `networking.k8s.io/v1`, `autoscaling/v2`,
  `policy/v1`. (#52)
- **CRD-generic dynamic-client reader (2)** — `list_crds` and `list_cr`
  via `client-go/dynamic` with `unstructured.Unstructured`. One pair of
  tools unlocks Argo Rollouts, KEDA scaledobjects, cert-manager,
  Sloth SLOs, Gateway API, ServiceMonitor, etc. without adding typed
  bindings for each CRD. Dotted-path field projection so the LLM never
  consumes a 50KB+ Unstructured object. (#61)
- **Alertmanager + Prom rules reader (3)** — `alert.list_active`,
  `alert.list_silences` (Alertmanager v2), `alert.list_rules`
  (Prometheus rules API). Answers "what's actually paging right now". (#54)
- **Argo CD reader (3)** — `gitops.argo_list_apps`, `argo_app_status`,
  `argo_app_history`. Answers "what changed in the last 30 minutes". (#54)

### Added — Orchestrator-agnostic change / metric / log + correlation
- **Change & deploy inquiry (`change.recent`)** — one read-only timeline of
  recent rollouts / image bumps / scale / restart events for a workload
  across **both Kubernetes and Docker** hosts. Adds a read-only Docker
  client adapter and a `docker_hosts` config field. (#100)
- **Docker container metrics (`metric.container_stats`)** — read-only CPU /
  memory / network / block-IO via the Docker one-shot stats API (cgroup v1
  and v2 aware). Kubernetes metrics remain in `prom` + `k8s.top_*`. (#101)
- **Docker container logs (`log.container`)** — tail plus an error-line
  summary off the Docker daemon, demultiplexing stdout/stderr and reading
  TTY containers raw. (#102)
- **Cross-signal correlation (`correlate.workload`)** — merges the change
  timeline (k8s + Docker) with Argo CD sync history, then folds in
  metric / log / trace symptoms (Prometheus breach, Loki error bursts,
  Jaeger error/slow spans) and names a candidate cause: the most recent
  change before the earliest symptom. (#103)
- All four are read-only and RiskLow. The Docker side gates on
  `docker_hosts`; symptom sources gate on their backends (Tempo traces and
  Elasticsearch logs deferred).

### Fixed
- `cloudy tools` now reports the same registry that `cloudy ask` runs, by
  routing through `wiring.Rebuild`. It previously hand-built `Options` and
  omitted `docker_hosts` / `argocd` / `alertmanager`, so it under-reported
  the `change` / `metric` / `log.container` / `gitops` / `alert` groups.

### Added — Skill playbooks (7 → 24)
- **`incident-context`** — joins active alerts + recent Argo syncs + pod
  restarts + Argo Rollouts/KEDA CR state into a fixed-shape situational
  summary with explicit confidence rubric. (#54)
- **`log-spike-correlation`**, **`trace-error-pivot`**,
  **`db-latency-hunt`**, **`oom-killed-triage`**,
  **`crashloop-deep-dive`** — five new SRE skills closing gaps that
  every existing tool group needed a playbook for. (#45)
- **SRE skill-library expansion (13 → 24)** — incident/operations
  playbooks (`triage-orchestrator`, `deploy-regression`,
  `capacity-scheduling`, `network-connectivity`, `slo-burn`); application
  runtime-engine playbooks (`go-runtime`, `node-runtime`, `ruby-runtime`,
  `dotnet-runtime`, `native-perf`); and AI-inference serving
  (`ai-inference` — vLLM / Triton / TGI / TorchServe). All compose
  existing tools only — no new tool groups. Skills are now sourced
  solely from the embedded `internal/core/skills/builtin/`; the duplicate
  top-level `skills/` mirror was removed to end the silent drift between
  the two copies. (#94, #95)

### Added — TUI quality
- **Typewriter playback** — assistant tokens buffer through a per-frame
  rune drain instead of arriving in raw SSE bursts, so multi-token
  responses read as a steady cadence regardless of upstream chunking.
  Multi-byte UTF-8 (Korean, emoji) survives the playback boundary. (#42)
- **Queued user-echo chip** — operator's submitted prompt lives as a
  pre-rendered chip directly above the prompt until the agent's first
  event of the turn, then slides into the stream history. Stacks
  multiple submits while the agent is busy. (#45)
- **Mouse wheel scrolls the transcript** instead of being hijacked by
  the prompt's history navigation (was an arrow-key passthrough). (#48)
- **Native terminal scrollback** — finished turns are committed to the
  terminal's real scrollback via `tea.Println` rather than living in an
  in-memory viewport, so the mouse wheel scrolls the whole conversation
  and drag-to-copy works; the header prints once at the top and the live
  cost readout moves to the pinned footer. A new question mid-playback
  finalises the prior reply first, ending the dump/re-render artifact.
  `CLOUDY_FULLSCREEN=1` keeps the alt-screen in-app viewport. (#97)
- **Stream → prompt margin + braille spinner** so the reply does not
  butt against the prompt border and the thinking row reads as alive
  during silent gaps between tool results. (#48)
- **System preamble teaches cloudy itself** — the LLM can now answer
  meta-questions (`what is /setup?`, `what skills do you have?`) from
  in-band context. Adds a skill catalog injection point so RAG-style
  providers slot in cleanly later. (#47)
- **Stream-inline `/setup` and `/login` wizards** with arrow-picker
  multi-select plus an interactive model picker. (#41, #35)

### Added — Self-update path
- **In-process `cloudy update` / `/update`** atomically replaces the
  running binary with the latest GitHub release. Verifies SHA-256
  against the per-asset companion file before chmod/rename. (#44, #58)
- **`install.sh` one-liner** with the same SHA-256 verification
  contract; refuses HTML error pages, falls back to `shasum -a 256`
  when `sha256sum` is unavailable. (#43, #58)

### Added — Security hardening (v0.5 adversarial audit)
- **`MaskingHook` wired into the agent data path** — `permission.Masker`
  had been a published API since v0.4 with zero production call sites.
  Connection strings with embedded passwords reached the LLM verbatim.
  Now every tool observation passes through the active profile's
  KeyRegex / ValueRegex redaction before the prompt is assembled.
  (Audit M-1, #57)
- **SPDY portforward exception documented + bounded** — the SPDY
  upgrade POSTs outside `ReadOnlyRoundTripper`. Documented in
  SAFETY.md, inline `SECURITY NOTE` at the construction site, and
  `TestOpenPortForward_CallerAllowList` fails the build if a new
  caller appears outside the audited allow-list. (Audit H-1, #57)
- **Mutator-token blocklist extended** with `recreate`, `purge`,
  `evict`, `cordon`, `drain`, `taint`, `truncate`, `terminate`.
  (Audit M-2, #57)
- **`~/.cloudy/profiles/` mode 0755 → 0700** — profile YAML can
  disclose namespace allow/deny patterns and tool ACLs that benefit
  from not being world-readable. (Audit M-3, #57)
- **MySQL DSN URL-escapes the password** so `@:/?` in operator-
  supplied credentials does not split the DSN at the wrong boundary.
  (Audit L-1, #58)
- **`~/.cloudy/secrets` writes sorted** so `git diff`-style review
  and integrity tracking are stable across `Add()` calls. (Audit L-2, #58)
- **`secrets.Load` warns on malformed lines** instead of silently
  skipping — truncated API-key pastes no longer run cloudy
  unauthenticated without notice. (Audit L-3, #58)
- **Self-update SHA-256 verification** (already listed above) closes
  the "asset corrupted in flight" gap that the 4-byte magic-byte
  check missed. (Audit L-4, #58)

### Added — Structure & observability
- **`wiring.Rebuild` single owner** for the build-registry + replace
  sequence; three callsites collapsed into one (`cmd/main.go`,
  `setup/wizard.go`, `tui/setupchat.go`). Also fixed a latent bug
  where the boot registry was under-calling `BuildRegistry` and
  silently dropping Databases / Logs / Tracing / Pprof / NodeInspectors
  until the first `/setup` ran. (#50)
- **Startup tool-inventory banner** — at boot cloudy prints one stderr
  line naming the wired groups plus skipped groups with their reason,
  so operators do not need to type `/tools` to discover that, say, the
  trace group was dropped because no Tempo/Jaeger endpoint was
  discovered. (#49)
- **`tui/app.go` split**: 1,770 → 1,203 LOC (−32%). Extracted splash /
  playback / thinking / selfupdate / styles into sibling files (#51),
  then agent_runner controller (`runAgent`, `pumpAgentCmd`,
  `applyAgentEvent`, event types) (#59).
- **`SkillProvider` interface scaffold** — `*skills.Skill` becomes
  `StaticSkill` behind an interface so future `RAGSkill` / `RunbookSkill`
  implementations slot in without changing every agent call site.
  Zero behaviour change today. (#63, opens path to v0.6 RAG layer.)

### Added — Tests
- **`internal/cli/` 0 → 18 tests** covering dispatcher, `parseInto`,
  `update`/`doctor` identity, `environmentChecks`. (#53)
- **K8s workload tools 0 → 11 tests** covering each new list tool's
  namespace filter / column rendering, plus a constructor-Name-drift
  guard. (#55)
- Plus regression tests for `Registry.Filter()` `llmAlias` carry-over
  (#49), `MaskingHook` redaction contract (#57), `OpenPortForward`
  caller allow-list (#57, #59), SHA-256 verification (#58),
  `SkillProvider` round-trip (#63), and Tempo metrics-generator
  service-graph + RED tools (#64).

### Added — Tool surface from this PR set (open, not yet merged at time of writing)
- **`trace.service_graph` + `trace.route_red`** via Tempo's
  metrics-generator (`traces_service_graph_*`, `traces_spanmetrics_*`).
  Service topology + per-route RED metrics without adding a new
  backend. (#64)

### Added — Docs
- **`docs/RFC-RAG.md`** — pre-implementation PRD for the v0.6 RAG /
  knowledge-base layer (`~/.cloudy/knowledge/`). Phased rollout
  v0.6.0 → v0.7.0. (#60)
- **`docs/SAFETY.md` honesty pass** — corrected the "4 enforcement
  layers" claim to actual 3 enforcement + 2 hardening guards.
  Removed `transport.CheckVerb` dead code referenced as a fictional
  fourth layer. (#50, #56)

### Fixed
- **`Registry.Filter()` lost `llmAlias`** — skill-narrowed registries
  silently broke sanitized-name resolution for Anthropic / OpenAI /
  Google / Moonshot providers; recovered tool dispatches now resolve
  through the alias map. (#49)
- **`fix(ci): gofmt`** regression in the typewriter playback PR. (#46)

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
