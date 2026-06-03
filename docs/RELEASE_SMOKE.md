# Release Smoke QA

Run this after building or installing `cloudy` from a release branch.

## Local No-Credential Checks

```sh
cloudy --help
cloudy --version
cloudy skills --json
cloudy setup --auto --dry-run
cloudy doctor --json
cloudy tools --json
```

Expected results:

- help and version print without requiring a kubeconfig or model key;
- `skills --json` emits the built-in/user skill inventory;
- setup dry-run completes without writing files;
- doctor reports actionable failures instead of panicking;
- tools JSON includes skipped groups with reasons when no backends are configured.

## Optional Credentialed Checks

Use an isolated `$CLOUDY_HOME` and a read-only kubeconfig/profile.

```sh
cloudy setup --auto --dry-run --kubeconfig "$KUBECONFIG"
cloudy tools --json --kubeconfig "$KUBECONFIG"
cloudy ask --prompt "Summarize what tools are available without calling RiskHigh tools."
```

Expected results:

- configured read-only groups appear in `tools --json`;
- skipped groups explain missing prerequisites;
- one-shot `ask` starts with a configured model/provider or returns a clear setup/API-key error.

## Safety Checks

Targeted tests cover the non-negotiable read-only and approval contracts:

```sh
go test ./internal/transport ./internal/core/tools/cloud ./internal/core/agent
go test ./internal/ui/cli ./internal/setup ./internal/wiring
```

RiskHigh tools must be denied in headless mode unless an approver is installed.
Disallowed HTTP methods must fail through `transport.ReadOnlyRoundTripper`.

## Full Verification

```sh
make verify
```

In restricted sandboxes, `httptest` may fail to bind `127.0.0.1` or `::1`.
Treat that as an environment limitation and rerun full verification in a
normal shell before cutting a release.
