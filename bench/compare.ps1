# Runs the same workload through every strategy and prints a comparison table.
#
# PREREQUISITES
#   1. the three mock backends are running (alpha :9001, beta :9002, gamma :9003)
#   2. the -strategy flag patch in cmd/gobalance/PATCH.md has been applied
#   3. nothing else is listening on :8080 or :8081
#
# USAGE
#   .\bench\compare.ps1
#   .\bench\compare.ps1 -Requests 600 -Concurrency 50

param(
    [int]$Requests = 300,
    [int]$Concurrency = 30,
    [string[]]$Strategies = @('round_robin', 'weighted_round_robin', 'least_connections', 'p2c', 'ewma')
)

$ErrorActionPreference = 'Stop'

Write-Host "building..." -ForegroundColor Cyan
go build -o bin\gobalance.exe .\cmd\gobalance
if ($LASTEXITCODE -ne 0) { throw "build failed" }

# Confirm the backends are actually up before measuring anything.
foreach ($port in 9001, 9002, 9003) {
    try {
        Invoke-RestMethod "http://127.0.0.1:$port/healthz" -TimeoutSec 2 | Out-Null
    } catch {
        throw "no mock backend on port $port -- start them first"
    }
}

$results = @()

foreach ($s in $Strategies) {
    Write-Host ""
    Write-Host "=== $s ===" -ForegroundColor Yellow

    $proc = Start-Process -FilePath .\bin\gobalance.exe `
        -ArgumentList '-config', 'gobalance.yaml', '-strategy', $s, '-log-level', 'warn' `
        -PassThru -WindowStyle Hidden

    Start-Sleep -Milliseconds 800

    try {
        # A short warm-up. EWMA in particular has no latency history on its very
        # first requests, so measuring from a cold start would understate it.
        go run .\bench\loadgen -n 100 -c $Concurrency | Out-Null

        $output = go run .\bench\loadgen -n $Requests -c $Concurrency | Out-String
        Write-Host $output

        $parse = {
            param($text, $pattern)
            if ($text -match $pattern) { return $matches[1] } else { return '' }
        }

        $results += [pscustomobject]@{
            strategy   = $s
            throughput = (& $parse $output 'throughput\s+([\d.]+)')
            mean       = (& $parse $output 'mean\s+(\S+)')
            p50        = (& $parse $output 'p50\s+(\S+)')
            p90        = (& $parse $output 'p90\s+(\S+)')
            p95        = (& $parse $output 'p95\s+(\S+)')
            p99        = (& $parse $output 'p99\s+(\S+)')
            gammaShare = (& $parse $output 'gamma\s+\d+\s+([\d.]+)%')
        }
    }
    finally {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 400
    }
}

Write-Host ""
Write-Host "=== comparison ===" -ForegroundColor Green
$results | Format-Table -AutoSize

$results | Export-Csv -Path docs\results-week2.csv -NoTypeInformation
Write-Host "saved to docs\results-week2.csv"
