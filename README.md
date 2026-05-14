# cloudy

Read-only multi-cluster SRE monitoring AI CLI agent.

`cloudy` runs in your terminal. Type plain language, get answers about
Kubernetes / JVM / Python / GPU workloads across multiple clusters.
It never mutates infrastructure: every call is `GET` / `LIST` / `WATCH`,
enforced at three layers (HTTP transport, Kubernetes verb whitelist,
ClusterRole RBAC).

## Status

v0.4 in development. v0.2 / v0.3 released; v0.4 brings auto-discovery and TUI-integrated /setup.

## v0.4 Highlights

- **Auto-discovery in /setup**: A single `/setup` slash command inside the TUI scans the configured K8s contexts and proposes every detected Prometheus / Loki / Elasticsearch / Tempo / Jaeger / Postgres / MySQL / Redis / pprof / V8 inspector endpoint. The user picks them from a checkbox and inputs any required credentials inline; cloudy.yaml is generated, not hand-edited.
- **Bastion-friendly reachability**: HTTP backends are reached via the K8s apiserver's `services/proxy` (no VPN required), and TCP databases via in-process SPDY port-forward; the existing read-only contract (`GET/HEAD/OPTIONS`) is preserved end-to-end.
- **Live registry hot-swap**: After `/setup` the new backends are usable immediately — no `cloudy` restart. The agent picks up the new tool catalog on the next user question.
- **First-launch banner**: Launching `cloudy` with no config shows a `cloudy` banner + the three command hints (`⚙ /setup`, `? /help`, `⏎ ask`); compact one-liner on subsequent launches.

## v0.2 Highlights

- **T2 Bastion Deployment**: Run cloudy on a secure bastion host. Respects `CLOUDY_HOME` for multi-user isolation and `HTTPS_PROXY` for corporate networks. See [docs/BASTION.md](docs/BASTION.md) for deployment guide and systemd unit.
- **Permission Profiles**: Tool/namespace allow-deny rules plus field-level masking (passwords, tokens, keys). Per-session limits (log lines, profiling duration, token budget, USD spend). See [docs/PERMISSION_PROFILES.md](docs/PERMISSION_PROFILES.md).
- **Multi-Cluster Context Override**: Pass `contexts:` in config to pin K8s clusters per tool, or use `@prod-eu` prefix in a query to override context for a single turn.

## Why

`kubectl` + Prometheus + `jcmd` + `py-spy` + `nvidia-smi` collapsed into one
natural-language interface, with a TUI that feels like Claude Code.

## Install

Pre-1.0: build from source.

```sh
git clone https://github.com/rlaope/cloudy.git
cd cloudy
make build
./cloudy --version
```

## Quickstart

```sh
cloudy                             # open TUI; first launch shows /setup banner
cloudy ask "왜 결제 서비스 응답시간이 느려?"   # one-shot mode
cloudy setup                       # equivalent non-interactive setup (CI)

cloudy profile list                # list all available profiles
cloudy profile use payments-sre    # activate a profile
cloudy profile new my-profile      # create a new profile interactively
cloudy profile none                # clear active profile
cloudy profile cluster             # show RBAC permissions from current context
```

## Safety

Four independent guards keep cloudy read-only:

1. HTTP `RoundTripper` rejects every method other than `GET`/`HEAD`/`OPTIONS`.
2. Kubernetes client wrapper rejects verbs other than `get`/`list`/`watch`.
3. The bundled `ClusterRole` (`manifests/rbac/`) only grants those verbs.
4. Bastion reachability verbs (`services/proxy: get`, `pods/portforward: create`) are the minimum required for HTTP and TCP backends and do not widen the mutation surface.

Mutating tools are not registered, so the LLM never sees them.

## Models

Bring your own key. Supported providers: OpenAI (gpt-*), Anthropic (claude-*),
Google (gemini-*), Moonshot (kimi-*), and any OpenAI-compatible endpoint
(vLLM, Ollama, OpenRouter). Switch models inside a session with `/model`.

LLM adapters honor `HTTP_PROXY` and `HTTPS_PROXY` environment variables via Go's
default transport, making cloudy compatible with corporate proxies and bastions.

## License

MIT.
