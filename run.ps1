<#
.SYNOPSIS
    Boots the ssh.night.ms server locally — starts a Postgres + Redis container, builds
    the solution, and runs Night.Ms.SshServer with the right connection strings.

.DESCRIPTION
    This is the standalone mode that the project's milestones were validated against.
    For the Aspire orchestrator path, run `dotnet run --project src/Night.Ms.AppHost`
    directly (also requires Docker Desktop).

.PARAMETER SysopHandle
    Handle that gets auto-promoted to sysop on first registration (or at startup if it
    already exists). Defaults to "nick".

.PARAMETER PostgresPort
    Host-side port for the Postgres container. Defaults to 55432 to avoid colliding
    with any locally-installed Postgres.

.PARAMETER RedisPort
    Host-side port for the Redis container. Defaults to 56379.

.PARAMETER SshPort
    SSH listener port for the BBS. Defaults to 2222.

.PARAMETER Stop
    Stops and removes the Postgres + Redis containers and exits. Doesn't run the server.

.PARAMETER Reset
    Drops and recreates the bbs database before starting the server. Useful when the
    schema has changed and you want a clean slate.

.EXAMPLE
    .\run.ps1
    Start everything with defaults.

.EXAMPLE
    .\run.ps1 -SysopHandle alice -Reset
    Wipe the database and bring it back up with alice as the bootstrap sysop.

.EXAMPLE
    .\run.ps1 -Stop
    Tear down the containers (and stop the server if it's still running).
#>
[CmdletBinding()]
param(
    [string]$SysopHandle = 'nick',
    [int]$PostgresPort = 55432,
    [int]$RedisPort = 56379,
    [int]$SshPort = 2222,
    [switch]$Stop,
    [switch]$Reset
)

$ErrorActionPreference = 'Stop'
$RepoRoot = $PSScriptRoot
$PgContainer = 'nightms-pg'
$RedisContainer = 'nightms-redis'
$HostKeyDir = Join-Path $RepoRoot 'data\host-keys'

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Note($msg) { Write-Host "    $msg" -ForegroundColor DarkGray }

function Stop-Containers {
    Write-Step "Stopping Night.Ms.SshServer (if running)"
    Get-Process Night.Ms.SshServer -ErrorAction SilentlyContinue | Stop-Process -Force
    Write-Step "Removing $PgContainer + $RedisContainer"
    docker rm -f $PgContainer $RedisContainer 2>&1 | Out-Null
    Write-Note "Done."
}

if ($Stop) {
    Stop-Containers
    return
}

# --- Preflight ----------------------------------------------------------------
Write-Step "Preflight"

$dotnetVersion = & dotnet --version 2>$null
if (-not $dotnetVersion) {
    throw ".NET SDK not found on PATH. Install .NET 10."
}
Write-Note ".NET SDK: $dotnetVersion"

$null = & docker info 2>$null
if ($LASTEXITCODE -ne 0) {
    throw "Docker daemon isn't reachable. Start Docker Desktop and re-run."
}
Write-Note "Docker daemon: ok"

# --- Postgres -----------------------------------------------------------------
$pgRunning = (& docker ps --filter "name=^$PgContainer$" --format '{{.Names}}') -eq $PgContainer
if (-not $pgRunning) {
    # Wipe a stopped-but-present container so the run command below doesn't conflict.
    & docker rm -f $PgContainer 2>&1 | Out-Null

    Write-Step "Starting Postgres on host port $PostgresPort"
    & docker run -d `
        --name $PgContainer `
        -e POSTGRES_PASSWORD=postgres `
        -e POSTGRES_DB=bbs `
        -p "${PostgresPort}:5432" `
        postgres:17-alpine | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "docker run for Postgres failed." }
} else {
    Write-Step "Postgres container already running ($PgContainer)"
}

# --- Redis --------------------------------------------------------------------
$redisRunning = (& docker ps --filter "name=^$RedisContainer$" --format '{{.Names}}') -eq $RedisContainer
if (-not $redisRunning) {
    & docker rm -f $RedisContainer 2>&1 | Out-Null

    Write-Step "Starting Redis on host port $RedisPort"
    & docker run -d `
        --name $RedisContainer `
        -p "${RedisPort}:6379" `
        redis:7-alpine | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "docker run for Redis failed." }
} else {
    Write-Step "Redis container already running ($RedisContainer)"
}

# --- Wait for Postgres to accept connections ----------------------------------
Write-Step "Waiting for Postgres to accept connections"
$deadline = (Get-Date).AddSeconds(30)
$ready = $false
while ((Get-Date) -lt $deadline) {
    & docker exec $PgContainer pg_isready -U postgres 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) { $ready = $true; break }
    Start-Sleep -Milliseconds 500
}
if (-not $ready) { throw "Postgres didn't become ready within 30 seconds." }
Write-Note "Postgres: ready"

# --- Optional reset -----------------------------------------------------------
if ($Reset) {
    Write-Step "Resetting bbs database"
    & docker exec $PgContainer psql -U postgres -c "DROP DATABASE IF EXISTS bbs;" | Out-Null
    & docker exec $PgContainer psql -U postgres -c "CREATE DATABASE bbs;" | Out-Null
    Write-Note "Recreated."
}

# --- Build --------------------------------------------------------------------
Write-Step "Building"
Push-Location $RepoRoot
try {
    & dotnet build --nologo --verbosity quiet
    if ($LASTEXITCODE -ne 0) { throw "dotnet build failed." }
} finally {
    Pop-Location
}

# --- Host key directory -------------------------------------------------------
if (-not (Test-Path $HostKeyDir)) {
    New-Item -ItemType Directory -Path $HostKeyDir | Out-Null
}

# --- Run ----------------------------------------------------------------------
Write-Step "Starting Night.Ms.SshServer on :$SshPort"
Write-Host ""
Write-Host "    Connect with:" -ForegroundColor Green
Write-Host "      ssh -p $SshPort <handle>@localhost" -ForegroundColor Green
Write-Host ""
Write-Host "    First connection from a key not on file lands on the registration screen." -ForegroundColor DarkGray
Write-Host "    Bootstrap sysop handle: $SysopHandle" -ForegroundColor DarkGray
Write-Host "    Press Ctrl+C to stop the server (containers keep running; use -Stop to tear down)." -ForegroundColor DarkGray
Write-Host ""

$env:Logging__LogLevel__Default = 'Information'
$env:NIGHTMS_BOOTSTRAP_SYSOP_HANDLE = $SysopHandle
$env:NIGHTMS_HOST_KEY_DIR = $HostKeyDir
$env:ConnectionStrings__bbs = "Host=127.0.0.1;Port=$PostgresPort;Database=bbs;Username=postgres;Password=postgres"
$env:ConnectionStrings__redis = "127.0.0.1:$RedisPort,abortConnect=false"

& dotnet run --project (Join-Path $RepoRoot 'src\Night.Ms.SshServer') --no-build
