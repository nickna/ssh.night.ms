<#
.SYNOPSIS
    Boots the ssh.night.ms server locally — starts a Postgres + Redis container,
    builds the binary, and runs nightms with the right connection strings.

.DESCRIPTION
    Single-host dev loop. Requires Docker Desktop. Container names + ports match
    the .NET stack's run.ps1, so you can use either stack against the same data
    (one at a time — never both online together).

.PARAMETER SysopHandle
    Handle that gets auto-promoted to sysop on startup if it already exists.
    Defaults to "nick".

.PARAMETER PostgresPort
    Host-side port for the Postgres container. Defaults to 55432.

.PARAMETER RedisPort
    Host-side port for the Redis container. Defaults to 56379.

.PARAMETER SshPort
    SSH listener port for the BBS. Defaults to 2222.

.PARAMETER HttpPort
    HTTP listener port for the web server. Defaults to 5080.

.PARAMETER Stop
    Stops and removes the Postgres + Redis containers and exits. Doesn't run
    the server.

.PARAMETER Reset
    Drops and recreates the bbs database before starting the server. Useful
    when migrations have changed and you want a clean slate.

.PARAMETER NoBuild
    Skip the `go build` step (use the already-built binary in bin/).

.EXAMPLE
    .\run.ps1
    Start everything with defaults.

.EXAMPLE
    .\run.ps1 -SysopHandle alice -Reset
    Wipe the database and bring it back up with alice as the bootstrap sysop.

.EXAMPLE
    .\run.ps1 -Stop
    Tear down the containers.
#>
[CmdletBinding()]
param(
    [string]$SysopHandle = 'nick',
    [int]$PostgresPort = 55432,
    [int]$RedisPort = 56379,
    [int]$SshPort = 2222,
    [int]$HttpPort = 5080,
    [switch]$Stop,
    [switch]$Reset,
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'
$RepoRoot = $PSScriptRoot
$PgContainer = 'nightms-pg'
$RedisContainer = 'nightms-redis'
$HostKeyDir = Join-Path $RepoRoot 'data\host-keys'
$BinDir = Join-Path $RepoRoot 'bin'
$BinPath = Join-Path $BinDir 'nightms.exe'

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Note($msg) { Write-Host "    $msg" -ForegroundColor DarkGray }

function Stop-Containers {
    Write-Step "Stopping nightms (if running)"
    Get-Process nightms -ErrorAction SilentlyContinue | Stop-Process -Force
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

$goVersion = & go version 2>$null
if (-not $goVersion) {
    throw "Go SDK not found on PATH. Install Go 1.26 or newer."
}
Write-Note "$goVersion"

$null = & docker info 2>$null
if ($LASTEXITCODE -ne 0) {
    throw "Docker daemon isn't reachable. Start Docker Desktop and re-run."
}
Write-Note "Docker daemon: ok"

# --- Postgres -----------------------------------------------------------------
$pgRunning = (& docker ps --filter "name=^$PgContainer$" --format '{{.Names}}') -eq $PgContainer
if (-not $pgRunning) {
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
if (-not $NoBuild) {
    Write-Step "Building nightms"
    if (-not (Test-Path $BinDir)) {
        New-Item -ItemType Directory -Path $BinDir | Out-Null
    }
    Push-Location $RepoRoot
    try {
        & go build -o $BinPath ./cmd/nightms
        if ($LASTEXITCODE -ne 0) { throw "go build failed." }
    } finally {
        Pop-Location
    }
}
if (-not (Test-Path $BinPath)) {
    throw "Binary not found at $BinPath. Run without -NoBuild first."
}

# --- Host key directory -------------------------------------------------------
if (-not (Test-Path $HostKeyDir)) {
    New-Item -ItemType Directory -Path $HostKeyDir | Out-Null
}

# --- Run ----------------------------------------------------------------------
Write-Step "Starting nightms on ssh :$SshPort, http :$HttpPort"
Write-Host ""
Write-Host "    Connect with:" -ForegroundColor Green
Write-Host "      ssh -p $SshPort <handle>@localhost" -ForegroundColor Green
Write-Host "    Or open:" -ForegroundColor Green
Write-Host "      http://localhost:$HttpPort/" -ForegroundColor Green
Write-Host ""
Write-Host "    No signup screen yet — seed a user first:" -ForegroundColor DarkGray
Write-Host "      go run ./cmd/seeduser -handle $SysopHandle -password <pw>" -ForegroundColor DarkGray
Write-Host "    Bootstrap sysop handle: $SysopHandle (promoted on boot if user exists)." -ForegroundColor DarkGray
Write-Host "    Press Ctrl+C to stop the server (containers keep running; use -Stop to tear down)." -ForegroundColor DarkGray
Write-Host ""

$env:NIGHTMS_BOOTSTRAP_SYSOP_HANDLE = $SysopHandle
$env:NIGHTMS_HOST_KEY_DIR = $HostKeyDir
$env:BBS_SSH_PORT = $SshPort
$env:BBS_HTTP_PORT = $HttpPort
$env:NIGHTMS_DB_CONN = "postgres://postgres:postgres@127.0.0.1:${PostgresPort}/bbs?sslmode=disable"
$env:NIGHTMS_REDIS_CONN = "redis://127.0.0.1:${RedisPort}"

& $BinPath
