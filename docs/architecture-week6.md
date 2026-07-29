# Architecture notes — week 6

Append to `docs/architecture.md`.

## What was added

| Component | Purpose |
|---|---|
| Open-loop load generator | constant arrival rate, measures coordinated omission |
| Concurrency-limited mock backend | real queueing, real tail latency |
| Benchmark automation | 4 strategies × 4 scenarios × 2 modes, CSV output |

## Coordinated omission: why weeks 1–5 understated the problem

Weeks 1–5 used a closed-loop load generator: 30 workers, each sends a new
request only when the previous finishes. When gamma takes 92ms, the worker
waits 92ms before sending again — the arrival rate drops proportionally.

Real traffic does not slow down because one backend is slow. Users keep
arriving. The open-loop generator sends requests at a fixed rate (e.g. 500/s)
regardless of response time. If backends can't keep up, requests queue in the
generator itself, and latency includes the queueing time.

The difference between closed-loop and open-loop measurements for the same
system is called **coordinated omission**. It was named by Gil Tene (Azul
Systems) and is the single most common benchmarking mistake in the industry.

The loadgen now reports `wait time` (time from scheduled arrival to actual
dispatch). Under light load, wait time is near zero. Under overload, it grows —
and that growth **is** coordinated omission being measured rather than hidden.

## Concurrency limits: why weeks 1–5 didn't show real tail latency

The mock backend called `time.Sleep` with unlimited concurrency. Gamma served
1000 concurrent requests as happily as 1 — it was **slow** but never
**saturated**. Real backends have finite resources (connection pools, CPU
cores, goroutines, memory), and when all slots are busy, arriving requests
**queue**.

Queueing is where tail latency actually comes from. A request that arrives when
3 of 3 slots are busy must wait for one to finish before it even starts
processing. Its latency is `queue_wait + processing_time`, which can be
multiples of the base delay.

The `-max-concurrent` flag models this with a buffered channel as a semaphore:

```go
sem = make(chan struct{}, maxConcurrent)

// In the handler:
select {
case sem <- struct{}{}:
    defer func() { <-sem }()
default:
    http.Error(w, "503", http.StatusServiceUnavailable)
    return
}
```

When all slots are full, excess requests either queue (if the select blocks) or
are rejected with 503 (if using the default branch). This is exactly how
connection pools in database drivers and HTTP servers behave.

## The four scenarios

| Scenario | Control variable | What it isolates |
|---|---|---|
| Homogeneous | nothing differs | control — every strategy should be equal |
| Heterogeneous | gamma is 8× slower | routing quality: does the strategy avoid gamma? |
| Heavy-tail | 5–10% of requests take 500ms–1s | reaction speed to transient slowness |
| Concurrency-limited | gamma has 3 slots vs 10 | queueing under saturation |

The homogeneous scenario is the **control**. If strategies differ there, the
benchmark has a bug. It exists to validate the measurement, not the algorithm.

## Expected findings

### Round-robin fails under concurrency limits

Round-robin sends 33% of traffic to gamma regardless. With 3 concurrent slots,
gamma saturates immediately at moderate RPS. Every request beyond slot 3 either
queues or is rejected. The result is 503 errors and latency that scales with
the queue depth.

### P2C handles saturation gracefully

P2C reads the in-flight counter. When gamma's slots fill, its in-flight count
rises, and P2C stops selecting it. Traffic flows to alpha and beta where
capacity exists. No configuration, no tuning — the algorithm simply works.

### EWMA is the most aggressive avoider

EWMA's cost function is `latency × (inflight + 1)`. Under concurrency limits,
gamma's latency includes queue wait, which EWMA sees and penalises. It may
reduce gamma to near-zero traffic — which is optimal if gamma is uniformly
slow, but potentially wrong if some requests specifically need gamma.

### The delta is larger under concurrency limits

In weeks 1–5 (unlimited concurrency), the penalty for routing to gamma was one
slow response. Under concurrency limits, the penalty is queueing that affects
multiple subsequent requests. Bad routing decisions compound, so the gap
between round-robin and P2C/EWMA should be **wider** than in the unlimited
case.

## Honest limitations

| Limitation | Consequence |
|---|---|
| Go's goroutine scheduler adds noise at high RPS | jitter in open-loop timing |
| Single-machine benchmarks share CPU between loadgen, balancer, and backends | results are relative, not absolute |
| 3 backends is too few for the herd-avoidance argument to matter | P2C vs LC delta is about O(1) vs O(n), which needs n >> 3 |
| Budget ratio is untested across scenarios | the budget may be too generous or too tight |
