# cloudy RBAC

Read-only ClusterRole + ServiceAccount for the cloudy agent.

```sh
kubectl apply -k manifests/rbac/base
```

## Verb whitelist

The `cloudy-readonly` ClusterRole grants read-only access across cluster resources:

- `get`
- `list`
- `watch`

Special resource permissions:
- `pods/log`: `get` — stream container logs.
- `services/proxy`: `get` — required for ServiceProxy.URL routing in apiserver-proxy.
- `pods/portforward`: `create` — required for SPDY in-process port-forward to backend databases.

`pods/exec` is intentionally **not** included; in-cluster JVM / Python
diagnostic tools (jcmd, py-spy, async-profiler) require a separately
deployed sidecar.

## Verifying

```sh
kubectl auth can-i create pods --as=system:serviceaccount:cloudy:cloudy
# → no
kubectl auth can-i list pods --all-namespaces --as=system:serviceaccount:cloudy:cloudy
# → yes
```
