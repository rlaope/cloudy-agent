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
executable bit, and prints a PATH-setup hint if needed. Re-run the
same line anytime to upgrade — the installer always pulls whatever
GitHub marks as `latest`.

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
make build
./cloudy --version
```

Either path leaves the binary discoverable as `cloudy` once the
install directory is on your `PATH`.

## First run

```sh
./cloudy
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

Four independent guards, any one of which would catch a mutation
attempt. Defense in depth, not a single chokepoint.

1. **HTTP `RoundTripper`** rejects every method other than `GET` /
   `HEAD` / `OPTIONS` before the request reaches the network.
2. **Kubernetes client wrapper** rejects verbs other than `get` /
   `list` / `watch` before the request reaches the apiserver.
3. **Bundled `ClusterRole`** (`manifests/rbac/`) only grants those
   verbs at the RBAC layer.
4. **Bastion reachability verbs** (`services/proxy: get`,
   `pods/portforward: create`) are the minimum required to reach HTTP
   and TCP backends through the apiserver and do not widen the
   mutation surface.

Mutating tools (delete, exec, patch, write-mode port-forward) are not
registered with the agent, so the LLM never sees them in its tool
catalog and cannot ask for them.

A separate **risk-rated approval gate** sits in front of tools that
*are* read-only but expensive enough to perturb the system they're
observing — STW JVM pauses, attached eBPF probes, long profiling
windows. The TUI surfaces a `y/N` banner; headless entry points
refuse them with a clear message. See [docs/SAFETY.md](docs/SAFETY.md).

## Backends cloudy understands

| Domain        | What it talks to                                   |
| ------------- | -------------------------------------------------- |
| Kubernetes    | apiserver (`get` / `list` / `watch` only)          |
| Metrics       | Prometheus, Thanos, VictoriaMetrics                |
| Logs          | Loki, Elasticsearch, OpenSearch                    |
| Traces        | Tempo, Jaeger                                      |
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

### Tool surface (45 tools across 10 groups)

Every probe the agent can call is a typed tool with a JSON schema.
Tools self-register at boot — perf, eBPF, and DB groups also gate on
binary / driver presence. Type `/tools` in the TUI to see what's
wired in your environment.

| Group | Tools (count) |
| ----- | ------------- |
| `k8s` (8) | `list_pods`, `list_nodes`, `list_namespaces`, `describe_pod`, `events`, `logs`, `top_pods`, `top_nodes` |
| `prom` (4) | `query`, `query_range`, `label_values`, `series` |
| `log` (7) | `loki_query_range`, `loki_labels`, `loki_label_values`, `loki_series`, `es_search`, `es_indices`, `es_cluster_health` |
| `trace` (5) | `tempo_get_trace`, `tempo_search`, `jaeger_services`, `jaeger_operations`, `jaeger_search_traces` |
| `db` (18) | Postgres: `pg_version`, `pg_stat_activity`, `pg_stat_database`, `pg_stat_replication`, `pg_locks`, `pg_top_table_size`. MySQL: `mysql_version`, `mysql_processlist`, `mysql_global_status`, `mysql_global_variables`, `mysql_engine_innodb_status`, `mysql_top_table_size`. Redis: `redis_info`, `redis_dbsize`, `redis_scan`, `redis_inspect_key`, `redis_slowlog`, `redis_client_list` |
| `perf` (4) | `rbspy_dump` (Ruby, always-on), `go_pprof_cpu`, `linux_perf_record`, `v8_inspector_cpu_profile` *(last three conditional on host binaries)* |
| `jvm` (4) | `jstat_gc`, `jcmd_gc`, `jcmd_thread_dump`, `async_profile` |
| `py` (2) | `spy_dump`, `spy_top_snapshot` |
| `gpu` (2) | `nvidia_smi`, `dcgm_metrics` |
| `ebpf` (5) | `biolatency`, `tcptop`, `tcprtt`, `execsnoop`, `bpftrace_oneliner` *(all RiskHigh; gated by the approval banner)* |

> **No `ruby` group.** rbspy is registered as `perf.rbspy_dump`. If
> you are looking for Ruby profiling in `/tools`, search `perf`.

### Skill playbooks (12 built-in)

Skills are curated multi-step playbooks the agent picks when a
question matches their triggers. They live in
[skills/](skills/) (mirrored under `internal/skills/skills/` for
embedding); you can override or add by dropping a `.md` file into
`~/.cloudy/skills/` — user files win on name conflicts.

| Skill | When it fires |
| ----- | ------------- |
| `cluster-recon` | "What's running in my cluster right now?" topology dump. |
| `k8s-incident` | First-pass triage for CrashLoopBackOff / Pending / OOMKilled / Eviction. |
| `crashloop-deep-dive` | Beyond exit codes — previous-container logs, probe audit, init-container ordering, traces. |
| `oom-killed-triage` | Container-limit vs. node-level OOM, sawtooth-vs-plateau working-set pattern, JVM heap flag check. |
| `log-spike-correlation` | Joins a Loki / ES error spike to Prom anomalies and pod events. |
| `trace-error-pivot` | Walk a p99 / error-rate regression down to the slow span in Tempo or Jaeger and back to the pod. |
| `db-latency-hunt` | PostgreSQL / MySQL / Redis read-only forensics for slow upstream DB calls. |
| `prom-explorer` | Interactive PromQL composition without prior knowledge of the metric schema. |
| `jvm-gc` | GC pause / heap-exhaustion / old-gen growth diagnosis. |
| `jvm-thread` | Deadlock, blocked threads, pool exhaustion. |
| `py-perf` | GIL contention, async-loop stalls, CPU bottlenecks. |
| `gpu-saturation` | GPU OOM, low utilization, thermal throttling. |

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
