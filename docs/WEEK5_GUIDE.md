# Week 5 — Kubernetes Integration Guide

## What this adds

A `discovery.Provider` interface with two implementations:
- **Static** (default): backends from `gobalance.yaml`, same as weeks 1–4.
- **Kubernetes**: backends from EndpointSlice objects. When pods scale up or down, the pool updates automatically.

## Prerequisites

```powershell
# Install kind (Kubernetes-in-Docker)
go install sigs.k8s.io/kind@latest

# Install kubectl
winget install -e --id Kubernetes.kubectl
```

Restart PowerShell after installing so the new commands are on PATH.

## Setup — files

```powershell
cd D:\Load_balancing\gobalance

# New packages
New-Item -ItemType Directory -Force internal\discovery | Out-Null

# Copy files (adjust source path)
Copy-Item <extracted>\internal\discovery\*.go    .\internal\discovery\
Copy-Item <extracted>\internal\config\config.go  .\internal\config\ -Force
Copy-Item <extracted>\cmd\gobalance\main.go      .\cmd\gobalance\ -Force
Copy-Item <extracted>\deploy\k8s\*               .\deploy\k8s\ -Recurse

# Download k8s dependencies
go get k8s.io/client-go@latest
go get k8s.io/api@latest
go get k8s.io/apimachinery@latest
go mod tidy

# Build and test
go build ./...
go test -race ./...
```

## Static mode still works

Your existing `gobalance.yaml` has `discovery.mode: static` (the default), so
everything from weeks 1–4 works exactly as before:

```powershell
go run ./cmd/gobalance -config gobalance.yaml
```

## Deploy to kind

One script does everything:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\k8s\deploy.ps1
```

It builds images, creates a kind cluster, loads images, applies manifests, and
waits for pods.

Then in two separate terminals:

```powershell
kubectl -n gobalance port-forward svc/gobalance 8080:8080
kubectl -n gobalance port-forward svc/gobalance 8081:8081
```

## The demo

Send traffic:

```powershell
go run ./bench/loadgen -n 300 -c 30
Invoke-RestMethod http://127.0.0.1:8081/backends | ConvertTo-Json -Depth 4
```

You'll see three backends named after their pod IPs.

Scale up:

```powershell
kubectl -n gobalance scale deployment/mock-backend --replicas=5
```

Wait 5 seconds, check again:

```powershell
Invoke-RestMethod http://127.0.0.1:8081/backends | ConvertTo-Json -Depth 4
```

Five backends. No config edit, no restart.

Scale down:

```powershell
kubectl -n gobalance scale deployment/mock-backend --replicas=2
```

Two backends remain. The informer saw the EndpointSlice update and reconciled.

## Teardown

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\k8s\teardown.ps1
```

## Resume line

> Integrated with Kubernetes EndpointSlice API via client-go informers for live
> service discovery. Scaling a Deployment from 3 to 5 replicas updates the
> backend pool in under 1 second with zero configuration changes.
