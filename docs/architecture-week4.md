# Architecture notes — week 4

Append to `docs/architecture.md`.

## What was added

| Component | Package/Location | Purpose |
|---|---|---|
| Prometheus metrics | `internal/metrics` | counters, histograms, gauges for every request |
| Metrics collector | `internal/metrics/collector.go` | syncs health/breaker state to gauges every second |
| `/metrics` endpoint | `cmd/gobalance/main.go` | Prometheus scrapes this |
| Grafana dashboard | `deploy/grafana/dashboards/gobalance.json` | 11-panel dashboard, auto-provisioned |
| Config hot reload | `internal/reload` | watches `gobalance.yaml` via fsnotify |
| Atomic strategy swap | `internal/proxy` | `SwapStrategy()` via `atomic.Value` |

## Why a custom Prometheus registry

The global `prometheus.DefaultRegisterer` includes every metric from every library in the binary. A custom registry keeps `/metrics` clean: only GoBalance metrics plus Go runtime stats. Tests can also create isolated registries without polluting each other.

## Why histogram buckets are custom

Default Prometheus buckets (.005, .01, .025, ..., 10) are designed for second-scale operations. At 10–100ms latencies most buckets are empty, and the heatmap shows nothing. Custom buckets from 1ms to 10s in log-spaced steps put data where it actually lives.

## Why health and breaker gauges use a periodic collector

Request-path metrics (counters, histograms) are updated inline — every request passes through the proxy anyway. But health and breaker **state** is changed by background goroutines (the health checker) or by internal transitions inside the breaker. Updating gauges on every request would miss transitions that happen between requests, and polluting the health checker with Prometheus imports violates its single responsibility.

Polling once per second is cheap (one pass over the backend list), keeps the request path free of gauge updates, and gives Grafana a smooth timeline.

## Why `atomic.Value` for the strategy

Hot reload swaps the strategy while requests are in flight. The old strategy finishes the requests it already started; the next request picks up the new one. `sync/atomic.Value` gives a lock-free read on the hot path (one interface load per request) and a safe write from the reload goroutine.

The alternative — a `sync.RWMutex` — would add a lock acquisition to every single request. At 10k RPS that contention is measurable. `atomic.Value` is zero-cost on the read path.

## Hot reload: what can and cannot be reloaded

| Reloadable | Why |
|---|---|
| Backend list (add, remove, change URL/weight) | pool.Set() is an atomic swap |
| Strategy | atomic.Value swap |

| NOT reloadable | Why |
|---|---|
| Listen addresses | the TCP listener is already bound; changing it requires closing and reopening the socket, which is a restart |
| TLS certificates | same reason — the listener holds the cert |
| Timeout values | applied at server construction; changing them requires rebuilding the http.Server |

Those limitations are worth stating because "what can't you hot-reload and why?" is a real interview question. The answer is always "anything that requires re-binding a socket or rebuilding an already-running server".

## Debouncing

Editors write multiple filesystem events per save (write, chmod, rename on some platforms). Reloading on each one wastes work and can parse a half-written file. The watcher waits 500ms of quiet before applying, so a save that fires three events results in one reload.

## New dependencies

| Dependency | Version | Why |
|---|---|---|
| `github.com/prometheus/client_golang` | latest | Prometheus client library |
| `github.com/fsnotify/fsnotify` | latest | cross-platform filesystem notifications |

Both are standard, widely used, and have no transitive dependencies worth worrying about.

## The dashboard

11 panels covering the metrics that tell the load balancing story:

- **Request rate by backend** — the distribution chart. This is the visual equivalent of the loadgen's distribution table.
- **Latency percentiles (p50/p95/p99)** — per backend, so you can see gamma's tail separately.
- **Backend health** — 1/0 timeline. A drop from 1 to 0 is visible instantly.
- **Breaker state** — 0/0.5/1 timeline. Closed → open → half-open → closed is the story of a failure and recovery.
- **In-flight requests** — what least-connections and P2C are actually reading.
- **Retry budget** — the token balance dipping during a fault and refilling after.
- **Failures by type** — transport vs 5xx, so you can see whether the backend is down or sick.
- **Retries** — how many retries succeeded vs failed.
- **Traffic share (pie)** — gamma's 7% vs 33% in one glance.
- **Throughput (stat)** — total req/s as a big number.
- **Error rate (stat)** — with green/yellow/red thresholds.

The dashboard is auto-provisioned: `docker compose up` and it appears in Grafana with no manual import.
