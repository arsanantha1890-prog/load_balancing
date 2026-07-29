# Week 6 — Benchmark Guide

## What changed

| Component | Change |
|---|---|
| `bench/loadgen` | Added open-loop mode (constant arrival rate) |
| `bench/mockbackend` | Added `-max-concurrent` and `-queue-size` for concurrency limiting |
| `bench/benchmark.ps1` | Full benchmark matrix: 4 strategies × 4 scenarios × 2 modes |
| `bench/quick-compare.ps1` | Fast single-scenario comparison |

## Key concepts

### Coordinated omission

Closed-loop generators (week 1's loadgen) keep a fixed number of requests in
flight. When a backend is slow, the generator slows down too — it waits for a
response before sending the next request. This **understates** the damage
because real users don't wait.

Open-loop generators send requests at a fixed rate regardless of response time.
If backends are slow, requests pile up. The loadgen now reports `wait time` —
how long a request waited before being dispatched. High wait time = real
overload, not hidden by the measurement tool.

### Concurrency-limited backends

Previous weeks used `time.Sleep` with unlimited concurrency — gamma was slow
but never saturated. Real backends have finite resources. The `-max-concurrent`
flag limits how many requests a backend processes simultaneously. Excess
requests queue (up to `-queue-size`), then get rejected with 503.

This is what makes tail latency **real** instead of theoretical: when gamma's 3
slots are full, arriving requests wait in a queue, and their latency is
`queue_wait + processing_time`, not just processing time.

## The four scenarios

| Scenario | What it tests |
|---|---|
| `homogeneous` | all backends 10ms — control, every strategy should be equal |
| `heterogeneous` | gamma 80ms — the week-1 problem, strategies should differ |
| `heavy_tail` | 5-10% of requests take 500ms-1s — tests P2C and EWMA under variance |
| `concurrency_limited` | gamma has 3 slots vs alpha/beta with 10 — tests real queueing |

## Running

### Quick comparison (one scenario, ~3 min)

```powershell
powershell -ExecutionPolicy Bypass -File .\bench\quick-compare.ps1
```

### Full benchmark (all scenarios, ~15 min)

```powershell
powershell -ExecutionPolicy Bypass -File .\bench\benchmark.ps1
```

### Custom run

```powershell
powershell -ExecutionPolicy Bypass -File .\bench\benchmark.ps1 `
    -Mode open -RPS 800 -Duration 15s `
    -Strategies round_robin,p2c,ewma `
    -Scenarios heterogeneous,concurrency_limited
```

### Manual open-loop test

```powershell
# Start backends with concurrency limits
Start-Process .\bin\mockbackend.exe -ArgumentList '-name','alpha','-addr',':9001','-delay','10ms','-jitter','5ms','-max-concurrent','10'
Start-Process .\bin\mockbackend.exe -ArgumentList '-name','beta','-addr',':9002','-delay','10ms','-jitter','5ms','-max-concurrent','10'
Start-Process .\bin\mockbackend.exe -ArgumentList '-name','gamma','-addr',':9003','-delay','80ms','-jitter','20ms','-max-concurrent','3'

# Start balancer
go run ./cmd/gobalance -config gobalance.yaml -strategy p2c

# Open-loop test at 500 rps for 10 seconds
go run ./bench/loadgen -mode open -rps 500 -duration 10s
```

## What to look for

### Homogeneous scenario
All strategies should perform nearly identically. If they don't, something is
wrong with the measurement, not the algorithm.

### Heterogeneous scenario (the thesis test)
Round-robin: gamma gets 33%, p90 ≈ 92ms.
P2C/LC: gamma gets ~7%, p90 drops to ~15ms.
EWMA: gamma gets ~1-2%, most aggressive avoidance.

### Heavy-tail scenario
P2C and EWMA should show lower p99 than round-robin and least-connections,
because they avoid backends that *recently* responded slowly.

### Concurrency-limited scenario (the new finding)
This is where week 6 data differs from weeks 1-5. With gamma limited to 3
concurrent requests:
- Round-robin saturates gamma, causing 503s and massive queueing
- P2C sees in-flight counts rise and routes around it
- EWMA sees latency spike and routes around it faster

The delta between strategies should be **larger** than in the unlimited case,
because the penalty for bad routing is now queueing, not just slow responses.

## Commit

```powershell
git add -A
git commit -m "feat: week 6 — open-loop loadgen, concurrency-limited backends, full benchmark suite"
git push origin main
```
