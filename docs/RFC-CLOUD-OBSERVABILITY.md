# RFC: Cloud-Provider Observability for cloudy (AWS / GCP / Azure)

- **Status**: Draft (pre-implementation)
- **Target**: v0.6.0 → (phased)
- **Owner**: rlaope
- **Last updated**: 2026-05-29

> TL;DR — Today cloudy is a deep read-only SRE agent for the *open-source*
> stack (k8s, Docker, Prometheus, Loki, Tempo, Jaeger, DBs) — 88 tools across
> 15 groups. It has **zero** cloud-provider observability: it can already
> *detect* which of AWS/GCP/Azure you are logged into
> (`internal/discovery/cloud_iam.go`) but cannot query a single CloudWatch
> metric. This RFC adds a read-only `cloud` tool group that reaches AWS, GCP,
> and Azure telemetry **by shelling out to the operator's already-configured
> `aws` / `gcloud` / `az` CLIs** — no new SDK dependencies, no stored secrets.
> The agent stays strictly read-only; cloud APIs become *another* read-only
> source the LLM can consult.

---

## 1. Problem statement

cloudy investigates clusters and hosts, but the moment an incident touches a
managed cloud service — an RDS failover, a CloudWatch-only metric, a Lambda
cold-start storm, a cost spike — the trail goes cold. The agent has:

- **0** cloud metric / log / trace query tools,
- **0** cloud inventory / managed-service health tools,
- **0** cost / FinOps visibility,
- a `change.recent` timeline that stops at k8s + Docker and never sees
  CloudTrail / Cloud Audit Logs / Azure Activity Log,
- a `cloud_iam.go` identity probe that is **detected but never wired to a
  tool** — a dormant capability.

### Current-state score — cloud observability readiness ≈ 3/10

| Dimension | Score | Note |
| --- | :---: | --- |
| Read-only security architecture | 9/10 | mutator-name panic + transport GET/HEAD/OPTIONS whitelist + permission profiles |
| Open-stack observability coverage | 9/10 | 88 tools / 15 groups |
| Cloud signal coverage (metric/log/trace/cost/inventory) | 1/10 | none |
| Multi-cloud breadth (AWS/GCP/Azure) | 2/10 | identity detection only, unwired |
| Cloud auth / credential model | 3/10 | no query-time auth, no short-lived-token story |
| Trend alignment (OTel / unified / FinOps / AI-SRE) | 5/10 | `correlate`/`change` align; cloud/OTel/FinOps absent |
| DevOps convenience (discovery → setup → inquiry) | 6/10 | framework strong, cloud not connected |

The *foundation* (registration, config, secrets, discovery, cross-signal
`correlate`) is strong, so the marginal cost of each cloud signal is low.

## 2. Trend context (2026)

- **OpenTelemetry is the converging standard**; AI-agent observability
  semantic conventions are emerging. cloudy already reads OTel backends
  (Tempo/Prom), so it can position as an OTel-friendly read-only agent.
- **Observability 2.0** = logs+metrics+traces in one context-rich pipeline —
  exactly what `correlate.workload` does; cloud signals just feed in.
- **FinOps is converging into AIOps** — cost-as-a-signal; a read-only cost
  reader fits cloudy's ethos perfectly.
- **AI-SRE shifts from dashboards to automated root-cause reasoning** —
  cloudy's existing direction.
- **CloudWatch now speaks PromQL + cross-source queries** — cloudy can reuse
  its `prom` mental model against CloudWatch.
- **Security baseline = OIDC / short-lived tokens / assume-role least
  privilege.** Leaning on the operator's ambient CLI credentials keeps
  secrets out of cloudy entirely.

## 3. Decisions (locked)

1. **Integration mechanism: CLI shell-out** (`aws`, `gcloud`, `az`) — extends
   the pattern already in `cloud_iam.go`. **Zero new dependencies.** Reuses
   the operator's existing login / SSO / assume-role / workload-identity
   session; cloudy never stores cloud secrets.
2. **First delivery: Phase 0 + Phase 1** (foundation + metrics). Metrics is
   the universal signal and reuses the PromQL mental model.

## 4. Security model — the critical design point

> **CLI shell-out bypasses `transport/readonly.go`.** The HTTP GET/HEAD/OPTIONS
> whitelist only guards Go `http.Client` traffic. A subprocess (`aws …`) does
> not pass through it. Read-only must therefore be re-established at three
> layers:

1. **Tool-name guard (unchanged).** Every `cloud.*` tool name uses a
   read-only verb; `assertReadOnlyName` still panics on mutator tokens at
   registration.
2. **CLI sub-command allowlist (NEW).** A central `cloudexec` helper accepts
   only an explicit allowlist of read-only sub-commands and a fixed flag set
   — e.g. `cloudwatch get-metric-data`, `logs filter-log-events`,
   `monitoring`, `monitor metrics list`. Anything not on the list is refused
   before exec. **No shell string interpolation** — args are passed as an
   `[]string` exec vector; no user/LLM-controlled value reaches a shell.
   Output is forced to JSON (`--output json` / `--format json`) and size-capped.
3. **Least-privilege IAM (documented, operator-enforced).** cloudy documents
   the minimal read-only role to attach and assumes nothing more:
   - **AWS**: `ReadOnlyAccess` or scoped `cloudwatch:GetMetricData`,
     `cloudwatch:ListMetrics`, `logs:FilterLogEvents`, `logs:StartQuery`/`GetQueryResults`.
   - **GCP**: `roles/monitoring.viewer` + `roles/logging.viewer`.
   - **Azure**: `Monitoring Reader` + `Reader`.

A timeout, an output byte-cap, and `argv`-only exec (no `sh -c`) are
mandatory in `cloudexec`. The allowlist is the security boundary — it gets a
dedicated test that asserts a mutating sub-command (e.g. `ec2 terminate-instances`)
is rejected.

## 5. Architecture

### 5.1 Config (`internal/config/config.go`)

```go
type Config struct {
    // … existing …
    CloudAWS   []AWSAccount   `yaml:"cloud_aws,omitempty"`
    CloudGCP   []GCPProject   `yaml:"cloud_gcp,omitempty"`
    CloudAzure []AzureAccount `yaml:"cloud_azure,omitempty"`
}

type AWSAccount   struct { Name, Region, Profile string } // Profile → AWS_PROFILE / --profile
type GCPProject   struct { Name, ProjectID, Configuration string } // gcloud --configuration
type AzureAccount struct { Name, SubscriptionID string }
```

No secret fields — credentials come from the CLI's own resolved session.
Empty config + a detected CLI identity (`cloud_iam.go`) can **auto-register**
a zero-config account for that provider (the DevX win below).

### 5.2 New group `internal/core/tools/cloud/`

Follows the proven `BuildClients` + `RegisterAll` template (see
`internal/core/tools/alert/register.go`). "Clients" here are validated
provider+account handles around the `cloudexec` helper, not SDK clients.

```
internal/core/tools/cloud/
├── register.go    // BuildClients(aws,gcp,azure) + RegisterAll + MarkSkipped("cloud", …)
├── cloudexec.go   // argv-only exec, sub-command allowlist, JSON+timeout+byte-cap  ← security core
├── aws.go              // cloud.aws_cw_list_metrics, cloud.aws_cw_get_metric_statistics
├── aws_logs.go         // cloud.aws_logs_{describe_groups,filter_events,insights_query}
├── aws_xray.go         // cloud.aws_xray_{trace_summaries,batch_get_traces,service_graph}
├── azure.go            // cloud.azure_monitor_{metric_definitions,metrics}
├── azure_logs.go       // cloud.azure_log_analytics_query
├── azure_appinsights.go// cloud.azure_appinsights_query
├── gcp_logs.go         // cloud.gcp_logging_read   (Cloud Logging only — see §9)
└── *_test.go           // stub-runner unit tests; allowlist refuses mutating verbs
```

### 5.3 Wiring (`internal/wiring/tools.go`)

Add `CloudAWS/CloudGCP/CloudAzure` to `Options`, then after `gitops.RegisterAll`:

```go
cloudClients, cloudSkips := cloud.BuildClients(opts.CloudAWS, opts.CloudGCP, opts.CloudAzure)
cloud.RegisterAll(reg, cloudClients, cloudSkips)
```

### 5.4 Discovery / setup (the zero-config DevX win)

Wire `discovery.ProbeCloudIdentities` into `RunDiscovery` so `/setup` surfaces
"AWS account 1234… detected via your `aws` CLI — enable cloud tools? [y]".
If the CLI is logged in, the matching `cloud.*` tools light up with **no
secret entry and no config edits**.

## 6. Phased plan (coverage axis, not MVP)

- **Phase 0 — Foundation & credentials.** `cloudexec` + allowlist + tests;
  config structs; wire `cloud_iam` into discovery/setup; document least-priv
  roles. *(this delivery)*
- **Phase 1 — Metrics.** CloudWatch (`get-metric-data`, PromQL/Metrics
  Insights), Cloud Monitoring, Azure Monitor. Reuse `prom` mental model.
  *(this delivery)*
- **Phase 2 — Logs.** *(delivered for AWS + Azure + GCP)* CloudWatch Logs
  (`describe-log-groups`, `filter-log-events`) + Logs Insights
  (`start-query`/`get-query-results`, wrapped synchronously); Azure Log
  Analytics KQL (`az monitor log-analytics query`); **GCP Cloud Logging
  (`gcloud logging read`, delivered) — see §9 for why logging is the only clean
  read-only gcloud signal.**
- **Phase 3 — Traces + topology.** *(delivered for AWS + Azure, see §10)* AWS
  X-Ray (trace summaries / batch-get-traces / service-graph) and Azure
  Application Insights KQL; GCP Cloud Trace deferred for the same reason as GCP
  metrics (no read-only gcloud command). Feeds `correlate`.
- **Phase 4 — Inventory / managed-service health.** Describe/List (RDS,
  CloudSQL, Azure SQL; Lambda, CloudRun, Functions; EKS, GKE, AKS) → extend
  `change.recent` across cloud.
- **Phase 5 — FinOps / cost.** Cost Explorer / Billing / Cost Management
  (read-only) → cost-anomaly inquiry.
- **Cross-cutting.** `correlate.workload` ingests cloud symptoms;
  `change.recent` ingests CloudTrail `LookupEvents` / Cloud Audit Logs /
  Azure Activity Log.

## 7. Non-goals

- No mutation surface, ever (no scaling, no deletes, no alarm creation).
- No vendor SaaS APM (Datadog/New Relic) — open-stack + first-party clouds only.
- No long-lived cloud secrets stored by cloudy.
- No bundling of the cloud CLIs — cloudy uses what the operator already has;
  a missing CLI is a graceful `MarkSkipped`, not an error.

## 8. Open questions

- ~~GCP metric reads via `gcloud` are awkward~~ **Resolved in §9.** `gcloud`
  has no read-only time-series read at all; GCP delivers Cloud Logging only.
- Multi-account/region fan-out ergonomics: one tool call per account vs an
  account selector argument. *(current: account selector arg, default when one
  account is configured.)*
- Caching CLI identity probes to avoid re-shelling on every discovery run.

## 9. GCP path decision (locked)

The original RFC flagged GCP metric reads as "awkward." Confirmed against the
2026 `gcloud` reference: it is not awkward, it is **absent**.

- **`gcloud monitoring`** manages dashboards / alerting policies / snoozes /
  uptime checks — there is **no `time-series list` (metric data read)**
  sub-command. Reading metric points requires the Monitoring API
  (`projects.timeSeries.list`) directly.
- **Cloud Trace** has no stable `gcloud trace` read command either; trace reads
  go through the Trace API (`projects.traces.list`).
- **`gcloud logging read`** *is* a clean, first-class read-only command with
  JSON output and the Logging query language — directly analogous to
  CloudWatch `filter-log-events`.

**Decision:** wire **GCP Cloud Logging only** (`cloud.gcp_logging_read`), and
keep GCP metric + trace **deferred**. We deliberately do **not** reach for the
REST APIs with `gcloud auth print-access-token`, because that would:
(a) break the "shell out to the operator's CLI, store no secrets" mechanism by
putting a bearer token in cloudy's process, and (b) introduce a bespoke
HTTP-signing path the other providers don't need. If GCP metric/trace coverage
becomes a priority, the cleanest future option is a raw Monitoring/Trace API
**GET** through cloudy's existing `transport/readonly.go` guard (which already
whitelists GET), authenticated by a short-lived token — tracked as a separate
RFC, not folded in here.

**Allowlist impact:** `gcloud` is added to `allowedSubcommands` with exactly one
entry, `logging read`. Because `gcloud logging read` takes its filter as a
trailing positional, cloud tools emit `logging read` immediately followed by
flags (`--project …`) and append the filter LAST, so `subcommandPrefix` stays
`logging read` and the allowlist match is exact. A unit test asserts a mutating
`gcloud compute instances delete` is refused.

## 10. Phase 3 — traces (AWS + Azure delivered, GCP deferred)

Traces are the third Observability-2.0 signal and the natural feed into
`correlate.workload`. Two of three providers expose clean read-only CLIs today;
both are now delivered.

### 10.1 AWS X-Ray (delivered)

All read-only, JSON output, fit the existing `awsAccount.baseArgs()` shape:

- `cloud.aws_xray_trace_summaries` → `aws xray get-trace-summaries`
  `--start-time --end-time [--filter-expression]`. Returns trace IDs + latency /
  error / fault annotations for a window. (Times are epoch seconds.)
- `cloud.aws_xray_batch_get_traces` → `aws xray batch-get-traces --trace-ids …`
  (≤5 IDs/call). Full segment documents for IDs surfaced by the summary tool —
  the documented two-step X-Ray workflow.
- `cloud.aws_xray_service_graph` → `aws xray get-service-graph
  --start-time --end-time`. Service-dependency topology + per-edge health; a
  strong `correlate` input.

Allowlist additions (read verbs only): `xray get-trace-summaries`,
`xray batch-get-traces`, `xray get-service-graph`.

### 10.2 Azure Application Insights (delivered)

Reuses the Azure account + KQL pattern already proven by
`cloud.azure_log_analytics_query`:

- `cloud.azure_appinsights_query` → `az monitor app-insights query --apps
  --analytics-query [--offset | --start-time/--end-time] [--resource-group]`
  against the `requests` / `dependencies` / `traces` tables. KQL is read-only by
  construction; the raw `{tables:[{rows}]}` response is surfaced.

Allowlist addition: `monitor app-insights query` (the `az monitor
app-insights` extension auto-installs on first use; a missing extension surfaces
as a tool error, matching the missing-CLI convention).

### 10.3 GCP Cloud Trace (deferred)

Same blocker as GCP metrics (§9): no read-only `gcloud trace` command. Deferred
to the future Monitoring/Trace-API-GET RFC.

### 10.4 Cross-cutting

`correlate.workload` gains an optional cloud-trace symptom source; the X-Ray
service graph and App Insights dependency table both map onto cloudy's existing
topology mental model. Wiring cloud traces into `correlate` is the remaining
cross-cutting follow-up (the tools exist; the symptom-source adapter does not
yet consume them).
