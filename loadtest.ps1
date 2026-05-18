<#
.SYNOPSIS
    Runs the Night.Ms.Tools.LoadTest harness against a locally-running ssh.night.ms
    server. Auto-seeds the database if not enough bot keys are on disk, then opens N
    SSH sessions, runs the scenario mix, and writes a stdout table + CSV + JSON report.

.DESCRIPTION
    Companion to run.ps1 — run that one first to bring the server + Postgres + Redis
    up, then run this in a second shell.

    Defaults are smoke-test friendly (10 bots, 30 s duration, 5 s ramp). For a real
    run, pass -Count, -RampSeconds, -DurationSeconds explicitly. The 30 s / 5 min
    profile from the plan corresponds to:
      .\loadtest.ps1 -Count 500 -RampSeconds 30 -DurationSeconds 300

.PARAMETER Count
    Number of synthetic bots. Each bot gets its own RSA-2048 keypair persisted under
    -KeysDir. Defaults to 10 (smoke).

.PARAMETER SshPort
    SSH listener port the bots connect to. Must match run.ps1 -SshPort. Defaults to 2222.

.PARAMETER PostgresPort
    Postgres host port for seed/clean. Must match run.ps1 -PostgresPort. Defaults to 55432.

.PARAMETER RampSeconds
    Window over which bots ramp up. Defaults to 5 (smoke). Use 30 for real runs.

.PARAMETER DurationSeconds
    Steady-state duration after ramp. Defaults to 30 (smoke). Use 300 for real runs.

.PARAMETER Mix
    Scenario proportions, "chat/forum/idle". Defaults to "60/30/10".

.PARAMETER Host
    SSH host the bots connect to. Defaults to "localhost".

.PARAMETER KeysDir
    Per-bot private keys directory. Defaults to ./loadtest-keys at the repo root so
    runs share keys across rebuilds (the tool's default lands under bin/).

.PARAMETER ReportsDir
    Where CSV + JSON reports are written. Defaults to ./loadtest-reports at the repo root.

.PARAMETER Gate
    Path to a thresholds JSON file. If set, the harness exits 1 when any threshold is
    breached. Without it, the run is purely informational (exit 0 always).

.PARAMETER SeedOnly
    Just seed the configured -Count of bots, then exit. Doesn't connect or run.

.PARAMETER CleanOnly
    Delete every loadbot-* user (cascade drops their credentials), then exit. Doesn't
    touch on-disk keys; pass -DeleteKeys to wipe those too.

.PARAMETER DeleteKeys
    With -CleanOnly, also delete the local -KeysDir so the next seed regenerates fresh
    keypairs. Useful when fingerprints have drifted out of sync with the database.

.PARAMETER Reset
    Equivalent to running with -CleanOnly first, then a normal run with the same other
    flags. Re-seeds from scratch.

.EXAMPLE
    .\loadtest.ps1
    Smoke: 10 bots, 5 s ramp, 30 s steady. Useful sanity check that the harness can
    connect at all.

.EXAMPLE
    .\loadtest.ps1 -Count 50 -RampSeconds 10 -DurationSeconds 60
    A step-up beyond smoke without committing to a full 5-minute run.

.EXAMPLE
    .\loadtest.ps1 -Count 500 -RampSeconds 30 -DurationSeconds 300 -Gate .\loadtest-thresholds.json
    Full target run with pass/fail gating from the thresholds file.

.EXAMPLE
    .\loadtest.ps1 -SeedOnly -Count 500
    Just pre-seed 500 bots — useful when you want the long seed to finish out-of-band
    before the next run.

.EXAMPLE
    .\loadtest.ps1 -CleanOnly
    Remove all loadbot-* users from the database (keeps on-disk keys for re-seeding).
#>
[CmdletBinding()]
param(
    [int]$Count = 10,
    [int]$SshPort = 2222,
    [int]$PostgresPort = 55432,
    [int]$RampSeconds = 5,
    [int]$DurationSeconds = 30,
    [string]$Mix = '60/30/10',
    [string]$BbsHost = 'localhost',
    [string]$KeysDir,
    [string]$ReportsDir,
    [string]$Gate,
    [switch]$SeedOnly,
    [switch]$CleanOnly,
    [switch]$DeleteKeys,
    [switch]$Reset
)

$ErrorActionPreference = 'Stop'
$RepoRoot = $PSScriptRoot
$Project = Join-Path $RepoRoot 'src\Night.Ms.Tools.LoadTest'

if (-not $KeysDir)    { $KeysDir    = Join-Path $RepoRoot 'loadtest-keys' }
if (-not $ReportsDir) { $ReportsDir = Join-Path $RepoRoot 'loadtest-reports' }

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Note($msg) { Write-Host "    $msg" -ForegroundColor DarkGray }

# Match run.ps1's connection-string convention so seed/clean talk to the same Postgres
# container the server is bound to.
$env:ConnectionStrings__bbs = "Host=127.0.0.1;Port=$PostgresPort;Database=bbs;Username=postgres;Password=postgres"

# --- Preflight ----------------------------------------------------------------
Write-Step 'Preflight'

$dotnetVersion = & dotnet --version 2>$null
if (-not $dotnetVersion) { throw '.NET SDK not found on PATH. Install .NET 10.' }
Write-Note ".NET SDK: $dotnetVersion"

# Build once. Subsequent dotnet run uses --no-build so seed + run aren't redundant.
Write-Step 'Building Night.Ms.Tools.LoadTest'
Push-Location $RepoRoot
try {
    & dotnet build $Project --nologo --verbosity quiet
    if ($LASTEXITCODE -ne 0) { throw 'dotnet build failed.' }
} finally {
    Pop-Location
}

function Invoke-LoadTest {
    param([string[]]$Args)
    & dotnet run --project $Project --no-build -- @Args
    return $LASTEXITCODE
}

# --- CleanOnly ----------------------------------------------------------------
if ($CleanOnly) {
    Write-Step 'Cleaning loadbot-* users from database'
    $code = Invoke-LoadTest @('clean')
    if ($code -ne 0) { throw "clean failed (exit $code)" }

    if ($DeleteKeys -and (Test-Path $KeysDir)) {
        Write-Step "Deleting on-disk keys at $KeysDir"
        Remove-Item -Recurse -Force $KeysDir
    }
    return
}

# --- Reset = clean then continue ----------------------------------------------
if ($Reset) {
    Write-Step 'Reset: cleaning loadbot-* before re-seeding'
    $code = Invoke-LoadTest @('clean')
    if ($code -ne 0) { throw "clean failed (exit $code)" }
    if ($DeleteKeys -and (Test-Path $KeysDir)) {
        Remove-Item -Recurse -Force $KeysDir
    }
}

# --- Auto-seed when needed ----------------------------------------------------
# Count the .pem files we have. If fewer than -Count, seed up to Count (idempotent —
# the tool only inserts missing rows / regenerates missing keys).
$existingKeyCount = 0
if (Test-Path $KeysDir) {
    $existingKeyCount = (Get-ChildItem -Path $KeysDir -Filter 'loadbot-*.pem' -ErrorAction SilentlyContinue | Measure-Object).Count
}

if ($SeedOnly -or $existingKeyCount -lt $Count) {
    if ($SeedOnly) {
        Write-Step "Seeding $Count bots (SeedOnly)"
    } else {
        Write-Step "Auto-seeding: have $existingKeyCount keys, need $Count"
    }
    $code = Invoke-LoadTest @('seed', '--count', "$Count", '--keys-dir', $KeysDir)
    if ($code -ne 0) { throw "seed failed (exit $code)" }
    if ($SeedOnly) { return }
} else {
    Write-Note "Skipping seed: $existingKeyCount keys already on disk, need $Count."
}

# --- Run ----------------------------------------------------------------------
$total = $RampSeconds + $DurationSeconds
Write-Step "Running: N=$Count mix=$Mix ramp=${RampSeconds}s steady=${DurationSeconds}s (≈${total}s wall + drain)"
Write-Note "host=${BbsHost}:$SshPort  reports=$ReportsDir"

$runArgs = @(
    'run'
    '--count'; "$Count"
    '--host'; $BbsHost
    '--port'; "$SshPort"
    '--ramp-seconds'; "$RampSeconds"
    '--duration-seconds'; "$DurationSeconds"
    '--mix'; $Mix
    '--keys-dir'; $KeysDir
    '--reports-dir'; $ReportsDir
)
if ($Gate) {
    if (-not (Test-Path $Gate)) { throw "Gate file not found: $Gate" }
    $runArgs += @('--gate', $Gate)
}

$code = Invoke-LoadTest $runArgs
if ($code -ne 0) {
    Write-Host ''
    Write-Host "loadtest exited with $code." -ForegroundColor Yellow
}
exit $code
