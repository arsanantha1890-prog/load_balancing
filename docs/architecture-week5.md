# Architecture notes — week 5

Append to `docs/architecture.md`.

## What was added

| Component | Package | Purpose |
|---|---|---|
| Provider interface | `internal/discovery` | abstracts static vs kubernetes |
| EndpointSlice watcher | `internal/discovery` | informer-based controller |
| Discovery config | `internal/config` | `discovery.mode` selector |
| k8s manifests | `deploy/k8s/` | RBAC, Deployment, Service, ConfigMap |
| kind setup | `deploy/k8s/` | local cluster for testing |

## Why informers, not raw watches

The Kubernetes API supports a raw Watch verb, but using it means handling
connection drops, bookmark events, re-list on 410 Gone, and resource-version
tracking. client-go's SharedInformerFactory wraps all of that into a cache that
fires Add/Update/Delete callbacks. Every production controller uses informers.

## Why reconciliation, not event processing

The callbacks do not apply diffs. They call reconcile(), which reads the full
state from the informer cache and rebuilds the backend list from scratch.

A diff-based approach is faster but must handle every combination of
add/update/delete across multiple EndpointSlice objects. Getting it wrong means
a stale pool. Reconciliation is O(n) where n is the number of endpoints, and n
is small — a few hundred at most.

## Why EndpointSlice, not Endpoints

The Endpoints resource packs every address into one object. At scale, every pod
change rewrites the entire thing — O(n) updates for O(1) events. EndpointSlice
shards addresses across smaller objects. Endpoints is deprecated since
Kubernetes 1.33.

## Why the Provider interface

The balancer, health checker, metrics collector and config reloader all see the
same `pool.Pool`. They don't need to know where the backends came from. The
Provider interface makes the source pluggable:

```go
type Provider interface {
    Run(ctx context.Context) error
}
```

Static mode's Run just blocks until cancelled. Kubernetes mode's Run starts an
informer and reconciles on every event. main.go picks one based on config and
starts it in a goroutine.

## RBAC: minimum permissions

The ServiceAccount gets a Role with exactly two verbs on one resource:

```yaml
rules:
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["list", "watch"]
```

No write access, no cluster-wide scope, no access to secrets or pods. This is
the minimum required to watch endpoints in one namespace.

## Config watcher disabled in kubernetes mode

In static mode, fsnotify watches `gobalance.yaml` for backend changes. In
kubernetes mode, the informer is the source of truth — the config watcher would
fight it. So `main.go` only starts the watcher in static mode.

## Testing without a cluster

All tests use `k8s.io/client-go/kubernetes/fake`, which is a simulated API
server that runs in-process. The tests create, update and delete EndpointSlice
objects and verify the pool changes accordingly. No kind cluster, no Docker, no
network. This is how production Kubernetes controllers are tested.

## Known limitations

| Limitation | Consequence |
|---|---|
| Backends from k8s all get weight=1 | no weighted routing based on pod resources |
| No leader election | multiple replicas would each watch independently (fine for stateless LB) |
| Reconnect on API server restart depends on informer retry | client-go handles this, but a very long partition could cause a stale cache |
