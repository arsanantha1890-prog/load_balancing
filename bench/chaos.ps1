param(
    [ValidateSet('kill', 'grey')]
    [string]$Scenario = 'kill',
    [int]$Requests = 400,
    [int]$Concurrency = 20
)

$ErrorActionPreference = 'Continue'

function Show-Pool {
    param([string]$Label)
    Write-Host ""
    Write-Host "--- $Label ---" -ForegroundColor Cyan
    try {
        $state = Invoke-RestMethod http://127.0.0.1:8081/backends -TimeoutSec 3
        $state.backends | Format-Table name, healthy, breaker, total, failures, consecutive_failures -AutoSize
        Write-Host ("retry budget tokens: {0}" -f $state.retry_budget_tokens)
    } catch {
        Write-Host "admin endpoint unreachable" -ForegroundColor Red
    }
}

Write-Host "=== chaos scenario: $Scenario ===" -ForegroundColor Yellow

Show-Pool "before"

# Background load so the balancer is busy while the fault is injected.
Write-Host ""
Write-Host "starting background load..." -ForegroundColor Cyan
$loadJob = Start-Job -ScriptBlock {
    param($dir, $n, $c)
    Set-Location $dir
    go run ./bench/loadgen -n $n -c $c 2>&1
} -ArgumentList (Get-Location).Path, $Requests, $Concurrency

Start-Sleep -Seconds 2

if ($Scenario -eq 'kill') {
    Write-Host ""
    Write-Host "KILLING gamma (:9003) -- health checks report it down" -ForegroundColor Red
    try { Invoke-RestMethod http://127.0.0.1:9003/toggle -TimeoutSec 2 } catch {}
} else {
    Write-Host ""
    Write-Host "GREY FAILURE on gamma (:9003) -- still reports healthy, but every request 500s" -ForegroundColor Red
    Write-Host "this is the case PASSIVE health checking exists for" -ForegroundColor DarkGray
    try { Invoke-RestMethod http://127.0.0.1:9003/sick -TimeoutSec 2 } catch {}
}

Start-Sleep -Seconds 4
Show-Pool "after the fault"

Write-Host ""
Write-Host "RESTORING gamma" -ForegroundColor Green
if ($Scenario -eq 'kill') {
    try { Invoke-RestMethod http://127.0.0.1:9003/toggle -TimeoutSec 2 } catch {}
} else {
    try { Invoke-RestMethod http://127.0.0.1:9003/well -TimeoutSec 2 } catch {}
}

Start-Sleep -Seconds 6
Show-Pool "after recovery"

Write-Host ""
Write-Host "load generator output:" -ForegroundColor Cyan
Receive-Job $loadJob -Wait -AutoRemoveJob

Write-Host ""
Write-Host "WHAT TO LOOK FOR" -ForegroundColor Yellow
Write-Host "  * failures > 0 on gamma, but the load generator reports few or no client errors"
Write-Host "    -> retries absorbed the fault"
Write-Host "  * gamma healthy=False during the fault, healthy=True again afterwards"
Write-Host "    -> only ACTIVE health checking can detect recovery"
Write-Host "  * breaker moving closed -> open -> half_open -> closed"
Write-Host "  * retry budget tokens dipping during the fault and refilling after"
