# cloudy RBAC

Read-only ClusterRole + ServiceAccount for the cloudy agent.

```sh
kubectl apply -k manifests/rbac/base
```

## Verb whitelist

The `cloudy-readonly` ClusterRole grants exactly three verbs:

- `get`
- `list`
- `watch`

`pods/log` additionally allows `get` so cloudy can stream container logs.
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
