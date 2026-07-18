# GoBalance

A Layer-7 HTTP load balancer written in Go, with pluggable routing strategies,
health checking, circuit breaking, live Kubernetes service discovery, and a
benchmark suite that measures which strategy wins under which workload.

> **Status: Week 1 of 8.** The core proxy, backend pool, strategy plugin system,
> round-robin and random strategies, admin API, graceful shutdown and the test
> suite are done. See [the roadmap](#roadmap).

---

## Why this project exists

Almost every load balancer in production — nginx, kube-proxy, most cloud LBs —
defaults to round-robin. Round-robin is optimal only when every backend is
identical and every request costs the same. Real systems violate both
assumptions constantly: nodes differ in size, requests differ in cost, and
service times are heavy-tailed.

GoBalance implements five strategies behind one interface and measures the
difference, so the claim "power-of-two-choices reduces p99 latency by X% under
heterogeneous load" comes with numbers attached.

---

## Quick start

Requires Go 1.22+.

```bash
make tidy       # download the one dependency (yaml)
make race       # run the full test suite with the race detector
make backends   # start 3 mock upstreams on :9001 :9002 :9003
make run        # start the balancer on :8080 (admin on :8081)

# in another terminal:
make smoke      # 12 requests, showing which backend served each
```

You should see a perfect rotation:

```
  request  1 -> alpha
  request  2 -> beta
  request  3 -> gamma
  request  4 -> alpha
  ...
```

Now open `gobalance.yaml`, change `strategy: round_robin` to `strategy: random`,
restart, and run `make smoke` again. The distribution changes; nothing else in
the codebase does. That is the plugin architecture doing its job.

---

## Architecture

```
                         ┌──────────────────────────────┐
   client ──── :8080 ───►│  proxy.Balancer (ServeHTTP)  │
                         └───────────────┬──────────────┘
                                         │
                     1. pool.Healthy()   │   snapshot of eligible backends
                                         ▼
                         ┌──────────────────────────────┐
                         │  strategy.Strategy.Pick()    │  round_robin | random
                         └───────────────┬──────────────┘  (week 2: wrr, leastconn, p2c, ewma)
                                         │
                     2. IncInflight()    │
                                         ▼
                         ┌──────────────────────────────┐
                         │  httputil.ReverseProxy       │──► upstream backend
                         └───────────────┬──────────────┘
                                         │
                     3. OnFinish(latency)│   only if the strategy is an Observer
                                         ▼
                                    metrics / EWMA state
```

### Packages

| Package | Responsibility |
|---|---|
| `internal/config` | parse + validate + default `gobalance.yaml` |
| `internal/backend` | one upstream: URL, weight, atomic counters, its `ReverseProxy` |
| `internal/pool` | concurrency-safe set of backends; answers "who is healthy?" |
| `internal/strategy` | the `Strategy` interface, the plugin registry, the algorithms |
| `internal/proxy` | the request path: pick, count, forward, observe |
| `cmd/gobalance` | wiring, two HTTP servers, graceful shutdown |
| `bench/mockbackend` | configurable-latency upstream for testing and benchmarking |

---

## Admin API (`:8081`)

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | liveness — is the process up |
| `GET /readyz` | readiness — are there healthy backends |
| `GET /backends` | JSON view of the pool: health, in-flight, totals, failures |

```bash
curl -s localhost:8081/backends
```

Every proxied response also carries `X-Gobalance-Backend` and
`X-Gobalance-Strategy`, which is how `scripts/smoke.sh` and the week-6 benchmark
verify the distribution.

---

## Roadmap

- [x] **Week 1** — proxy core, pool, strategy interface + registry, round-robin, random, config, admin API, graceful shutdown, tests
- [ ] **Week 2** — smooth weighted round-robin, least-connections, power-of-two-choices, EWMA-latency
- [ ] **Week 3** — active + passive health checks, circuit breaker, retry budgets, connection draining
- [ ] **Week 4** — Prometheus metrics, Grafana dashboard, config hot reload
- [ ] **Week 5** — Kubernetes EndpointSlice service discovery
- [ ] **Week 6** — benchmark harness (k6, constant arrival rate, heavy-tailed workloads)
- [ ] **Week 7** — results analysis and write-up
- [ ] **Week 8** — buffer / one stretch feature

## Out of scope

L4/TCP balancing, TLS termination, and multi-balancer coordination. Stated
explicitly so the scope stays defensible.
