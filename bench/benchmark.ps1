
param(
    [string]$Mode = 'both',       # 'closed', 'open', or 'both'
    [int]$Requests = 1000,        # closed-loop total requests
    [int]$Concurrency = 30,       # closed-loop workers
    [double]$RPS = 500,           # open-loop requests/sec
    [string]$Duration = '10s',    # open-loop duration
    [string[]]$Strategies = @('round_robin', 'least_connections', 'p2c', 'ewma'),
    [string[]]$Scenarios = @('homogeneous', 'heterogeneous', 'heavy_tail', 'concurrency_limited')
)

$ErrorActionPreference = 'Continue'

Write-Host "=== GoBalance Week 6 Benchmark ===" -ForegroundColor Cyan
Write-Host ""

# Build
Write-Host "building..." -ForegroundColor Yellow
go build -o bin\gobalance.exe .\cmd\gobalance 2>&1 | Out-Null
go build -o bin\mockbackend.exe .\bench\mockbackend 2>&1 | Out-Null
if (!(Test-Path bin\gobalance.exe) -or !(Test-Path bin\mockbackend.exe)) {
    throw "build failed"
}

# Scenario definitions: each is a set of mock backend flags
$scenarioDefs = @{
    'homogeneous' = @{
        alpha = '-name alpha -addr :9001 -delay 10ms -jitter 5ms'
        beta  = '-name beta  -addr :9002 -delay 10ms -jitter 5ms'
        gamma = '-name gamma -addr :9003 -delay 10ms -jitter 5ms'
        desc  = 'all backends identical (10ms)'
    }
    'heterogeneous' = @{
        alpha = '-name alpha -addr :9001 -delay 10ms -jitter 5ms'
        beta  = '-name beta  -addr :9002 -delay 10ms -jitter 5ms'
        gamma = '-name gamma -addr :9003 -delay 80ms -jitter 20ms'
        desc  = 'gamma 8x slower (80ms vs 10ms)'
    }
    'heavy_tail' = @{
        alpha = '-name alpha -addr :9001 -delay 10ms -jitter 5ms  -tail 0.05 -tail-delay 500ms'
        beta  = '-name beta  -addr :9002 -delay 10ms -jitter 5ms  -tail 0.05 -tail-delay 500ms'
        gamma = '-name gamma -addr :9003 -delay 80ms -jitter 20ms -tail 0.10 -tail-delay 1s'
        desc  = '5% tail at 500ms (alpha/beta), 10% tail at 1s (gamma)'
    }
    'concurrency_limited' = @{
        alpha = '-name alpha -addr :9001 -delay 10ms -jitter 5ms  -max-concurrent 10'
        beta  = '-name beta  -addr :9002 -delay 10ms -jitter 5ms  -max-concurrent 10'
        gamma = '-name gamma -addr :9003 -delay 80ms -jitter 20ms -max-concurrent 3'
        desc  = 'gamma limited to 3 concurrent (alpha/beta: 10)'
    }
}

function Stop-All {
    Get-Process mockbackend -ErrorAction SilentlyContinue | Stop-Process -Force
    Get-Process gobalance -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Milliseconds 500
}

function Start-Backends($scenario) {
    $def = $scenarioDefs[$scenario]
    foreach ($role in @('alpha', 'beta', 'gamma')) {
        $args = $def[$role] -split '\s+'
        Start-Process .\bin\mockbackend.exe -ArgumentList $args -WindowStyle Hidden
    }
    Start-Sleep -Seconds 1

    # Verify all three are up
    foreach ($port in 9001, 9002, 9003) {
        try {
            Invoke-RestMethod "http://127.0.0.1:$port/healthz" -TimeoutSec 3 | Out-Null
        } catch {
            throw "backend on port $port did not start for scenario $scenario"
        }
    }
}

$csvRows = @()

foreach ($scenario in $Scenarios) {
    $desc = $scenarioDefs[$scenario].desc
    Write-Host ""
    Write-Host "====== SCENARIO: $scenario ======" -ForegroundColor Green
    Write-Host "  $desc" -ForegroundColor DarkGray
    Write-Host ""

    foreach ($strategy in $Strategies) {
        $modes = @()
        if ($Mode -eq 'both') { $modes = @('closed', 'open') }
        else { $modes = @($Mode) }

        foreach ($m in $modes) {
            Write-Host "--- $strategy / $m ---" -ForegroundColor Yellow

            Stop-All
            Start-Backends $scenario

            # Start balancer
            $lb = Start-Process .\bin\gobalance.exe `
                -ArgumentList '-config', 'gobalance.yaml', '-strategy', $strategy, '-log-level', 'warn' `
                -PassThru -WindowStyle Hidden
            Start-Sleep -Seconds 2

            # Warm-up
            if ($m -eq 'closed') {
                go run .\bench\loadgen -n 200 -c $Concurrency -mode closed 2>&1 | Out-Null
            } else {
                go run .\bench\loadgen -rps $RPS -duration 2s -mode open 2>&1 | Out-Null
            }

            # Actual run
            if ($m -eq 'closed') {
                $output = go run .\bench\loadgen -n $Requests -c $Concurrency -mode closed -scenario "$scenario/$strategy" 2>&1 | Out-String
            } else {
                $output = go run .\bench\loadgen -rps $RPS -duration $Duration -mode open -scenario "$scenario/$strategy" 2>&1 | Out-String
            }

            Write-Host $output

            # Parse output
            $parse = {
                param($text, $pattern)
                if ($text -match $pattern) { return $matches[1] } else { return '' }
            }

            $csvRows += [pscustomobject]@{
                scenario    = $scenario
                strategy    = $strategy
                mode        = $m
                requests    = (& $parse $output 'requests\s+(\d+)')
                failures    = (& $parse $output 'failures\s+(\d+)')
                throughput  = (& $parse $output 'throughput\s+([\d.]+)')
                mean        = (& $parse $output 'mean\s+(\S+)')
                p50         = (& $parse $output 'p50\s+(\S+)')
                p90         = (& $parse $output 'p90\s+(\S+)')
                p95         = (& $parse $output 'p95\s+(\S+)')
                p99         = (& $parse $output 'p99\s+(\S+)')
                max_latency = (& $parse $output 'max\s+(\S+)')
                gamma_share = (& $parse $output 'gamma\s+\d+\s+([\d.]+)%')
            }

            Stop-Process -Id $lb.Id -Force -ErrorAction SilentlyContinue
            Start-Sleep -Milliseconds 500
        }
    }
}

Stop-All

# Write CSV
$csvPath = "docs\results-week6-benchmark.csv"
$csvRows | Export-Csv -Path $csvPath -NoTypeInformation
Write-Host ""
Write-Host "=== results saved to $csvPath ===" -ForegroundColor Green

# Summary table
Write-Host ""
Write-Host "=== comparison ===" -ForegroundColor Green
$csvRows | Format-Table scenario, strategy, mode, throughput, mean, p50, p90, p95, p99, gamma_share -AutoSize
