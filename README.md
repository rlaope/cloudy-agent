# cloudy

Read-only multi-cluster SRE agent in your terminal. Ask plain-language
questions about Kubernetes / JVM / Python / GPU workloads across every
cluster you have credentials for, and get answers stitched together
from `kubectl`, Prometheus, Loki, jcmd, py-spy, nvidia-smi, perf, eBPF,
and friends — without typing any of them.

cloudy never mutates infrastructure. Every call is `GET` / `LIST` /
`WATCH`, enforced at four layers.

```
 ██████╗██╗      ██████╗ ██╗   ██╗██████╗ ██╗   ██╗
██╔════╝██║     ██╔═══██╗██║   ██║██╔══██╗╚██╗ ██╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║ ╚████╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║  ╚██╔╝
╚██████╗███████╗╚██████╔╝╚██████╔╝██████╔╝   ██║
 ╚═════╝╚══════╝ ╚═════╝  ╚═════╝ ╚═════╝    ╚═╝

  ⚙  /setup    discover clusters & backends
  ?  /help     keyboard shortcuts
  ⏎           or just ask a question
```

## What it does

You type:

> Why did checkout-service p99 spike around 2am yesterday?

cloudy plans the investigation, runs the relevant read-only probes
(metrics, logs, traces, profiles), and explains what it found. The
agent picks tools from a typed registry — Kubernetes, Prometheus,
Loki / ES, Tempo / Jaeger, pprof, async-profiler, py-spy, NVIDIA SMI,
perf, eBPF — based on the question, not on a fixed script.

## Install

**One-liner (macOS, Linux — amd64 + arm64):**

```sh
curl -fsSL https://raw.githubusercontent.com/rlaope/cloudy/master/install.sh | sh
```

Drops the latest GitHub release into `~/.local/bin/cloudy`, sets the
executable bit, and prints a PATH-setup hint if needed. Once the
installer finishes, the binary is reachable as plain `cloudy` from
any directory (no `./` prefix — it lives on `$PATH`, not in your
working directory). Re-run the same one-liner anytime to upgrade,
or use `cloudy update` from inside the TUI — the installer always
pulls whatever GitHub marks as `latest`.

Override the install location with `CLOUDY_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/rlaope/cloudy/master/install.sh \
  | CLOUDY_INSTALL_DIR=/usr/local/bin sh
```

**Build from source** (Windows, contributors, anything off the
release matrix):

```sh
git clone https://github.com/rlaope/cloudy.git
cd cloudy
make build         # produces ./cloudy in the repo root
./cloudy --version # quick smoke test from the build dir
sudo mv cloudy /usr/local/bin/   # or move it onto your PATH any other way
cloudy --version   # now reachable as a bare command
```

Either install path leaves the binary reachable as plain `cloudy`
from any directory once it is on your `PATH`.

## First run

```sh
cloudy
```

The TUI opens. Two commands get you to the first question:

1. **`/setup`** — scans your kubeconfig contexts, auto-discovers
   Prometheus / Loki / Elasticsearch / Tempo / Jaeger / Postgres /
   MySQL / Redis / pprof / V8 inspector endpoints, lets you pick which
   to enable inline, then writes `~/.cloudy/config.yaml` plus a
   `profile.yaml` snapshot of the scan. No restart.
2. **`/login`** — picks an LLM provider (Anthropic / OpenAI / Google /
   Moonshot) with arrow keys and saves the API key to
   `~/.cloudy/secrets` (mode `0600`). The chosen model is active
   immediately; `/model <id>` swaps mid-session.

Then ask:

```
 > Why does the payments-api pod keep getting OOMKilled?
```

Headless / CI usage:

```sh
cloudy ask "Why is the checkout service slow right now?"   # one-shot
cloudy setup                                # non-interactive setup
cloudy profile use payments-sre             # activate a permission profile
cloudy profile cluster                      # show RBAC for current context
```

## Read-only by design

Three independent enforcement layers plus boot-time and runtime
hardening. Defense in depth, not a single chokepoint.

1. **HTTP `RoundTripper`** rejects every method other than `GET` /
   `HEAD` / `OPTIONS` before the request reaches the network. The
   K8s client honours this too — `rest.Config.WrapTransport` is set
   to the same wrapper, so apiserver calls share the HTTP whitelist
   end-to-end.
2. **Bundled `ClusterRole`** (`manifests/rbac/`) only grants `get`
   / `list` / `watch` (plus the two narrow bastion verbs below) at
   the RBAC layer — the cluster itself refuses anything else even
   if a guard in cloudy were bypassed.
3. **Bastion reachability verbs** (`services/proxy: get`,
   `pods/portforward: create`) are the minimum required to reach
   HTTP and TCP backends through the apiserver and do not widen
   the mutation surface.

On top of those layers cloudy adds two hardening guards:

- The **`tools.Registry` mutator-name assertion** panics at boot if
  any registered tool name looks like a write (`create_*`,
  `delete_*`, `patch_*`, ...). Mutating tools (`exec`, `delete`,
  `patch`, write-mode port-forward) are never registered, so the
  LLM never sees them in its tool catalogue and cannot ask for them.
- A **risk-rated approval gate** sits in front of tools that are
  read-only but expensive enough to perturb the system they're
  observing — STW JVM pauses, attached eBPF probes, long profiling
  windows. The TUI surfaces a `y/N` banner; headless entry points
  refuse them with a clear message. See
  [docs/SAFETY.md](docs/SAFETY.md).

## Backends cloudy understands

| Domain        | What it talks to                                   |
| ------------- | -------------------------------------------------- |
| Kubernetes    | apiserver (`get` / `list` / `watch` only)          |
| Docker        | daemon API (container list / inspect / stats / logs) |
| Metrics       | Prometheus, Thanos, VictoriaMetrics                |
| Logs          | Loki, Elasticsearch, OpenSearch                    |
| Traces        | Tempo, Jaeger                                      |
| Change        | k8s rollouts / images / scale, Docker containers, Argo CD sync |
| Correlation   | cross-signal change↔symptom evidence timeline      |
| JVM           | jcmd, async-profiler (heap / cpu / alloc)          |
| Python        | py-spy (sampling / dump-stacks)                    |
| Ruby          | rbspy (sampling) — registered as `perf.rbspy_dump` |
| GPU           | NVIDIA SMI, DCGM                                   |
| Kernel        | perf, eBPF (read-only probes only)                 |
| Databases     | Postgres / MySQL / Redis (read-only query subset)  |

HTTP backends are reached via the K8s apiserver's `services/proxy`,
TCP backends via in-process SPDY port-forward. A single
`kubectl`-reachable cluster is enough — no VPN, no per-service
ingress.

### Tool surface (88 tools across 15 groups)

Every probe the agent can call is a typed tool with a JSON schema.
Tools self-register at boot — perf, eBPF, and DB groups also gate on
binary / driver presence. Type `/tools` in the TUI to see what's
wired in your environment.

| Group | Tools (count) |
| ----- | ------------- |
| `k8s` (20) | `list_pods`, `list_nodes`, `list_namespaces`, `describe_pod`, `events`, `logs`, `top_pods`, `top_nodes`, `list_deployments`, `list_statefulsets`, `list_daemonsets`, `list_jobs`, `list_cronjobs`, `list_services`, `list_ingresses`, `list_hpa`, `list_pdbs`, `list_networkpolicies`, `list_crds`, `list_cr` *(CRD-generic dynamic-client reader; unlocks Argo Rollouts, KEDA, cert-manager, Gateway API, Sloth SLOs, ServiceMonitor, etc. in one tool)* |
| `prom` (4) | `query`, `query_range`, `label_values`, `series` |
| `log` (8) | `loki_query_range`, `loki_labels`, `loki_label_values`, `loki_series`, `es_search`, `es_indices`, `es_cluster_health`, `container` *(Docker container logs; registers when `docker_hosts` is configured)* |
| `trace` (7) | `tempo_get_trace`, `tempo_search`, `service_graph` *(Tempo metrics-generator service-graph edges)*, `route_red` *(Tempo metrics-generator per-route RED)*, `jaeger_services`, `jaeger_operations`, `jaeger_search_traces` |
| `alert` (3) | `list_active`, `list_silences` *(Alertmanager v2)*, `list_rules` *(Prometheus rules API)* |
| `gitops` (3) | `argo_list_apps`, `argo_app_status`, `argo_app_history` *(Argo CD v1 API)* |
| `change` (1) | `recent` *(orchestrator-agnostic deploy / image / scale / rollout timeline across Kubernetes **and** Docker; registers when k8s or `docker_hosts` is available)* |
| `metric` (1) | `container_stats` *(read-only Docker container CPU / mem / net / block-IO; k8s metrics live in `prom` + `k8s.top_*`; needs `docker_hosts`)* |
| `correlate` (1) | `workload` *(cross-signal evidence timeline — change history + metric / log / trace symptoms — with a candidate-cause that aligns the earliest symptom to the change before it; folds in Argo CD sync)* |
| `db` (18) | Postgres: `pg_version`, `pg_stat_activity`, `pg_stat_database`, `pg_stat_replication`, `pg_locks`, `pg_top_table_size`. MySQL: `mysql_version`, `mysql_processlist`, `mysql_global_status`, `mysql_global_variables`, `mysql_engine_innodb_status`, `mysql_top_table_size`. Redis: `redis_info`, `redis_dbsize`, `redis_scan`, `redis_inspect_key`, `redis_slowlog`, `redis_client_list` |
| `perf` (9) | `rbspy_dump` (Ruby, always-on), `go_pprof_cpu`, `go_pprof_goroutine`, `go_pprof_heap`, `go_pprof_allocs`, `go_pprof_threadcreate` *(Go pprof; gated on `pprof` endpoints)*, `v8_inspector_targets`, `v8_inspector_cpu_profile` *(Node.js V8; gated on `node_inspectors`)*, `linux_perf_record` *(gated on host `perf` binary)* |
| `jvm` (4) | `jstat_gc`, `jcmd_gc`, `jcmd_thread_dump`, `async_profile` |
| `py` (2) | `spy_dump`, `spy_top_snapshot` |
| `gpu` (2) | `nvidia_smi`, `dcgm_metrics` |
| `ebpf` (5) | `biolatency`, `tcptop`, `tcprtt`, `execsnoop`, `bpftrace_oneliner` *(all RiskHigh; gated by the approval banner)* |

> **No `ruby` group.** rbspy is registered as `perf.rbspy_dump`. If
> you are looking for Ruby profiling in `/tools`, search `perf`.

### Skill playbooks (24 built-in)

Skills are curated multi-step playbooks the agent picks when a
question matches their triggers. They live in
[`internal/core/skills/builtin/`](internal/core/skills/builtin/),
embedded into the binary via `//go:embed`; you can override or add by
dropping a `.md` file into `~/.cloudy/skills/` — user files win on name
conflicts.

| Skill | When it fires |
| ----- | ------------- |
| `triage-orchestrator` | "Just got paged — where do I start?" Breadth-first scan, ranks a hypothesis, hands off to the deep skill. |
| `cluster-recon` | "What's running in my cluster right now?" topology dump. |
| `incident-context` | "What's burning right now?" — cross-references firing alerts with recent Argo CD syncs and pod restarts. |
| `deploy-regression` | "Did the last deploy break it?" Aligns Argo sync timestamps with error/latency onset and names the revision to roll back to. |
| `k8s-incident` | First-pass triage for CrashLoopBackOff / Pending / OOMKilled / Eviction. |
| `crashloop-deep-dive` | Beyond exit codes — previous-container logs, probe audit, init-container ordering, traces. |
| `oom-killed-triage` | Container-limit vs. node-level OOM, sawtooth-vs-plateau working-set pattern, JVM heap flag check. |
| `capacity-scheduling` | Why pods stay Pending — capacity vs. taints/affinity vs. stuck autoscaler / HPA-maxed / PDB block. |
| `network-connectivity` | Why workload A can't reach B — walks DNS → Service/endpoints → NetworkPolicy → Ingress / mesh sidecar. |
| `slo-burn` | SLO error-budget burn — multi-window multi-burn-rate, time-to-exhaustion, page-now vs. ticket. |
| `log-spike-correlation` | Joins a Loki / ES error spike to Prom anomalies and pod events. |
| `trace-error-pivot` | Walk a p99 / error-rate regression down to the slow span in Tempo or Jaeger and back to the pod. |
| `db-latency-hunt` | PostgreSQL / MySQL / Redis read-only forensics for slow upstream DB calls. |
| `prom-explorer` | Interactive PromQL composition without prior knowledge of the metric schema. |
| `go-runtime` | Go runtime — goroutine leaks, GC pacing (GOGC), scheduler latency, pprof CPU hot paths. |
| `node-runtime` | Node.js / V8 — event-loop lag, scavenge vs. mark-sweep GC, TurboFan deopt, V8 Inspector CPU profile. |
| `jvm-gc` | GC pause / heap-exhaustion / old-gen growth diagnosis. |
| `jvm-thread` | Deadlock, blocked threads, pool exhaustion. |
| `py-perf` | GIL contention, async-loop stalls, CPU bottlenecks. |
| `ruby-runtime` | Ruby / Rails — GVL contention, generational GC pressure, YJIT, rbspy stack sampling. |
| `dotnet-runtime` | .NET / CLR — gen0/1/2 + LOH GC, Server-vs-Workstation mode, ThreadPool starvation, tiered JIT. |
| `native-perf` | C / C++ / Rust — Linux perf hot paths, cache misses, branch mispredict, lock contention, missed codegen. |
| `gpu-saturation` | GPU OOM, low utilization, thermal throttling. |
| `ai-inference` | LLM/ML serving — TTFT / inter-token latency, throughput, KV-cache & batch saturation, GPU util (vLLM / Triton / TGI / TorchServe). |

## LLM providers

Bring your own key. Picked at `/login`, swappable mid-session with
`/model <id>`.

| Provider             | Env var               | Model prefix       |
| -------------------- | --------------------- | ------------------ |
| Anthropic            | `ANTHROPIC_API_KEY`   | `claude-*`         |
| OpenAI               | `OPENAI_API_KEY`      | `gpt-*`, `o1-*`    |
| Google Gemini        | `GOOGLE_API_KEY`      | `gemini-*`         |
| Moonshot / Kimi      | `MOONSHOT_API_KEY`    | `kimi-*`           |
| OpenAI-compatible    | `OPENAI_BASE_URL`     | any                |

OpenAI-compatible covers Ollama, vLLM, LM Studio, OpenRouter, and any
in-network gateway that speaks the same wire format. LLM adapters
honor `HTTP_PROXY` / `HTTPS_PROXY` for corporate egress.

## Configuration

cloudy resolves its state directory in this order: `$CLOUDY_HOME` →
`$XDG_CONFIG_HOME/cloudy` → `$HOME/.cloudy`. Layout:

| Path                       | What                                                                                                    |
| -------------------------- | ------------------------------------------------------------------------------------------------------- |
| `config.yaml`              | Clusters, backends, model, safety limits. Generated by `/setup`; hand-editing supported.                |
| `profile.yaml`             | Snapshot of the last `/setup` scan (discovered endpoints + selection state).                            |
| `secrets`                  | Dotenv-format API keys (mode `0600`). Written by `/login`.                                              |
| `profiles/<name>.yaml`     | **Permission profile** bundles: tool/namespace allow-deny rules and field masking (passwords, tokens). |
| `active_profile`           | Pointer to the currently selected permission profile (managed by `cloudy profile use`).                 |

See [docs/PERMISSION_PROFILES.md](docs/PERMISSION_PROFILES.md) for the
permission-profile schema.

## Documentation

- [docs/SAFETY.md](docs/SAFETY.md) — read-only guards, risk-rated approval gate, threat model
- [docs/AUTO_DISCOVERY.md](docs/AUTO_DISCOVERY.md) — what `/setup` probes, where, and how findings map to config
- [docs/BASTION.md](docs/BASTION.md) — deploying cloudy on a shared bastion (per-user state, systemd, proxy)
- [docs/PERMISSION_PROFILES.md](docs/PERMISSION_PROFILES.md) — profile schema, masking rules, per-session limits
- [CHANGELOG.md](CHANGELOG.md) — release notes

## Project status

Pre-1.0. Build from source. Public API and config schema may shift
between minor versions; pin a tag if that matters for you.

## License

MIT.
