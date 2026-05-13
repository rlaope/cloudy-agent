# Changelog

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
