# Quick comparison: heterogeneous scenario, all strategies, both modes.
# USAGE: powershell -ExecutionPolicy Bypass -File .\bench\quick-compare.ps1

$strategies = @('round_robin', 'least_connections', 'p2c', 'ewma')
$scenarios = @('heterogeneous')

& powershell -ExecutionPolicy Bypass -File .\bench\benchmark.ps1 `
    -Mode both `
    -Requests 500 `
    -Concurrency 30 `
    -RPS 300 `
    -Duration 5s `
    -Strategies $strategies `
    -Scenarios $scenarios