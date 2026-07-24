# Week 4 — Setup Guide

## Prerequisites

- Docker and Docker Compose (for Prometheus + Grafana)
- Everything from weeks 1–3 working

## 1. Install new dependencies

```powershell
cd D:\Load_balancing\gobalance
go get github.com/prometheus/client_golang/prometheus
go get github.com/prometheus/client_golang/prometheus/promhttp
go get github.com/fsnotify/fsnotify
go mod tidy
```

## 2. Build and test

```powershell
go build ./...
go test -race ./...
```

## 3. Start Prometheus and Grafana

```powershell
cd deploy
docker compose up -d
cd ..
```

Wait ~10 seconds, then:
- Prometheus: http://localhost:9090
- Grafana: http://localhost:3000 (login: admin / admin)

The GoBalance dashboard appears automatically under Dashboards.

## 4. Start backends and balancer

```powershell
Start-Process .\bin\mockbackend.exe -ArgumentList '-name','alpha','-addr',':9001','-delay','10ms','-jitter','5ms'
Start-Process .\bin\mockbackend.exe -ArgumentList '-name','beta','-addr',':9002','-delay','10ms','-jitter','5ms'
Start-Process .\bin\mockbackend.exe -ArgumentList '-name','gamma','-addr',':9003','-delay','80ms','-jitter','20ms'

go run ./cmd/gobalance -config gobalance.yaml
```

## 5. Verify metrics

```powershell
Invoke-RestMethod http://127.0.0.1:8081/metrics
```

You should see `gobalance_requests_total`, `gobalance_request_duration_seconds_bucket`, etc.

## 6. Generate traffic and watch the dashboard

```powershell
go run ./bench/loadgen -n 1000 -c 30
```

Open http://localhost:3000, navigate to the GoBalance dashboard, and watch the
panels fill in: request rate by backend, latency percentiles, traffic share pie
chart.

## 7. Chaos test with the dashboard open

With traffic flowing:

```powershell
Invoke-RestMethod http://127.0.0.1:9003/toggle
```

Watch the dashboard:
- Backend Health panel: gamma drops from 1 to 0
- Breaker State: gamma jumps from 0 (closed) to 1 (open)
- Request Rate: gamma's line disappears
- Retry Budget: tokens dip slightly

Restore gamma:

```powershell
Invoke-RestMethod http://127.0.0.1:9003/toggle
```

Watch breaker go 1 → 0.5 → 0 (open → half-open → closed), health go 0 → 1.

## 8. Test hot reload

With the balancer running, edit `gobalance.yaml`:
- Change `strategy: p2c` to `strategy: least_connections`
- Save the file

Watch the balancer's log — within ~1 second:

```
level=INFO msg="config reloaded — backend pool updated" ...
level=INFO msg="strategy reloaded" strategy=least_connections
```

No restart, no dropped connections, no interruption.

## 9. Teardown

```powershell
Get-Process mockbackend | Stop-Process
# Ctrl+C the balancer
cd deploy
docker compose down
cd ..
```

## 10. Commit

```powershell
git add -A
git commit -m "feat: week 4 — Prometheus metrics, Grafana dashboard, config hot reload"
git push origin main
```
