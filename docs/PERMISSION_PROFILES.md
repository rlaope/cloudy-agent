# Permission Profiles

## Overview

Permission Profiles implement Layer 2 in the 3-Layer Scope Model. While Layer 1 (RBAC ClusterRole) sets the absolute upper bound of what the Kubernetes API will permit, and Layer 3 (TUI `/scope` command) allows ad-hoc session-level narrowing, Permission Profiles enable operators to define persistent, role-based restrictions across tool execution, namespace visibility, and field-level masking.

A Profile is a YAML file that narrows what an already read-only cloudy instance may observe. It controls:
- Which Kubernetes contexts the agent may query
- Which namespaces and resources are visible
- Which tools the LLM can invoke
- Per-session limits (log lines, profiling duration, token budget, USD spend)
- Field-level masking to redact sensitive data (passwords, tokens, keys, secrets)

Profiles ensure that Layer 1 (RBAC) can never be widened—only narrowed. Skills, in turn, cannot widen what a profile allows; they filter further if needed.

## Permission Profile Schema

A Permission Profile is stored at `~/.cloudy/profiles/<name>.yaml` (or `$CLOUDY_HOME/profiles/<name>.yaml` if CLOUDY_HOME is set). The schema includes:

```yaml
name: <profile-name>              # Required. Must match filename.
description: <text>               # Optional. One-line purpose.
contexts: [<list>]                # Optional. Allowed kubeconfig contexts (empty = any).
namespaces:
  allow: [<list>]                 # Glob patterns (trailing * OK). Empty = all.
  deny: [<list>]                  # Glob patterns. Deny always wins.
tools:
  allow: [<list>]                 # Tool names (e.g. "k8s.*", "prom.query").
  deny: [<list>]                  # Tool names. Deny always wins.
limits:
  max_log_lines: <int>            # Cap per kubectl logs call. 0 = use global default.
  max_profile_seconds: <int>      # Max duration for async_profile / py-spy. 0 = global.
  max_tokens_per_session: <int>   # Total tokens before session ends. 0 = no cap.
  max_usd_per_day: <float>        # Daily spend limit across all sessions. 0 = no cap.
masking:
  key_regex: [<list>]             # Case-insensitive key patterns (e.g. ["password",".*_token","api_?key"]).
  value_regex: [<list>]           # Regexes for sensitive values (JWTs, AWS keys, emails).
```

## Worked Example: Payments SRE Profile

```yaml
name: payments-readonly
description: Payments domain SRE — read-only access to production workloads, no sensitive data.
contexts: [prod-eu, prod-us]
namespaces:
  allow:
    - payments
    - payments-*
  deny:
    - kube-system
    - kube-node-lease
tools:
  allow:
    - k8s.list_pods
    - k8s.describe_pod
    - k8s.logs
    - k8s.events
    - k8s.top_pods
    - k8s.top_nodes
    - prom.query
    - prom.query_range
    - prom.label_values
    - jvm.jcmd_gc
    - jvm.jstat_gc
  deny:
    - jvm.async_profile
    - py.spy_dump
    - py.spy_top_snapshot
limits:
  max_log_lines: 2000
  max_profile_seconds: 0
  max_tokens_per_session: 200000
  max_usd_per_day: 10.0
masking:
  key_regex:
    - password
    - ".*_token"
    - api_?key
    - secret
    - ".*_credential"
  value_regex:
    - "eyJ[A-Za-z0-9_=-]{20,}\\."
    - "AKIA[0-9A-Z]{16}"
    - "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"
```

The `masking.key_regex` patterns match (case-insensitively) against JSON/YAML keys and redact their values. The `value_regex` patterns match entire stringified values and redact them if they match—useful for catching JWTs, AWS access keys, and email addresses.

Default masking patterns are exported from `permission.DefaultMaskingPatterns()` in the code and include common sensitive fields (password, token, secret, key, credential, auth, bearer, etc.).

## Subcommands

### `cloudy profile new <name>`

Creates a new profile interactively. Prompts for:
- Allowed contexts
- Namespace allow/deny lists
- Tool allow/deny lists
- Limits (log lines, profile duration, token budget, daily spend)
- Masking patterns

Saves to `~/.cloudy/profiles/<name>.yaml` with mode 0600.

### `cloudy profile list`

Lists all available profiles (sorted alphabetically) with one-line descriptions.

```
PROFILE              DESCRIPTION
payments-readonly    Payments domain SRE — read-only access
oncall               On-call rotation — broad visibility, no async_profile
ml-ops-debug         ML platform debugging — async_profile + GPU tools allowed
```

### `cloudy profile show <name>`

Displays the full YAML content of a profile, validating schema and rendering human-readable descriptions of restrictions.

### `cloudy profile use <name>`

Activates the named profile. Writes the profile name to `~/.cloudy/active_profile` (mode 0600) and immediately applies its restrictions to the TUI (if running). If the profile does not exist, returns an error.

Alternatively, set the environment variable `CLOUDY_PROFILE=<name>` to activate a profile for a single session without modifying the marker file.

### `cloudy profile none`

Clears the active profile marker, returning to the default (no profile) state. Equivalent to `CLOUDY_PROFILE=""` or deleting `~/.cloudy/active_profile`.

### `cloudy profile cluster`

Displays the cluster-level Permission Profile as inferred from the RBAC ClusterRole bound to the current ServiceAccount. This is a v0.1 debug command that shows what the Kubernetes API itself permits, before any cloudy Profile filtering.

## Profile Resolution Order

When cloudy starts, it resolves the active profile in this order:

1. **Environment variable**: `$CLOUDY_PROFILE` (if set and non-empty).
2. **Marker file**: `~/.cloudy/active_profile` (if exists and readable).
3. **None**: If neither is set, no profile applies (all tools/namespaces available, subject to RBAC).

Environment variables take precedence, allowing temporary overrides without modifying the marker file.

## Interaction with Skills

Permission Profiles form Layer 2; Skills can only narrow further, never widen.

When a Skill is activated:
1. The Profile (Layer 2) filters the Tool Registry, removing denied tools.
2. The Skill then applies its own `allowed_tools` list, further filtering to only what the skill requires.
3. The LLM never sees tools outside this intersection.

If a tool is denied by the Profile, the Skill cannot make it available, even if the Skill requests it. Any such mismatch is logged as a warning.

## Session-Level Narrowing (Layer 3)

The TUI `/scope` command allows temporary, session-only restriction without modifying the persisted Profile:

```
/scope ns=payments
/scope ctx=prod-eu
/scope reset
```

These changes apply only to the current session and do not persist. The next session resumes with the Profile's settings. Today `/scope` is a prompt-level namespace/context narrowing aid, not a hard tool-filtering layer; persistent tool allow/deny enforcement belongs in the Profile.

## Field Masking

### How It Works

Field masking is applied in two ways:

1. **Key-based masking**: Any JSON/YAML value whose key matches one of the `masking.key_regex` patterns is replaced with `[REDACTED]` (or a placeholder message).

2. **Value-based masking**: Any value (regardless of key) that matches one of the `masking.value_regex` regexes is replaced with `[REDACTED]`.

Matching is case-insensitive for keys and case-sensitive for values. All masking happens before the observation is presented to the LLM and before logs are written.

### Default Patterns

From `permission.DefaultMaskingPatterns()`:

**Key patterns**:
- `password`, `passwd`, `pwd`
- `token`, `auth_token`, `access_token`, `refresh_token`, `bearer`
- `api_key`, `apikey`, `api-key`, `api_secret`
- `secret`, `aws_secret_access_key`
- `credential`, `credentials`
- `authorization`, `auth`
- `key` (but not "public_key")

**Value patterns**:
- JWT format: `eyJ[A-Za-z0-9_=-]{20,}\.`
- AWS access key: `AKIA[0-9A-Z]{16}`
- Email (optional): `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`

### Custom Patterns

Users can extend or override defaults in their Profile. For example, if your environment uses a custom API key prefix:

```yaml
masking:
  key_regex:
    - password
    - custom_api_token
  value_regex:
    - "CUSTOM[0-9A-Z]{32}"
    - "eyJ[A-Za-z0-9_=-]{20,}\\."
```

## Practical Advice

### Organizing Profiles

Keep profiles in version control under a compliance or security repo:

```
compliance-repo/
  profiles/
    oncall.yaml
    payments-sre.yaml
    ml-ops.yaml
    admin.yaml
```

### Profile-to-Role Mapping

Deploy one profile per role or team:

- **`oncall.yaml`**: broad visibility, no destructive-looking tools (async_profile, py-spy disabled).
- **`payments-sre.yaml`**: payments namespaces only, no secrets, limited profiling.
- **`ml-ops.yaml`**: ML namespaces, GPU tools enabled, profiling allowed.
- **`admin.yaml`**: all contexts, all tools (but RBAC still gates).

### Matching Kubeconfig Contexts

Ensure the `contexts:` list in a Profile matches the kubeconfig context names:

```yaml
contexts: [prod-eu, prod-us, staging-eu]
```

If the user switches to a context not in this list, cloudy returns a clear error.

### Per-Role Kubeconfig + Profile

A common pattern: each role gets a dedicated kubeconfig and matching Profile.

```bash
export KUBECONFIG=~/.kube/payments-sre-config
export CLOUDY_PROFILE=payments-sre
cloudy
```

This ensures that both the Kubernetes client and cloudy itself are scoped to the role.

## Acceptance Criteria

- Profile deny rules prevent tools from appearing in the LLM tool catalogue.
- Field masking redacts sensitive values before LLM observation.
- `/scope` session-level narrowing does not persist across sessions.
- RBAC (Layer 1) and Profile (Layer 2) contradictions always resolve to the more restrictive option.
- `cloudy profile lint <file>` validates schema, glob patterns, and regex patterns.
