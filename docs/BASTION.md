# T2 — Bastion / Jump Host Deployment

cloudy ships as a single static binary, so a bastion-style deployment is just
"copy the binary, give every shell user their own state directory, and make
sure egress traffic reaches both the Kubernetes API and the LLM endpoint."

This guide assumes:

- The bastion is the only host that can reach your private Kubernetes
  control planes and Prometheus servers.
- Outbound TLS to public LLM APIs may be blocked, restricted, or required
  to traverse a corporate proxy.
- Several shell users share the bastion and must not see each other's
  cloudy configuration, profile, or session logs.

## 1. Install the binary

```sh
# As root, once.
install -m 0755 cloudy /usr/local/bin/cloudy
cloudy --version
```

If you build from source on the bastion itself:

```sh
git clone https://github.com/rlaope/cloudy-agent.git /opt/cloudy
cd /opt/cloudy
make build
sudo install -m 0755 cloudy /usr/local/bin/cloudy
```

## 2. Per-user state directory

cloudy resolves its config / profile / session log paths in this order:

1. `$CLOUDY_HOME` (recommended on a bastion — pin it per-user)
2. `$XDG_CONFIG_HOME/cloudy`
3. `~/.cloudy`

Add a single line to `/etc/profile.d/cloudy.sh`:

```sh
export CLOUDY_HOME="$HOME/.cloudy"
```

Or, if you maintain a centrally-mounted home (NFS), keep cloudy state on
local disk:

```sh
export CLOUDY_HOME="/var/cloudy/$USER"
mkdir -p "$CLOUDY_HOME"
```

cloudy writes only inside `$CLOUDY_HOME` — no globally shared files are
touched.

## 3. Read-only kubeconfig

The bastion's kubeconfig must already be scoped to read-only access. The
recommended pattern is one ServiceAccount per cluster, bound to the
`cloudy-readonly` ClusterRole shipped under `manifests/rbac/base/`:

```sh
kubectl --context prod-eu apply -k manifests/rbac/base
TOKEN=$(kubectl --context prod-eu create token cloudy -n cloudy --duration 24h)
# Splice TOKEN into the bastion's kubeconfig "user" entry.
```

Verify before going live:

```sh
kubectl auth can-i create pods --as=system:serviceaccount:cloudy:cloudy
# → no
kubectl auth can-i list pods --all-namespaces --as=system:serviceaccount:cloudy:cloudy
# → yes
```

cloudy enforces the same `get`/`list`/`watch` whitelist client-side
regardless of the kubeconfig — the RBAC is the second of three independent
read-only guards.

## 4. LLM egress

A bastion typically cannot reach `api.anthropic.com` / `api.openai.com`
directly. Pick one of these patterns:

### A. Corporate HTTPS proxy

cloudy's LLM adapters use Go's default HTTP transport, which honours
`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` automatically. Set them once
in `/etc/profile.d/cloudy.sh`:

```sh
export HTTPS_PROXY="http://proxy.internal:3128"
export NO_PROXY="*.internal,10.0.0.0/8,127.0.0.1,localhost"
```

`cloudy doctor` echoes these so users can confirm what cloudy will use.

### B. In-network LLM service

Point cloudy at an internal OpenAI-compatible service (vLLM, Ollama, or a
gateway like LiteLLM):

```yaml
# ~/.cloudy/config.yaml  (or  $CLOUDY_HOME/config.yaml)
default_model: local/qwen2.5-coder-32b-instruct
providers:
  local:
    base_url: https://llm.internal/v1
    api_key_env: CLOUDY_LOCAL_API_KEY
```

Models prefixed with `local/` route to the `openai_compat` adapter; the
`local/` prefix is stripped before the request reaches the upstream.

### C. Pre-approved API key with proxy

Combine A and B — set `HTTPS_PROXY` so direct provider traffic still works
when you want it, and configure `local/` for the in-cluster fast path.

## 5. Verifying

`cloudy doctor` is the single source of truth. On a properly-configured
bastion shell user it should report:

```
✔  kubeconfig parseable              /home/alice/.kube/config
✔  profile valid                     2 contexts, generated 2026-05-13 10:21
✔  active context reachable          prod-eu
✔  default model has API key in env  ANTHROPIC_API_KEY
✔  cloudy home                       /var/cloudy/alice
✔  egress proxy                      HTTPS_PROXY=http://proxy.internal:3128
```

Use `cloudy doctor --json` from your bastion image's CI to fail fast if any
critical check fails for a freshly-provisioned user.

## 6. Multi-user safety

- `manifests/rbac/base/clusterrole.yaml` excludes Secrets and Pod Exec.
- cloudy never writes outside `$CLOUDY_HOME`.
- Session JSONL logs live under `$CLOUDY_HOME/logs/` and are 0600.
- The Tool Registry refuses to register any tool that reports
  `ReadOnly()==false`, so a bastion install cannot be silently augmented
  with a mutating tool by a misbehaving plugin.

## 7. systemd hint (optional)

If you want a long-running cloudy session (e.g. for an on-call dashboard),
the file `manifests/bastion/cloudy@.service` is a per-user systemd template
unit. Enable it as `cloudy@alice` etc. It runs in non-TUI mode tailing a
specific session log.
