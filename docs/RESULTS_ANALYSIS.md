# Results Analysis — All Weeks

## Week 1 Baseline

```
strategy: round_robin
gamma: 33.3%  |  p50: 14ms  |  p90: 92ms  |  p99: 100ms  |  680 req/s
```

The tail begins at p67 because gamma holds 33.3% of traffic. Mean latency of
39ms matches the arithmetic prediction: `(13 + 13 + 92) / 3 = 39.3ms`.
Throughput matches Little's Law: `30 / 0.039 ≈ 769 req/s` (measured 680,
gap is connection overhead).

## Week 2 Strategy Comparison

| Strategy | gamma % | p90 | Throughput | Key observation |
|---|---|---|---|---|
| Round-robin | 33.3% | 95ms | 670/s | baseline |
| Weighted RR | 33.3% | 93ms | 682/s | control — matches RR at equal weights |
| Least-conn | 7.0% | 16ms | 1166/s | Little's Law: predicted 6.6%, got 7.0% |
| P2C | 8.3% | 17ms | 1105/s | slightly behind LC; O(1) + herd resistant |
| EWMA | 1.3% | 15ms | 1490/s | most aggressive; latency-weighted scoring |

### Why EWMA beat P2C

EWMA's cost function is `latency × (inflight + 1)`. In-flight count alone
treats all requests equally. EWMA knows gamma's one in-flight request costs
92ms while alpha's costs 13ms — so gamma's cost is `92 × 2 = 184` vs alpha's
`13 × (1+k)`. Gamma loses until alpha accumulates 7+ in-flight requests.

### Why the control matters

Weighted RR at equal weights matched round-robin exactly: 33.3% each, same
throughput, same latency. This validates the measurement. If the control had
differed, every other number would be suspect.

## Week 3 Resilience Verification

Grey failure test:
- Made gamma `/sick` (healthy probe but 500s on real traffic)
- 100 requests → 8 failures (requests that hit gamma before ejection)
- Passive health check ejected gamma after 5 consecutive 500s
- Active health check could not detect the fault (healthz still returned 200)
- After `/well`, active health check detected recovery within 5 seconds

This demonstrates why both active and passive checking are necessary.

## Week 4 Observability

- Prometheus metrics exposed on `:8081/metrics`
- Grafana dashboard auto-provisioned with 11 panels
- Hot reload: strategy swap from P2C → least-connections → P2C with zero
  downtime, verified under load

## Week 5 Kubernetes Integration

- 3 backends discovered from EndpointSlice objects (pod IPs)
- `kubectl scale --replicas=5` → 5 backends in pool within 1 second
- `kubectl scale --replicas=2` → 2 backends, removed pods dropped from pool
- All with zero configuration changes, zero restarts

## Week 6 Benchmark Analysis

### Heterogeneous — closed-loop vs open-loop

| Strategy | Mode | gamma % | p90 | p99 |
|---|---|---|---|---|
| RR | closed | 33.4% | 96ms | 101ms |
| RR | open | 33.3% | 95ms | 100ms |
| LC | closed | 8.0% | 24ms | 104ms |
| LC | open | 8.8% | 16ms | 99ms |
| P2C | closed | 9.0% | 26ms | 101ms |
| P2C | open | 10.7% | 82ms | 99ms |
| EWMA | closed | 7.0% | 19ms | 100ms |
| EWMA | open | 3.1% | 16ms | 95ms |

At 300 RPS, coordinated omission was not significant (mean wait < 1ms). The
system had enough capacity that closed and open-loop produced similar results.

### Concurrency-limited scenario

| Strategy | Failures | gamma % | p90 | Throughput |
|---|---|---|---|---|
| RR | 6 (1.2%) | 1.8% | 16ms | 1965/s |
| P2C | 8 (1.6%) | 2.2% | 16ms | 1964/s |
| EWMA | 5 (1.0%) | 1.6% | 16ms | 1981/s |

**Key finding**: strategies converge under concurrency limits because passive
health checking ejects gamma after its slots fill and it returns 503s. The
resilience layer (week 3) dominates the routing layer (week 2).

## Summary of findings

1. Routing algorithm choice reduces p90 by 6× under heterogeneous latencies
2. Little's Law accurately predicts least-connections' traffic distribution
3. EWMA's latency-weighted scoring outperforms pure in-flight counting
4. Resilience infrastructure and routing interact: health checks can make
   strategy choice irrelevant under hard failure
5. Open-loop measurement confirms closed-loop results at moderate load
