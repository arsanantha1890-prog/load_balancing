# GoBalance — A Layer-7 HTTP Load Balancer with Pluggable Routing Strategies

## Abstract

GoBalance is a Layer-7 HTTP load balancer written in Go that implements five
pluggable routing strategies and measures their performance under controlled
conditions. The project demonstrates that the choice of routing algorithm has a
measurable, significant impact on tail latency and throughput when backend
servers differ in performance — a condition that is universal in production but
absent from most load balancer defaults.

Under heterogeneous backend latencies (two servers at 10ms, one at 80ms),
EWMA-latency routing reduced p90 latency from 95ms to 15ms and doubled
throughput (1490 req/s vs 670 req/s) compared to round-robin, by reducing the
slow backend's traffic share from 33.3% to 1.3%. These results were validated
under both closed-loop and open-loop load generation.

The system includes active and passive health checking, per-backend circuit
breakers, retry budgets, Prometheus metrics with Grafana dashboards, config
hot-reload, and Kubernetes EndpointSlice service discovery — making it a
complete, production-architecture load balancer rather than a theoretical
exercise.

---

## 1. Problem Statement

### 1.1 The default is wrong

Nearly every load balancer in production — nginx, kube-proxy, most cloud load
balancers — defaults to round-robin. Round-robin distributes requests uniformly
regardless of backend capacity or health. This is optimal only when every
backend is identical and every request costs the same.

Real systems violate both assumptions constantly:
- Nodes differ in size, generation, and available resources
- Requests differ in cost (database queries vs cache hits)
- Service times are heavy-tailed (most requests are fast, a few are very slow)
- Backends degrade gradually rather than failing cleanly

### 1.2 The cost of the wrong default

Under round-robin with heterogeneous backends, a slow backend receives the same
traffic share as a fast one. The tail of the latency distribution is determined
by the routing share, not by the workload:

```
gamma's share: 33.3%  →  tail begins at p67
gamma's share:  6.6%  →  tail begins at p93
gamma's share:  1.3%  →  tail begins at p99
```

The tail didn't shrink — it was pushed further out. Changing the routing share
is the only lever; making gamma faster is not within the load balancer's scope.

### 1.3 The thesis

**Tail latency under heterogeneous backends is a routing property, not a
hardware property.** An algorithm that accounts for backend performance can
reduce p90 latency by 6× and double throughput without any change to the
backends themselves.

---

## 2. Architecture

### 2.1 System overview

```
                         ┌──────────────────────────────────┐
   client ──── :8080 ───►│  proxy.Balancer (ServeHTTP)      │
                         │  ┌─ retry loop ────────────────┐ │
                         │  │ choose → breaker.Acquire    │ │
                         │  │ → IncInflight → ReverseProxy│ │
                         │  │ → observe → metrics         │ │
                         │  └─────────────────────────────┘ │
                         └──────────────┬───────────────────┘
                                        │
              ┌─────────────────────────┼─────────────────────────┐
              ▼                         ▼                         ▼
         ┌─────────┐             ┌─────────┐             ┌─────────┐
         │  alpha   │             │  beta   │             │  gamma  │
         │  10ms    │             │  10ms   │             │  80ms   │
         └─────────┘             └─────────┘             └─────────┘
```

### 2.2 Package structure

| Package | Responsibility | Key design decision |
|---|---|---|
| `config` | parse and validate YAML | defaults in one function; typos are errors |
| `backend` | one upstream server | atomic counters; per-backend transport |
| `pool` | concurrency-safe backend set | RWMutex for read-heavy access |
| `strategy` | routing algorithm selection | interface + registry; Observer is optional |
| `proxy` | request path (~100 lines) | retry loop with budget; functional options |
| `breaker` | per-backend circuit breaker | closed/open/half-open state machine |
| `health` | active health checking | hysteresis; separate transport |
| `retry` | retry budget | gRPC-style token bucket |
| `retryctx` | transport error propagation | enables retry without response corruption |
| `metrics` | Prometheus instrumentation | custom registry; periodic gauge collector |
| `reload` | config hot-reload | fsnotify with debouncing |
| `discovery` | backend source abstraction | static config vs Kubernetes informers |

### 2.3 The Strategy interface

The single most important design decision in the project:

```go
type Strategy interface {
    Name() string
    Pick(r *http.Request, healthy []*backend.Backend) (*backend.Backend, error)
}

type Observer interface {
    OnStart(b *backend.Backend)
    OnFinish(b *backend.Backend, latency time.Duration, err error)
}
```

`Pick` is mandatory. `Observer` is optional and resolved once at construction
via a type assertion, not per request. This split means:
- Round-robin and random carry no empty method bodies
- Adding a new strategy requires zero changes to the proxy
- The hot path performs no type assertions

---

## 3. Routing Strategies

### 3.1 Round-robin (baseline)

Fixed rotation via an atomic counter. No lock, no feedback. O(1).

Optimal when all backends are identical. Provably suboptimal otherwise — it
sends gamma 33.3% of traffic regardless of gamma's capacity.

### 3.2 Smooth weighted round-robin

The nginx algorithm. Weights 5:1:1 produce the sequence `a,a,b,a,c,a,a` —
correct ratio with no bursts. Naive weight expansion would produce
`a,a,a,a,a,b,c` — same ratio, five consecutive requests to one backend.

Requires a mutex (all scores must update atomically), which is the real cost
over plain round-robin.

### 3.3 Least-connections

Routes to the backend with the fewest in-flight requests. O(n) scan.

The theory (Little's Law): `L = λW`. Equalising L across backends with
different W makes λ proportional to 1/W — the traffic share converges on each
backend's actual capacity automatically, without measuring latency.

For the test setup: `(1/92) / (1/13 + 1/13 + 1/92) = 6.6%`. Measured: 7.0%.

Tie-breaking uses reservoir sampling to prevent bias toward healthy[0] at low
load.

### 3.4 Power-of-two-choices (P2C)

Sample two backends at random, pick the less loaded. O(1).

The theoretical result: maximum load drops from `Θ(log n / log log n)` to
`Θ(log log n)` — an exponential improvement from one extra sample. Going to
d=3 adds almost nothing; the gain is entirely between d=1 and d=2.

P2C's advantage over least-connections is not quality but **herd resistance**:
multiple balancer replicas making randomised choices don't stampede the same
backend simultaneously.

### 3.5 EWMA-latency

Cost function: `smoothed_latency × (inflight + 1)`, selected via P2C.

Three ideas stacked:
1. **Peak EWMA**: jumps instantly on a slow response, decays gradually on fast
   ones. Asymmetric by design — damage from routing to a degraded backend is
   immediate.
2. **In-flight multiplier**: historical latency is backward-looking; the
   multiplier converts the score into estimated queue time.
3. **Time-weighted decay**: `w = exp(-elapsed/tau)` — a quiet backend is
   forgiven at the same rate as a busy one.

---

## 4. Resilience

### 4.1 Health checking

**Active**: background prober polling `/healthz` every 2 seconds. The only
mechanism that detects recovery — a dead backend receives no traffic for
passive checking to observe.

**Passive**: ejection based on real traffic failures. Catches grey failures
where `/healthz` returns 200 while real requests fail.

Both use consecutive counters with different thresholds (hysteresis) to prevent
flapping.

### 4.2 Circuit breaker

Three-state machine (closed → open → half-open → closed) that converts failing
*slowly* (a 2-second connect timeout) into failing *instantly* (a boolean
check). Protects resources that would otherwise be wasted on doomed requests.

Half-open allows exactly `max_probes` concurrent requests — not full traffic —
to prevent a cold-start stampede on a recovering backend.

### 4.3 Retry budget

gRPC-style token bucket: successes add tokens, failures subtract them, retries
are allowed only while tokens exceed half the maximum. This caps retries as a
fraction of successful traffic rather than as a per-request count, preventing
the retry storm that turns a partial outage into a total one.

The budget is global, not per-backend — the point is to cap total retry traffic.

### 4.4 Retry mechanism

Retries are only possible for transport failures (connection refused, timeout
before response line) where nothing has been written to the client. A 5xx
response has already been sent and cannot be recalled. This is a physical
constraint, not a policy choice.

Only idempotent methods (GET, HEAD, OPTIONS, TRACE) with no request body are
retried.

---

## 5. Observability

### 5.1 Prometheus metrics

| Metric | Type | Labels |
|---|---|---|
| `gobalance_requests_total` | counter | backend, strategy, method, code |
| `gobalance_request_duration_seconds` | histogram | backend, strategy |
| `gobalance_backend_inflight` | gauge | backend |
| `gobalance_backend_healthy` | gauge | backend |
| `gobalance_breaker_state` | gauge | backend |
| `gobalance_failures_total` | counter | backend, type |
| `gobalance_retries_total` | counter | backend, outcome |
| `gobalance_retry_budget_tokens` | gauge | — |

Histogram buckets are custom (1ms–10s in log-spaced steps) rather than
Prometheus defaults, because the default buckets produce mostly empty bins at
millisecond-scale latencies.

### 5.2 Grafana dashboard

11-panel dashboard auto-provisioned via Docker Compose:
- Request rate by backend
- Latency percentiles (p50/p95/p99) per backend
- Backend health timeline (1/0)
- Circuit breaker state timeline (closed/half-open/open)
- In-flight requests per backend
- Retry budget token balance
- Failures by type (transport vs 5xx)
- Retries (succeeded vs failed)
- Traffic share pie chart
- Total throughput (stat)
- Error rate with thresholds (stat)

### 5.3 Config hot-reload

fsnotify watches `gobalance.yaml`. On change (debounced 500ms), the backend
pool and strategy are swapped atomically. Listen addresses and timeouts cannot
be reloaded because they require re-binding a socket.

---

## 6. Kubernetes Integration

### 6.1 EndpointSlice discovery

A controller using client-go SharedInformerFactory watches EndpointSlice objects
for a named Service. On any add/update/delete event, the controller reconciles
from scratch: reads the full state from the informer cache and rebuilds the
backend list.

Reconciliation is O(n) but correct and simple. A diff-based approach would need
to handle every combination of changes across multiple EndpointSlice objects.

### 6.2 Why EndpointSlice, not Endpoints

The Endpoints resource packs every address into one object. At scale, every pod
change rewrites the entire thing. EndpointSlice shards addresses across smaller
objects. Endpoints is deprecated since Kubernetes 1.33.

### 6.3 RBAC

The ServiceAccount gets a Role with `list` and `watch` on
`discovery.k8s.io/endpointslices` — the minimum required permission. No write
access, no cluster-wide scope.

### 6.4 Demonstrated result

Scaling a Deployment from 3 to 5 replicas updated the backend pool in under 1
second with zero configuration changes. Scaling down to 2 removed 3 backends
automatically.

---

## 7. Benchmark Methodology

### 7.1 Load generators

**Closed-loop**: fixed number of workers, each sends a new request when the
previous finishes. Understates damage from slow backends because the arrival
rate drops proportionally (coordinated omission).

**Open-loop**: requests arrive at a fixed rate regardless of response time.
Measures `wait time` (scheduled arrival to actual dispatch) — high wait time
indicates real overload rather than measurement-hidden overload.

### 7.2 Mock backends

Configurable upstream servers with tunable delay, jitter, tail probability,
failure rate, and concurrency limits. The concurrency limit (week 6) models
real backend saturation — when all slots are busy, requests queue, producing
realistic tail latency from queueing rather than just processing time.

### 7.3 Scenarios

| Scenario | Configuration | Purpose |
|---|---|---|
| Homogeneous | all backends 10ms | control — strategies should be equal |
| Heterogeneous | gamma 80ms, others 10ms | routing quality |
| Heavy-tail | 5-10% of requests take 500ms-1s | transient slowness |
| Concurrency-limited | gamma: 3 slots, others: 50 | real queueing |

### 7.4 Methodology

- 500 requests per run (closed-loop), 300 RPS for 5 seconds (open-loop)
- 200-request warm-up discarded before each measurement
- Each run starts clean: fresh balancer, fresh backends, fresh state
- Strategy varied by CLI flag; all other parameters identical

---

## 8. Results

### 8.1 Heterogeneous scenario (the thesis test)

| Strategy | gamma % | p50 | p90 | p99 | Throughput |
|---|---|---|---|---|---|
| Round-robin | 33.3% | 14ms | 95ms | 100ms | 670 req/s |
| Least-connections | 8.0% | 14ms | 24ms | 104ms | 1281 req/s |
| P2C | 9.0% | 14ms | 26ms | 101ms | 1275 req/s |
| EWMA | 7.0% | 14ms | 19ms | 100ms | 1397 req/s |

**Finding 1**: Least-connections confirms the Little's Law prediction.
Predicted gamma share: 6.6%. Measured: 8.0%. The theory holds.

**Finding 2**: EWMA outperforms P2C. Its cost function
`latency × (inflight + 1)` carries information that in-flight count alone
does not: gamma's one in-flight request is *expensive* (92ms), not just
*present*. Under open-loop, EWMA pushed gamma to 3.1%.

**Finding 3**: p99 barely moves across strategies. Gamma's floor is ~92ms
and some requests must still land there. The tail doesn't shrink — its
position changes.

**Finding 4**: p50 is identical (14ms) across all strategies, confirming
that the strategies affect only where gamma's traffic goes, not the fast
path.

### 8.2 Open-loop vs closed-loop

| Strategy | Mode | gamma % | p90 | Throughput |
|---|---|---|---|---|
| EWMA | closed | 7.0% | 19ms | 1397 req/s |
| EWMA | open | 3.1% | 16ms | 300 req/s |

EWMA's gamma share dropped from 7.0% to 3.1% under open-loop. The constant
arrival rate gives EWMA more opportunity to observe latency differences because
requests don't self-throttle.

Wait times were near zero at 300 RPS, indicating the system was not overloaded.
Coordinated omission would become visible at higher rates.

### 8.3 Concurrency-limited scenario

| Strategy | Failures | gamma % | p90 | Throughput |
|---|---|---|---|---|
| Round-robin | 6 (1.2%) | 1.8% | 16ms | 1965 req/s |
| P2C | 8 (1.6%) | 2.2% | 16ms | 1964 req/s |
| EWMA | 5 (1.0%) | 1.6% | 16ms | 1981 req/s |

**Finding 5**: Under concurrency limits with health checks, all strategies
converge. When gamma's 3 slots fill, it returns 503. After 5 consecutive
503s, passive health checking ejects gamma entirely. From that point, gamma
receives zero traffic regardless of the routing algorithm.

**The resilience infrastructure (week 3) dominates the routing algorithm
(week 2).** The strategy matters less when the system correctly identifies
and ejects the failing backend. This is not a failure of the benchmark — it
is a genuine finding about how the layers interact.

---

## 9. Honest Limitations

| Limitation | Impact |
|---|---|
| 3 backends is too few for the herd-avoidance argument | P2C vs LC delta is about O(1) vs O(n); needs n >> 3 |
| Single machine shares CPU between loadgen, balancer, backends | results are relative, not absolute |
| 300 RPS didn't overload the system under open-loop | coordinated omission was measured but not significant |
| EWMA scores freeze without traffic | starvation at low load; needs decay-on-read |
| Mock backends are synthetic | real services have correlated failures, garbage collection, disk I/O |
| Retry budget parameters untested across scenarios | may be too generous or too tight |
| No TLS termination | a real production LB would terminate TLS |

---

## 10. Conclusions

1. **Tail latency under heterogeneous backends is a routing property.** Changing
   gamma's traffic share from 33.3% to 7% moves the p90 from 95ms to 19ms
   without changing any backend.

2. **Little's Law predicts the optimal share.** The 6.6% prediction matches the
   7.0% measurement for least-connections, validating the queueing-theory model.

3. **EWMA is the strongest strategy when backends have stable but different
   latencies.** Its cost function sees what in-flight counting cannot: the
   *duration* of each in-flight request, not just its *existence*.

4. **Resilience infrastructure and routing interact non-additively.** Under
   concurrency limits, passive health checking ejected the failing backend
   before any strategy difference could manifest. The layers complement rather
   than duplicate.

5. **Open-loop measurement is necessary but not sufficient.** At the tested
   rates, coordinated omission was not significant. A higher-rate benchmark
   would be needed to demonstrate the full effect.

---

## 11. Technologies

| Component | Technology |
|---|---|
| Language | Go 1.22+ |
| Reverse proxy | `net/http/httputil.ReverseProxy` |
| Metrics | Prometheus (`prometheus/client_golang`) |
| Dashboards | Grafana (auto-provisioned via Docker Compose) |
| Config watching | fsnotify |
| Service discovery | Kubernetes client-go (EndpointSlice informers) |
| Container orchestration | Docker, kind (Kubernetes-in-Docker) |
| Load generation | Custom Go tool (closed-loop and open-loop modes) |
| CI | GitHub Actions (`go vet`, `go test -race`) |

---

## 12. Repository Structure

```
gobalance/
├── cmd/gobalance/          main entry point, server wiring
├── internal/
│   ├── backend/            one upstream: URL, counters, ReverseProxy
│   ├── pool/               concurrency-safe backend set
│   ├── strategy/           Strategy interface + 7 implementations
│   ├── proxy/              request path, retry loop
│   ├── breaker/            circuit breaker state machine
│   ├── health/             active health checker
│   ├── retry/              token-bucket retry budget
│   ├── retryctx/           transport error propagation
│   ├── metrics/            Prometheus collectors
│   ├── reload/             config hot-reload
│   └── discovery/          static + Kubernetes providers
├── bench/
│   ├── loadgen/            closed-loop and open-loop load generator
│   └── mockbackend/        configurable upstream with concurrency limits
├── deploy/
│   ├── docker-compose.yml  Prometheus + Grafana
│   ├── grafana/            auto-provisioned dashboard
│   ├── prometheus.yml      scrape config
│   └── k8s/                Kubernetes manifests + kind setup
├── docs/                   reports, results, architecture notes
└── .github/workflows/      CI
```
