<#
.SYNOPSIS
    Runs loadtest.ps1 at a progression of -Count values and prints a single comparison
    table at the end so the inflection point — where p95 climbs sharply or CPU pegs —
    is obvious at a glance.

.DESCRIPTION
    Each step writes its loadtest-*.json + server-metrics-*.csv into its own
    subdirectory under ./loadtest-reports/sweep-{stamp}/N{count}/ so the per-run
    artefacts are preserved alongside the combined summary.

    Pre-seeds the maximum-N keypairs once up-front so individual steps don't pay
    the seed cost during their ramp. Between steps, sleeps briefly so the server's
    GC + Postgres pool can settle before the next ramp.

.PARAMETER Counts
    Array of bot counts to sweep through. Default: 50, 100, 250, 500.

.PARAMETER SshPort, PostgresPort, BbsHost, Mix, KeysDir
    Pass-through to loadtest.ps1; defaults match.

.PARAMETER SettleSeconds
    Pause between steps so the server quiesces. Default 15.

.EXAMPLE
    .\sweep.ps1
    Full default progression: N = 50, 100, 250, 500.

.EXAMPLE
    .\sweep.ps1 -Counts 100, 500
    Skip the middle steps — useful when you've already identified the inflection
    region and want a confirmation run at the extremes.
#>
[CmdletBinding()]
param(
    [int[]]$Counts = @(50, 100, 250, 500),
    [int]$SshPort = 2222,
    [int]$PostgresPort = 55432,
    [string]$BbsHost = '127.0.0.1',
    [string]$Mix = '60/30/10',
    [string]$KeysDir,
    [int]$SettleSeconds = 15
)

$ErrorActionPreference = 'Stop'
$RepoRoot = $PSScriptRoot
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

if (-not $KeysDir) { $KeysDir = Join-Path $RepoRoot 'loadtest-keys' }
$Stamp = (Get-Date).ToString('yyyyMMdd-HHmmss')
$SweepRoot = Join-Path $RepoRoot "loadtest-reports\sweep-$Stamp"
New-Item -ItemType Directory -Path $SweepRoot | Out-Null

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Note($msg) { Write-Host "    $msg" -ForegroundColor DarkGray }

# Ramp + duration that scale with N. The intent: ramp long enough for steady state to
# dominate, duration long enough to collect a few thousand chat samples per step.
function Get-RunShape {
    param([int]$N)
    if     ($N -le 50)   { return @{ Ramp = 10; Duration = 60  } }
    elseif ($N -le 100)  { return @{ Ramp = 20; Duration = 120 } }
    elseif ($N -le 250)  { return @{ Ramp = 30; Duration = 180 } }
    else                 { return @{ Ramp = 30; Duration = 300 } }
}

# --- Pre-seed -----------------------------------------------------------------
$maxCount = ($Counts | Measure-Object -Maximum).Maximum
Write-Step "Pre-seeding $maxCount bots so individual steps skip the seed cost"
& "$RepoRoot\loadtest.ps1" -SeedOnly -Count $maxCount -SshPort $SshPort -PostgresPort $PostgresPort -KeysDir $KeysDir
if ($LASTEXITCODE -ne 0) { throw "pre-seed failed (exit $LASTEXITCODE)" }

# --- Run the sweep ------------------------------------------------------------
$stepResults = @()
$stepIndex = 0
foreach ($n in $Counts) {
    $stepIndex++
    $shape = Get-RunShape -N $n
    $perRunDir = Join-Path $SweepRoot "N$n"
    New-Item -ItemType Directory -Path $perRunDir | Out-Null

    Write-Host ''
    Write-Step "Step $stepIndex/$($Counts.Count): N=$n  ramp=$($shape.Ramp)s  duration=$($shape.Duration)s"

    & "$RepoRoot\loadtest.ps1" `
        -Count $n `
        -SshPort $SshPort -PostgresPort $PostgresPort -BbsHost $BbsHost `
        -RampSeconds $shape.Ramp -DurationSeconds $shape.Duration `
        -Mix $Mix `
        -KeysDir $KeysDir `
        -ReportsDir $perRunDir
    $stepExit = $LASTEXITCODE

    $stepResults += [pscustomobject]@{
        Count    = $n
        Ramp     = $shape.Ramp
        Duration = $shape.Duration
        ExitCode = $stepExit
        Dir      = $perRunDir
    }

    if ($stepIndex -lt $Counts.Count) {
        Write-Note "settle $SettleSeconds s before next step"
        Start-Sleep -Seconds $SettleSeconds
    }
}

# --- Aggregate ----------------------------------------------------------------
function Read-LatencyJson {
    param([string]$Dir)
    $json = Get-ChildItem -Path $Dir -Filter 'loadtest-*.json' -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $json) { return $null }
    return Get-Content -Path $json.FullName -Raw | ConvertFrom-Json
}

function Read-ServerMetrics {
    param([string]$Dir)
    $csv = Get-ChildItem -Path $Dir -Filter 'server-metrics-*.csv' -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $csv) { return $null }
    return Import-Csv -Path $csv.FullName
}

function Get-LatencyOrDash {
    param($Latencies, [string]$Metric, [string]$Percentile)
    if (-not $Latencies) { return '-' }
    $entry = $Latencies.$Metric
    if (-not $entry) { return '-' }
    $val = $entry.$Percentile
    if ($null -eq $val) { return '-' }
    return ('{0,7:N0}ms' -f [double]$val)
}

function Get-StatsOrDash {
    param([object[]]$Rows, [string]$Column, [string]$Stat, [string]$Suffix = '')
    if (-not $Rows -or $Rows.Count -eq 0) { return '-' }
    $vals = $Rows | ForEach-Object { [double]$_.$Column }
    $v = switch ($Stat) {
        'Mean' { [math]::Round(($vals | Measure-Object -Average).Average, 1) }
        'Max'  { ($vals | Measure-Object -Maximum).Maximum }
        default { 0 }
    }
    return ('{0,6:N1}{1}' -f $v, $Suffix)
}

function Get-TotalErrors {
    param($Errors)
    if (-not $Errors) { return 0 }
    $sum = 0
    foreach ($p in $Errors.PSObject.Properties) { $sum += [long]$p.Value }
    return $sum
}

Write-Host ''
Write-Step 'Sweep summary'
Write-Host ('─' * 110)
Write-Host ('{0,5} {1,10} {2,10} {3,10} {4,10} {5,9} {6,9} {7,9} {8,9} {9,8}' -f `
    'N','chat p50','chat p95','chat p99','connect p50','cpu mean','cpu max','ws mean','ws max','errors')
Write-Host ('─' * 110)

foreach ($r in $stepResults) {
    $report  = Read-LatencyJson    -Dir $r.Dir
    $metrics = Read-ServerMetrics  -Dir $r.Dir
    $latencies = if ($report) { $report.latencies } else { $null }

    $chatP50 = Get-LatencyOrDash -Latencies $latencies -Metric 'chat.publish_to_receive_ms' -Percentile 'P50Ms'
    $chatP95 = Get-LatencyOrDash -Latencies $latencies -Metric 'chat.publish_to_receive_ms' -Percentile 'P95Ms'
    $chatP99 = Get-LatencyOrDash -Latencies $latencies -Metric 'chat.publish_to_receive_ms' -Percentile 'P99Ms'
    $connP50 = Get-LatencyOrDash -Latencies $latencies -Metric 'bot.connect_ms'             -Percentile 'P50Ms'

    $cpuMean = Get-StatsOrDash -Rows $metrics -Column 'cpu_pct'         -Stat 'Mean' -Suffix '%'
    $cpuMax  = Get-StatsOrDash -Rows $metrics -Column 'cpu_pct'         -Stat 'Max'  -Suffix '%'
    $wsMean  = Get-StatsOrDash -Rows $metrics -Column 'working_set_mb'  -Stat 'Mean' -Suffix 'MB'
    $wsMax   = Get-StatsOrDash -Rows $metrics -Column 'working_set_mb'  -Stat 'Max'  -Suffix 'MB'

    $errors = if ($report) { Get-TotalErrors -Errors $report.errors } else { '-' }

    Write-Host ('{0,5} {1,10} {2,10} {3,10} {4,10} {5,9} {6,9} {7,9} {8,9} {9,8}' -f `
        $r.Count, $chatP50, $chatP95, $chatP99, $connP50, $cpuMean, $cpuMax, $wsMean, $wsMax, $errors)
}

Write-Host ('─' * 110)
Write-Note "per-run artefacts: $SweepRoot"
