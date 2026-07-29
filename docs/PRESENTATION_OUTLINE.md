# GoBalance — Presentation Outline (15 minutes)

## Slide 1: Title
GoBalance — A Layer-7 HTTP Load Balancer with Pluggable Routing Strategies

## Slide 2: The Problem (1 min)
- Round-robin is the default everywhere
- It sends 33% of traffic to a backend that is 8× slower
- The tail begins at p67 — not because of the workload, because of the routing

## Slide 3: The Thesis (30 sec)
"Tail latency under heterogeneous backends is a routing property, not a
hardware property."

## Slide 4: Architecture Diagram (1 min)
- Show the request path: client → balancer → strategy.Pick → backend
- Highlight: the strategy is the only variable

## Slide 5: Five Strategies (2 min)
- Round-robin: fixed rotation, no feedback
- Least-connections: Little's Law — equalise L, λ becomes proportional to 1/W
- P2C: sample 2, pick lighter. O(1), herd-resistant
- EWMA: latency × (inflight + 1). React instantly to bad news
- Show the interface: 4 lines of Go

## Slide 6: Week 2 Results Table (2 min)
- The comparison table with gamma share and throughput
- Highlight: predicted 6.6%, measured 7.0% — theory confirmed
- EWMA at 1.3% — the surprise

## Slide 7: Resilience Layer (2 min)
- Circuit breaker state machine (3 states)
- Active + passive health checks (why both)
- Retry budget (why retries are dangerous without one)
- Grey failure demo screenshot

## Slide 8: Grafana Dashboard Screenshot (1 min)
- Live chaos demo: kill gamma, watch the panels

## Slide 9: Kubernetes Integration (1 min)
- EndpointSlice informer → pool.Set
- kubectl scale 3→5→2, pool tracks it in <1 second
- Show the /backends JSON with pod IPs

## Slide 10: Week 6 Benchmark Methodology (1 min)
- Open-loop vs closed-loop (coordinated omission)
- Concurrency-limited backends (real queueing)

## Slide 11: Week 6 Results (1 min)
- Concurrency-limited: all strategies converge
- Because passive health checking dominates
- "The resilience layer makes strategy choice irrelevant under hard failure"

## Slide 12: Limitations — State Them First (1 min)
- 3 backends too few for herd avoidance
- Single machine, relative not absolute numbers
- EWMA scores freeze without traffic
- Synthetic workloads

## Slide 13: Conclusions (1 min)
1. Tail latency is a routing property
2. Little's Law predicts the distribution
3. EWMA is strongest for stable heterogeneous latencies
4. Resilience and routing interact non-additively

## Questions to prepare for
- Why P2C over least-connections? (herd, O(1))
- Why can't you retry a 500? (bytes already sent)
- What breaks with multiple LB replicas? (P2C degrades gracefully, LC doesn't)
- What is coordinated omission? (closed-loop hides slow-backend damage)
- Why two health checks? (active detects recovery, passive catches grey failure)
