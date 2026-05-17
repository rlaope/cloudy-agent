# Auto-discovery & TUI `/setup`

v0.4 introduces a `/setup` slash command inside the TUI that scans every
selected Kubernetes context, proposes every detected observability backend,
collects credentials inline, and rebuilds the live tool registry without a
process restart. `cloudy.yaml` is generated, not hand-edited.

This document describes the wizard flow, how detectors decide what to
propose, where secrets live, and how to recover when discovery misses
something.

---

## TL;DR

```text
cloudy             # launches TUI; first run shows the welcome banner
/setup             # runs the wizard inline (no restart between steps)
```

The wizard is also runnable non-interactively:

```sh
cloudy setup       # automated path (CI / scripted bootstrap)
```

---

## The 7-step wizard

Each step is a `WizardModel` sub-screen in
[`internal/setup/wizard.go`](../internal/setup/wizard.go):

| # | Step          | What it does                                                                                  |
|---|---------------|-----------------------------------------------------------------------------------------------|
| 1 | `kubeconfig`  | Lists every context in `$KUBECONFIG` (or the default file). User picks one or more.            |
| 2 | `scan`        | Runs `setup.ScanContext` per selected context concurrently with a 30 s timeout.                |
| 3 | `discovered`  | Full-screen checkbox grouped by `Finding.Kind`. Empty groups are not toggleable.               |
| 4 | `credentials` | Streaming Q&A for each selected backend that needs auth. Pasted values echo as `*`.            |
| 5 | `hints`       | Optional loop for ad-hoc external endpoints (`kind URL [auth]`).                               |
| 6 | `fill-in`     | LLM default model + safety limits (max tokens/session, max USD/day, allow-secrets).            |
| 7 | `skills`      | Checkbox over `Recommend(profile, builtin)` — pre-checks the skills that match the scan.       |
|   | `save`        | Writes `~/.cloudy/cloudy.yaml` + `~/.cloudy/profile.yaml`, rebuilds the registry, hot-swaps it. |

The header reads `Step 1/7`, `Step 2/7`, … because `save` is a write step,
not a user-input step.

### Step 3 — discovered findings

The checkbox is grouped by `Finding.Kind` (e.g. `prometheus`, `loki`,
`postgres`). All members of a kind are toggled together. The display also
shows the count and a comma-joined list of `namespace/service-name`
labels per finding so the user can recognise them in the list.

### Step 4 — credentials

Each finding whose `AuthHint.Kind` is `basic`, `bearer`, or `password`
produces one prompt. Three input modes:

| You type                    | What happens                                                                            |
|-----------------------------|------------------------------------------------------------------------------------------|
| `$EXISTING_ENV`             | Wizard stores `EXISTING_ENV` as the env-var reference. No value is captured.            |
| `<literal value>`           | Wizard auto-generates `CLOUDY_<KIND>_<NAME>_PWD`, calls `secrets.Add(key, value)`.       |
| Empty / `Esc`               | Skips this credential. The backend will register without auth (may fail later).         |

The literal path is the one that writes to disk (see *Secrets* below).
The `$ENV_VAR` path never resolves the env var during the wizard — only
the *name* is stored in `cloudy.yaml`, so the value can rotate without
re-running `/setup`.

### Step 5 — external hints

For backends the cluster scan misses (e.g. a SaaS Grafana Cloud, an
external managed Postgres), enter one line at a time:

```text
prom    https://prom.example.com:9090         BEARER_ENV
loki    https://loki.example.com              user:BASIC_PASS_ENV
postgres postgres://app@db.example.com/orders  PG_PWD_ENV
```

Recognised kinds: `prom` / `prometheus`, `loki`, `elasticsearch`,
`tempo`, `jaeger`, `pprof`, `v8`, `postgres`, `mysql`, `redis`. Unknown
kinds are rejected inline; the cursor stays on the prompt so you can
retry. Type `done` or press `Esc` to advance.

---

## The detector model

[`internal/discovery/coordinator.go`](../internal/discovery/coordinator.go)
fan-outs every registered `Detector` against a shared `Env` with a 30 s
default deadline. Findings are aggregated and stable-sorted by
(`Group`, `Kind`, `Source.Context`, `Source.Namespace`, `Source.ServiceName`,
`Source.ExternalURL`) — so the order in the checkbox is deterministic
across runs.

A detector that panics is recovered and treated as zero findings: one
buggy backend cannot crash `/setup`.

### Self-registration

Backend packages live under `internal/tools/<kind>/` and register their
detectors from `init()`:

```go
// internal/tools/prom/detector.go
func init() { discovery.Register(promDetector{}) }
```

This is intentional — `discovery` must not import `internal/tools/*`,
otherwise we get the cycle `tools → discovery → tools`.

### Per-backend recognition signals

| Detector | Signals                                                                                       |
|----------|-----------------------------------------------------------------------------------------------|
| `prom`   | Service label `app.kubernetes.io/name=prometheus`, port `9090` or named `web`.                  |
| `log`    | Loki: `name=loki` + `3100`. Elasticsearch: `name=elasticsearch` + `9200`.                       |
| `trace`  | Tempo: `name=tempo` + `3200`. Jaeger: `name=jaeger-query` + `16686`.                            |
| `db`     | Postgres: `name=postgresql` + `5432`. MySQL: `name=mysql` + `3306`. Redis: `name=redis` + `6379`. |
| `perf`   | pprof: `name=*` + named port `pprof`. V8 Inspector: named port `inspector` (9229).               |

A detector contributes a `Finding` only when both the label match and
the port heuristic agree. External hints from `cloudy.yaml` are added
unconditionally with `Source.External = true`.

---

## Credentials lifecycle: `~/.cloudy/secrets`

The wizard never writes secret material into `cloudy.yaml`. Pasted
values go through [`internal/secrets/store.go`](../internal/secrets/store.go).

**Resolution order** (mirrors `config.Path()`):

1. `$CLOUDY_HOME/secrets`
2. `$XDG_CONFIG_HOME/cloudy/secrets`
3. `~/.cloudy/secrets`

**File format** — dotenv, one `KEY=VALUE` per line, mode `0600`. The
parent directory is created with mode `0700`. Writes are atomic
(temp file in the same directory + `rename`). Keys must match
`^[A-Z_][A-Z0-9_]*$`; values containing `\n` or `\r` are rejected.

**At boot** — `secrets.Load()` (called from `cmd/main.go`) reads the
file and calls `os.Setenv` for each pair so the existing `*_env` config
fields keep working.

**Key naming** — auto-generated names follow
`CLOUDY_<KIND>_<NAME>_PWD`, both segments sanitised to `[A-Za-z0-9_]`.
Example: `prometheus-main` in namespace `monitoring` becomes
`CLOUDY_PROMETHEUS_PROMETHEUS_MAIN_PWD`.

---

## Bastion reachability

Two transport layers let cloudy reach in-cluster workloads from a
bastion *outside* the cluster without a VPN:

- **HTTP backends** — `transport.ServiceProxy`
  ([`internal/transport/k8sproxy.go`](../internal/transport/k8sproxy.go))
  rewrites `http://svc.ns:port/path` as the apiserver
  `services/proxy` URL and reuses the apiserver-authenticated
  `http.Client`, wrapped in `ReadOnlyRoundTripper`. The
  `GET/HEAD/OPTIONS` whitelist applies end-to-end.
- **TCP databases** — `transport.OpenPortForward`
  ([`internal/transport/k8sportfwd.go`](../internal/transport/k8sportfwd.go))
  opens a SPDY port-forward in-process and returns a
  `localhost:<auto>` address. `db.BuildClients` recognises the
  `k8s://<context>/<ns>/<svc>:<port>` DSN scheme and dials through
  this tunnel transparently.

Both paths require the additional RBAC verbs in
[`manifests/rbac/base/clusterrole.yaml`](../manifests/rbac/base/clusterrole.yaml):

```yaml
- apiGroups: [""]
  resources: ["services/proxy"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods/portforward"]
  verbs: ["create"]
```

These are the minimum required for the two reachability paths and do
not widen cloudy's mutation surface.

---

## Hot-swap

After step 7 the wizard calls
[`wiring.BuildRegistry`](../internal/wiring/registry.go) with the
freshly-written config and replaces the live registry via
`wiring.Replace`. The agent reads the registry through
`Options.RegistryFn func() *tools.Registry` rather than a frozen
pointer, so the very next user question sees the new tool catalog.

No `cloudy` restart is required.

---

## Manual `cloudy.yaml` edits

Hand-editing remains supported for advanced use cases. The recommended
flow is:

1. Run `/setup` to produce a baseline.
2. Edit `~/.cloudy/cloudy.yaml` (or the equivalent path under
   `$CLOUDY_HOME`) to add what discovery missed.
3. Restart cloudy (manual edits do not currently trigger a hot-swap).

If you maintain `cloudy.yaml` by hand, you can still seed external
backends through `discovery.hints` in the same file — that section is
read by every detector in addition to its K8s scan.

---

## Troubleshooting

**`/setup` reports zero findings for a backend that exists.**
The Service label match is strict (`app.kubernetes.io/name=<expected>`).
A custom Helm chart may use a different label scheme. Use step 5 (hints)
to add the URL by hand, or expose the expected label on your Service.

**`services/proxy: forbidden`.**
The bundled ClusterRole is missing from the cluster. Apply
`manifests/rbac/base/` (or its kustomize overlay) and rebind the
ServiceAccount cloudy is running under.

**`pods/portforward: forbidden`.**
Same fix as above — the DB tunnel path requires
`pods/portforward: create`.

**`secrets.Add: invalid key`.**
Auto-generated keys conform to `^[A-Z_][A-Z0-9_]*$`. If the underlying
backend name contains exotic characters, rename the Service or use
step 5 hints with your own env-var name.

**Wizard wrote credentials but the next call still fails with
`authentication required`.**
Re-check that `cloudy.yaml` references the env-var name the wizard
generated (e.g. `password_env: CLOUDY_POSTGRES_ORDERS_PWD`). The value
itself lives in `~/.cloudy/secrets` and is loaded at boot — if you
exported a same-named env var by hand earlier, the boot-time
`os.Setenv` from the file silently wins.
