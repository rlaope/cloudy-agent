# cloudy

Read-only multi-cluster SRE monitoring AI CLI agent.

`cloudy` runs in your terminal. Type plain language, get answers about
Kubernetes / JVM / Python / GPU workloads across multiple clusters.
It never mutates infrastructure: every call is `GET` / `LIST` / `WATCH`,
enforced at three layers (HTTP transport, Kubernetes verb whitelist,
ClusterRole RBAC).

## Status

Early development. v0.1.0 in progress. See `.omc/plans/` for the design.

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
cloudy setup        # discover clusters, generate ~/.cloudy/{config,profile}.yaml
cloudy              # full-screen TUI
cloudy ask "왜 결제 서비스 응답시간이 느려?"   # one-shot mode
```

## Safety

Three independent guards keep cloudy read-only:

1. HTTP `RoundTripper` rejects every method other than `GET`/`HEAD`/`OPTIONS`.
2. Kubernetes client wrapper rejects verbs other than `get`/`list`/`watch`.
3. The bundled `ClusterRole` (`manifests/rbac/`) only grants those verbs.

Mutating tools are not registered, so the LLM never sees them.

## Models

Bring your own key. Supported providers: OpenAI (gpt-*), Anthropic (claude-*),
Google (gemini-*), Moonshot (kimi-*), and any OpenAI-compatible endpoint
(vLLM, Ollama, OpenRouter). Switch models inside a session with `/model`.

## License

MIT.
