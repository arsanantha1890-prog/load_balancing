# Deploys GoBalance to a local kind cluster.
#
# PREREQUISITES
#   - Docker running
#   - kind installed: go install sigs.k8s.io/kind@latest
#   - kubectl installed: winget install -e --id Kubernetes.kubectl
#
# USAGE
#   .\deploy\k8s\deploy.ps1

$ErrorActionPreference = 'Stop'

$ROOT = (Get-Item $PSScriptRoot).Parent.Parent.FullName
Set-Location $ROOT

Write-Host "=== building container images ===" -ForegroundColor Cyan
docker build -t gobalance:local -f Dockerfile .
docker build -t gobalance-mock:local -f deploy/k8s/Dockerfile.mock .

Write-Host ""
Write-Host "=== creating kind cluster ===" -ForegroundColor Cyan
kind create cluster --name gobalance --config deploy/k8s/kind-config.yaml 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "cluster already exists, reusing" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "=== loading images into kind ===" -ForegroundColor Cyan
kind load docker-image gobalance:local --name gobalance
kind load docker-image gobalance-mock:local --name gobalance

Write-Host ""
Write-Host "=== applying manifests ===" -ForegroundColor Cyan
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/rbac.yaml
kubectl apply -f deploy/k8s/gobalance-k8s.yaml
kubectl apply -f deploy/k8s/mock-backend.yaml

Write-Host ""
Write-Host "=== waiting for pods ===" -ForegroundColor Cyan
kubectl -n gobalance rollout status deployment/mock-backend --timeout=60s
kubectl -n gobalance rollout status deployment/gobalance --timeout=60s

Write-Host ""
Write-Host "=== pods ===" -ForegroundColor Green
kubectl -n gobalance get pods -o wide

Write-Host ""
Write-Host "=== port-forwarding ===" -ForegroundColor Cyan
Write-Host "run these in separate terminals:"
Write-Host "  kubectl -n gobalance port-forward svc/gobalance 8080:8080"
Write-Host "  kubectl -n gobalance port-forward svc/gobalance 8081:8081"
Write-Host ""
Write-Host "then:"
Write-Host "  go run ./bench/loadgen -n 300 -c 30"
Write-Host "  kubectl -n gobalance scale deployment/mock-backend --replicas=5"
Write-Host "  Invoke-RestMethod http://127.0.0.1:8081/backends | ConvertTo-Json -Depth 4"
