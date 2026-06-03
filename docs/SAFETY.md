# Safety model

cloudy is read-only by design and never mutates infrastructure. The
read-only contract is enforced by **four independent guards** at
different layers; on top of that, a **risk-rated approval gate** and
two observation-shaping hooks (log summary, alt-guidance) protect the
operator from expensive-but-legal calls and steer the LLM toward
cheaper alternatives.

Local cloudy-owned memory writes are outside the operational mutation surface:
`memory.record` is the only agent-callable local memory write tool, and it only
appends redacted environment facts to `memory.md`. HITL-approved incident case
cards are a separate operator-controlled local store under
`incident-memory/cards.jsonl`; approving or rejecting them never writes to
Kubernetes, cloud providers, databases, Prometheus, logs, traces, or other
monitored backends.

The incident case-card store is intentionally optimized for a modest,
Cloudy-owned local corpus. Cloudy serializes its own writes and caches reads by
file metadata, but external writers or much larger corpora should introduce a
stronger invalidation/indexing layer before relying on it as shared knowledge
infrastructure.

This document explains each layer, how they compose, and what
defense-in-depth means in practice.

---

## TL;DR

| Concern                                  | Guard                          | Layer          |
|------------------------------------------|--------------------------------|----------------|
| Outbound HTTP must be a read verb        | `transport.ReadOnlyRoundTripper` | Process       |
| ServiceAccount can only read             | `ClusterRole cloudy-readonly`  | Cluster        |
| Bastion proxy verbs are minimal          | RBAC `services/proxy: get`     | Cluster        |
| No mutation-named tool may register      | `tools.Registry` mutator panic | Process (boot) |
| Expensive read calls need consent        | `agent.ApprovalHook` (RiskHigh) | Agent         |
| Oversize log responses get summarized    | `agent.LogSummaryHook`         | Agent          |
| Blocked calls produce a useful hint      | `transport.readOnlyAlternative`| Agent ↔ LLM   |

K8s traffic is covered by the same `ReadOnlyRoundTripper` row: the K8s
client's `rest.Config.WrapTransport` is set to `transport.Wrap` in
`internal/tools/k8s/client.go`, so every K8s API call flows through the
HTTP whitelist just like Prometheus / Loki / Tempo do. There is no
separate "K8s verb wrapper" layer — the policy table for permissible
K8s verbs lives in `transport.AllowedKubeVerbs` for documentation and
in the bundled `ClusterRole` for enforcement at the cluster boundary.

The first three guards are the **read-only contract**: removing any one
of them does not make mutation possible, because the others remain. The
remaining four are **defense-in-depth and ergonomics**: they bound cost,
shape what the LLM sees, and make rejections informative rather than
opaque.

---

## Layer 1 — HTTP transport whitelist

[`internal/transport/readonly.go`](../internal/transport/readonly.go)

Every outbound HTTP request issued by cloudy passes through
`ReadOnlyRoundTripper`. It rejects any method outside an immutable
allowlist:

```go
var AllowedMethods = map[string]struct{}{
    http.MethodGet:     {},
    http.MethodHead:    {},
    http.MethodOptions: {},
}
```

`POST`, `PUT`, `PATCH`, `DELETE`, and any custom method return
`ErrReadOnlyViolation` before the wrapped transport is invoked. The
error message embeds the offending method, the target URL, and a
**per-method alternative hint** (see *Alt-guidance* below).

This guard applies to:

- Prometheus / Loki / Elasticsearch / Tempo / Jaeger queries
- pprof / V8 Inspector capture
- The apiserver `services/proxy` reachability path (so the
  whitelist applies end-to-end for in-cluster HTTP backends too)

The websocket dialer used by `perf.v8_inspector_cpu_profile` reuses
`ReadOnlyRoundTripper.DialContext`, so CDP traffic shares the same
network stack and read-only stance.

## Layer 2 — ClusterRole RBAC

[`manifests/rbac/base/clusterrole.yaml`](../manifests/rbac/base/clusterrole.yaml)

The bundled `ClusterRole` grants only:

- `get` / `list` / `watch` on core read targets (`pods`, `services`,
  `nodes`, `namespaces`, `events`, `configmaps`, …)
- `get` on `pods/log`
- `get` / `list` / `watch` on workload controllers (`apps`, `batch`)
- `get` / `list` on `metrics.k8s.io`

Secrets are intentionally absent. cloudy never reads Secret data; if a
specific deployment requires Secret metadata, add an overlay that
grants only `list` on secrets (no `get`) and rely on field masking.

## Layer 3 — Bastion reachability verbs

For the v0.4 bastion-friendly transports cloudy adds two narrow verbs:

```yaml
- apiGroups: [""]
  resources: ["services/proxy"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods/portforward"]
  verbs: ["create"]
```

These are the minimum required for the apiserver-proxy HTTP path and
the SPDY TCP-tunnel path respectively. Neither widens the mutation
surface — `services/proxy: get` routes a read-only HTTP request
through the apiserver, and `pods/portforward: create` opens a local
TCP tunnel without giving cloudy any verb on the workload itself.

### Documented exception: SPDY portforward upgrade

The portforward subresource is opened with `POST /api/v1/namespaces/{ns}/pods/{pod}/portforward`
because the K8s apiserver only accepts SPDY upgrade requests via POST.
This single POST is **not** routed through `ReadOnlyRoundTripper` (the
`spdy.RoundTripperFor(cfg)` constructor builds its own transport without
consulting `cfg.WrapTransport`).

The exception is bounded by construction:

- The POST only opens the upgrade handshake; once SPDY hands off to the
  multiplexed TCP stream, the bytes flowing through the tunnel are not
  HTTP — there is no HTTP method on the wire to check.
- The `pods/portforward: create` RBAC verb is the cluster-side fence:
  the apiserver itself will reject the POST if the ServiceAccount lacks
  that verb.
- No LLM-reachable tool dispatches `OpenPortForward`. The single caller
  is `internal/tools/db/client.go` for local-loopback DB-driver setup,
  and the target pod is resolved internally — never from LLM args.

This exception is enforced by a regression test in
`internal/transport/k8sportfwd_callers_test.go` that fails the build
if any new caller of `OpenPortForward` appears outside the audited
allow-list.

---

## Mutator-name registry panic

[`internal/tools/registry.go`](../internal/tools/registry.go)

`tools.Registry.Register` runs `assertReadOnlyName` on every tool
name. If the name contains a forbidden mutator verb at a token
boundary (split on `.` and `_`), registration panics with
`ErrMutatorTool` and a suggested rename:

```
tools: mutator tool rejected by read-only registry:
"k8s.create_pod" contains forbidden token "create" —
rename to a read-only verb (list/get/show/describe/inspect/query/top)
```

Tokens currently rejected:

```
create  update  delete  patch  apply  replace  drop  alter
insert  kill    restart scale  rollout exec    write post  put
```

Token-aware matching means legitimate names like
`mysql_top_table_size` (which contains "set" as a substring) are not
flagged. This guard is *boot-time*; a contributor cannot accidentally
add a mutation-sounding tool without triggering a panic in CI.

---

## Risk-rated approval gate

[`internal/tools/risk.go`](../internal/tools/risk.go) +
[`internal/agent/hook_approval.go`](../internal/agent/hook_approval.go)

The four read-only guards above make mutation impossible. They do not,
however, stop the LLM from issuing **expensive but legal** read calls:
a stop-the-world `jcmd class_histogram` on a 32 GB heap, a 60-second
`perf record`, an eBPF `biolatency` against every disk. These need
operator consent.

### The `RiskLevel` ladder

```go
const (
    RiskUnknown RiskLevel = iota  // zero value, treated as low
    RiskLow                       // cheap inspection, paginated lists
    RiskMedium                    // short profiling windows, wide queries
    RiskHigh                      // STW pause, long profile, probe, cluster scan
)
```

`RiskOf(tool)` checks three sources in order:

1. The tool's own `RiskRated.Risk()` method, if implemented.
2. The curated `riskByName` allowlist — currently:
   `jvm.async_profile`, `jvm.jcmd_gc`, `perf.linux_perf_record`,
   `perf.v8_inspector_cpu_profile`, `perf.go_pprof_cpu`,
   `perf.rbspy_dump`, `py.spy_top_snapshot`, `py.spy_dump`,
   `ebpf.biolatency`, `ebpf.tcprtt`, `ebpf.tcptop`, `ebpf.execsnoop`,
   `ebpf.bpftrace_oneliner`.
3. Fall back to `RiskLow`.

### The hook

`agent.ApprovalHook` runs `BeforeToolCall` for every tool dispatch. If
the resolved `RiskLevel` is `RiskHigh`, it invokes the injected
`Approver` and blocks until the operator responds. Lower-risk calls
pass through unchanged.

The hook reads the registry through a `func() *tools.Registry`
closure, so a registry hot-swap (e.g. `/setup` running mid-session)
is picked up automatically.

### Two `Approver` implementations

- **TUI approver** — `cloudy` (interactive). Surfaces a
  `y/N/Esc` banner with the tool name and arguments
  (`AgentEvent.Approval` + `ApprovalRequest{Tool, Args, Reply}`).
  Unrelated keypresses are swallowed while a decision is pending so
  the operator cannot accidentally type into the chat box.
- **`DenyApprover`** — non-interactive default for `cloudy ask` and
  any other headless entry point. Refuses every high-risk call with a
  message pointing the operator at the TUI:

  ```
  tool perf.linux_perf_record is rated RiskHigh and requires an
  interactive operator decision; run `cloudy` (TUI) to approve, or
  choose a lower-risk tool
  ```

  The denial returns an error rather than panicking — the LLM sees it
  as a tool-side failure and typically picks a lower-risk alternative
  on the next turn, which is the desired feedback loop.

### Why this is *not* one of the read-only guards

The approval gate sits in agent space, after the LLM has already chosen
a tool. If it were the only thing preventing mutation, a bug in
`ApprovalHook` (or an `Approver` that silently said yes) could let a
mutation through. Because the four contract layers below it already
make mutation impossible, the gate is free to focus on cost and impact
rather than on enforcement.

---

## Log summary hook

[`internal/agent/hook_log_summary.go`](../internal/agent/hook_log_summary.go)

Large log responses pollute the context window and dilute the actual
fault signal. `LogSummaryHook` compresses `log.*` tool observations
above `Options.MaxLogResponseBytes` (default **64 KB**) before they
reach the LLM:

- **Head** (first ¼ of the budget) — opening context
- **Exception/error windows** (middle ¼) — lines matching
  `(?i)(exception|caused by|panic|traceback|stack trace|fatal|error)`
  plus the 5 lines after each match, with adjacent-duplicate
  collapse for chatty stack traces
- **Tail** (last ½) — the most recent activity

Below the threshold the observation passes through unchanged —
head/tail clipping with exception extraction is more expensive than a
flat token-budget truncate, so it is reserved for cases where it
actually pays for itself.

The hook only fires for tool names beginning with `log.`. Other tool
families produce structured tables that do not benefit from regex
extraction.

---

## Alt-guidance: teaching the LLM to retry differently

[`internal/transport/readonly.go`](../internal/transport/readonly.go) +
[`internal/tools/registry.go`](../internal/tools/registry.go)

When a call is refused, the error string the LLM sees includes a
concrete alternative. Three places do this:

| Refusal point                       | Hint shown to the LLM                                                                                    |
|--------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `transport.ReadOnlyRoundTripper`     | `POST/PUT/PATCH → use a GET-based inspect/list/get tool instead of writing`                               |
|                                      | `DELETE → use a list/get tool to inspect what you would have removed`                                     |
|                                      | other → `switch to a read-only verb`                                                                       |
| `tools.Registry` mutator panic       | `rename to a read-only verb (list/get/show/describe/inspect/query/top)`                                   |
| `ApprovalHook` (`DenyApprover`)      | `requires an interactive operator decision; run cloudy (TUI) to approve, or choose a lower-risk tool`     |

The agent preamble also instructs the LLM **not to retry** on
`ErrReadOnlyViolation` or `ErrApprovalDenied` and to pick a different
tool on the next turn. The combination of typed error + concrete hint +
preamble instruction reliably steers the model toward a cheaper path
instead of looping.

---

## Field masking + Permission Profiles

Two earlier-release safety layers compose with everything above:

- **Field-level masking** —
  [`internal/permission/mask.go`](../internal/permission/mask.go)
  redacts secret keys (`password`, `token`, `api_key`, …) and value
  patterns (AWS access keys, JWT prefixes, GitHub PATs, OpenAI-style
  keys) from tool observations and free-form text. Applied after the
  log-summary hook so masking sees the compressed text and the
  context-window pollution stays bounded.
- **Permission Profiles** —
  [`docs/PERMISSION_PROFILES.md`](PERMISSION_PROFILES.md) narrows the
  tool registry and namespace allow/deny before any skill activation.
  A profile cannot widen anything; it only restricts. Profiles
  resolved before `ApprovalHook` runs — so a tool that a profile
  disallows is never even considered for approval.

Ordering at runtime:

```
LLM picks a tool
  → Permission Profile allow/deny           (registry-level narrow)
  → tools.Registry mutator-name assertion   (boot-time, but stale path)
  → ApprovalHook: RiskOf(tool) gate         (high → operator decision)
  → tool.Run executes
  → LogSummaryHook compresses log.* observation
  → Masker redacts secrets in observation text
  → transport.ReadOnlyRoundTripper enforces HTTP method
  → ClusterRole RBAC enforces server-side
```

---

## Active probing: `synthetic.http_check`

Almost every cloudy tool *passively reads* telemetry that already exists.
The one exception is `synthetic.http_check`, which makes an **outbound
GET/HEAD request to a URL the agent chooses** to test reachability,
latency, and TLS-cert expiry. This is the only tool that emits traffic
to an LLM-selected destination, so its exposure is documented here.

What constrains it:

- **Verb**: the tool issues only GET/HEAD, and every hop (including
  redirects) flows through `transport.ReadOnlyRoundTripper`, which
  refuses any non-read method. No probe can mutate anything.
- **Link-local / metadata guard**: a dial-time control function refuses
  connections to link-local addresses (`169.254.0.0/16`, `fe80::/10`).
  This blocks the cloud-metadata endpoint `169.254.169.254`, which
  serves IAM credentials over a plain GET — the one egress the verb
  contract cannot defend. The guard runs on the post-DNS IP, so it also
  defeats DNS-rebinding and a redirect aimed at metadata.
- **Timeout**: bounded (default 10s, hard max 30s) so a probe cannot
  hang the session.

What it does **not** restrict: loopback and private/RFC1918 ranges stay
reachable on purpose — probing an internal service is the tool's whole
point, and cloudy's host can already reach them. The residual exposure
is therefore *egress to an attacker-chosen public or internal URL* (data
egress / internal recon, not mutation). To fence it in a hardened
deployment, point `HTTPS_PROXY` / `NO_PROXY` at an audit-logging egress
proxy (see [BASTION.md](BASTION.md)), or drop the `synthetic` group via a
Permission Profile tool-deny.

---

## Threat model: what cloudy is *not* protecting against

- **A malicious operator on the bastion**. cloudy reads whatever its
  ServiceAccount and configured backends let it read. Use cluster RBAC
  + Permission Profiles + audit logs to constrain operators.
- **A compromised LLM provider**. cloudy honours the responses it gets
  back; the approval gate and per-call rate limits bound damage, but a
  provider that returns malicious tool-call payloads can still drive
  the agent to issue any read-only call it has access to.
- **Side channels from observations**. Tool output is shown to the LLM
  verbatim (after masking and summarisation). If a Pod label contains
  PII, the LLM sees it. Use masking patterns and Permission Profiles
  to scope what cloudy reads in the first place.

For these residual risks, run cloudy on a dedicated bastion under a
dedicated ServiceAccount, scoped by Permission Profile per operator,
with `HTTPS_PROXY` pointed at the audit-logging egress proxy.
