# GoBalance
[![CI](https://github.com/arsanantha1890-prog/load_balancing/actions/workflows/ci.yml/badge.svg)](https://github.com/arsanantha1890-prog/load_balancing/actions)

A Layer-7 HTTP load balancer written in Go, with pluggable routing strategies,
resilience features, live Kubernetes service discovery, and a benchmark suite
that measures which strategy wins under which workload.

> **EWMA-latency routing reduced p90 from 95ms to 15ms and doubled throughput
> vs round-robin under heterogeneous backend latencies.**

---

## Quick start

Requires Go 1.22+.

```bash
go mod tidy
go test -race ./...

# Start 3 mock backends (two fast, one slow)
start bin/mockbackend -name alpha -addr :9001 -delay 10ms -jitter 5ms
start bin/mockbackend -name beta  -addr :9002 -delay 10ms -jitter 5ms
start bin/mockbackend -name gamma -addr :9003 -delay 80ms -jitter 20ms

# Start the balancer
go run ./cmd/gobalance -config gobalance.yaml

# Send traffic
go run ./bench/loadgen -n 300 -c 30
```

---

## Strategies

| Strategy | Signal | Cost | gamma share (measured) |
|---|---|---|---|
| Round-robin | none | O(1) | 33.3% |
| Smooth weighted RR | static config | O(n) | 33.3% (equal weights) |
| Least-connections | in-flight count | O(n) | 7.0% |
| P2C | in-flight count | O(1) | 8.3% |
| EWMA-latency | latency × inflight | O(1) | 1.3% |
| Consistent hash  | request key + load | O(1) | key-affine           |

Switch with one flag: `go run ./cmd/gobalance -strategy ewma`

---

## Resilience

- **Active health checks** — background prober, configurable hysteresis
- **Passive health checks** — ejection from real traffic failures (catches grey failures)
- **Circuit breaker** — closed/open/half-open per backend
- **Retry budget** — gRPC-style token bucket prevents retry storms

---

## Observability

- **Prometheus metrics** — request rate, latency histogram, health, breaker state, retry budget
- **Grafana dashboard** — 11 panels, auto-provisioned via Docker Compose

```bash
cd deploy && docker compose up -d
# Grafana: http://localhost:3000 (admin/admin)
```

- **Config hot-reload** — edit `gobalance.yaml`, save. Strategy and backends update with zero downtime.

---

## Kubernetes integration

```yaml
discovery:
  mode: kubernetes
  service_name: my-service
  namespace: default
```

EndpointSlice informers track pod changes in real time. Scaling a Deployment
from 3 to 5 replicas updates the backend pool in under 1 second.

---

## Benchmark

```bash
# Closed-loop (fixed concurrency)
go run ./bench/loadgen -n 1000 -c 30 -mode closed

# Open-loop (constant arrival rate — avoids coordinated omission)
go run ./bench/loadgen -rps 500 -duration 10s -mode open

# Full benchmark matrix (4 strategies × 4 scenarios × 2 modes)
powershell -ExecutionPolicy Bypass -File bench/benchmark.ps1
```

### Results (heterogeneous backends, closed-loop)

| Strategy | gamma % | p90 | Throughput |
|---|---|---|---|
| Round-robin | 33.3% | 95ms | 670 req/s |
| Least-connections | 8.0% | 24ms | 1281 req/s |
| P2C | 9.0% | 26ms | 1275 req/s |
| EWMA | 7.0% | 19ms | 1397 req/s |

---

## Architecture

```
client → :8080 → proxy.Balancer
                   ├── pool.Healthy()
                   ├── strategy.Pick()      ← pluggable
                   ├── breaker.Acquire()
                   ├── retry loop
                   └── backend.Proxy.ServeHTTP()

:8081 → admin API (/healthz, /readyz, /backends, /metrics)

background:
  ├── health.Checker (active probing)
  ├── metrics.Collector (gauge sync)
  ├── reload.Watch (config hot-reload)
  └── discovery.Provider (static or kubernetes)
```

---

## Project report

See [docs/PROJECT_REPORT.md](docs/PROJECT_REPORT.md) for the full write-up
including methodology, results analysis, and conclusions.

---

## Development timeline

- [x] Week 1 — proxy core, pool, strategy interface, admin API, graceful shutdown
- [x] Week 2 — smooth WRR, least-connections, P2C, EWMA, benchmark comparison
- [x] Week 3 — active/passive health checks, circuit breaker, retries with budget
- [x] Week 4 — Prometheus metrics, Grafana dashboard, config hot-reload
- [x] Week 5 — Kubernetes EndpointSlice service discovery
- [x] Week 6 — open-loop loadgen, concurrency-limited backends, full benchmark
- [x] Week 7 — results analysis and project report
- [x] Week 8 — consistent hashing with bounded loads
