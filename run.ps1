#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Boots the ssh.night.ms server locally — starts a Postgres + Redis container,
    builds the binary, and runs nightms with the right connection strings.
    Runs on Windows (PowerShell 5.1 or 7+) and Linux/macOS (PowerShell 7+).

.DESCRIPTION
    Single-host dev loop. Requires Docker (Docker Desktop on Windows/macOS,
    docker engine on Linux). Container names + ports match the .NET stack's
    run.ps1, so you can use either stack against the same data (one at a
    time — never both online together).

    Carbonyl rich-mode browser:
      * Linux native run: works out of the box. The Carbonyl bundle from
        bundle/ is auto-extracted to bin/carbonyl/ on first run.
      * Windows native run: Carbonyl is Linux-only, so the "Web" lobby item
        is hidden. Use -Docker to test the full prod image instead.
      * -Docker (any platform): builds + runs the prod image, Carbonyl
        always works there.

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
    Skip the `go build` step (use the already-built binary in bin/). With
    -Docker this also skips the `docker build` step.

.PARAMETER Docker
    Run nightms inside the prod Docker image instead of as a native Windows
    binary. This is the only way to test rich-mode Carbonyl browsing locally
    (the Carbonyl bundle is Linux-only), so use this when you want a true
    "simulate prod" loop. Postgres + Redis still run in their own containers
    on the host; the nightms container reaches them via host.docker.internal.
    Slower per iteration than the native build but matches the deployed image
    1:1.

.EXAMPLE
    .\run.ps1
    Start everything with defaults (native Windows build).

.EXAMPLE
    .\run.ps1 -SysopHandle alice -Reset
    Wipe the database and bring it back up with alice as the bootstrap sysop.

.EXAMPLE
    .\run.ps1 -Docker
    Build + run the prod Docker image locally. Picks up the Carbonyl bundle
    from bundle/carbonyl-linux-x86_64.tar.xz so rich mode actually works.

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
    [switch]$NoBuild,
    [switch]$Docker
)

$ErrorActionPreference = 'Stop'
$RepoRoot = $PSScriptRoot
# $IsWindows is a built-in automatic variable in PS6+. Fallback to the env
# probe so the same script still runs under Windows PowerShell 5.1.
$IsWin = if ($null -ne $IsWindows) { $IsWindows } else { $env:OS -eq 'Windows_NT' }
$PgContainer = 'nightms-pg'
$RedisContainer = 'nightms-redis'
$AppContainer = 'nightms-app'
$DockerImageTag = 'nightms:local'
# IO.Path.Combine picks the right separator for the platform and works on every
# PS version. Backslash literals would silently misbehave on Linux.
$HostKeyDir = [IO.Path]::Combine($RepoRoot, 'data', 'host-keys')
$BinDir = [IO.Path]::Combine($RepoRoot, 'bin')
$BinName = if ($IsWin) { 'nightms.exe' } else { 'nightms' }
$BinPath = [IO.Path]::Combine($BinDir, $BinName)
# Carbonyl bundle paths — only used on the Linux native path. The Linux bundle
# is what's shipped in the repo; on Windows native it stays packed (we can't
# run it), and -Docker extracts inside the image via its own bundle stage.
$BundlePath = [IO.Path]::Combine($RepoRoot, 'bundle', 'carbonyl-linux-x86_64.tar.xz')
$CarbonylDir = [IO.Path]::Combine($BinDir, 'carbonyl')
$CarbonylBin = [IO.Path]::Combine($CarbonylDir, 'carbonyl')

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Note($msg) { Write-Host "    $msg" -ForegroundColor DarkGray }

function Stop-Containers {
    Write-Step "Stopping nightms (if running)"
    Get-Process nightms -ErrorAction SilentlyContinue | Stop-Process -Force
    Write-Step "Removing $AppContainer + $PgContainer + $RedisContainer"
    docker rm -f $AppContainer $PgContainer $RedisContainer 2>&1 | Out-Null
    Write-Note "Done."
}

if ($Stop) {
    Stop-Containers
    return
}

# --- Preflight ----------------------------------------------------------------
Write-Step "Preflight"

# Docker is mandatory either way: native mode needs it for Postgres/Redis,
# Docker mode needs it for everything.
$null = & docker info 2>$null
if ($LASTEXITCODE -ne 0) {
    throw "Docker daemon isn't reachable. Start Docker Desktop / dockerd and re-run."
}
Write-Note "Docker daemon: ok"

# Go is only required by the native build path. If it's missing on the host,
# silently flip to -Docker (which builds Go inside the alpine container) so a
# fresh Linux box without a Go SDK still "just works" — slower per iteration,
# but no install required.
$goAvailable = $null -ne (Get-Command go -ErrorAction SilentlyContinue)
if ($goAvailable) {
    Write-Note (& go version)
} elseif (-not $Docker) {
    Write-Step "Go not found on PATH — auto-enabling -Docker mode"
    Write-Note "Install Go 1.26+ from https://go.dev/dl/ for the faster native loop, then re-run."
    $Docker = $true
}

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

# --- Docker mode: build + run the prod image, exit when it does --------------
if ($Docker) {
    if (-not $NoBuild) {
        Write-Step "Building $DockerImageTag (this is the prod Dockerfile, bookworm-slim + carbonyl bundle)"
        Push-Location $RepoRoot
        try {
            & docker build -t $DockerImageTag .
            if ($LASTEXITCODE -ne 0) { throw "docker build failed." }
        } finally {
            Pop-Location
        }
    }

    # Drop any previous nightms container so the new run gets a clean slot.
    & docker rm -f $AppContainer 2>&1 | Out-Null

    # Ensure repo/data exists on the host before docker mounts it — otherwise
    # the daemon would create it root-owned in some configurations. The same
    # dir is used by native mode, so host keys, cookie secret, art, pfp, and
    # the per-user carbonyl profiles all stay consistent across mode switches.
    $DataDir = [IO.Path]::Combine($RepoRoot, 'data')
    if (-not (Test-Path $DataDir)) {
        New-Item -ItemType Directory -Path $DataDir | Out-Null
    }

    Write-Step "Starting nightms container on ssh :$SshPort, http :$HttpPort"
    Write-Host ""
    Write-Host "    Connect with:" -ForegroundColor Green
    Write-Host "      ssh -p $SshPort $SysopHandle@localhost" -ForegroundColor Green
    Write-Host "    Lobby will show 'Web' (Carbonyl) — pick it to test rich mode." -ForegroundColor Green
    Write-Host "    Press Ctrl+C to stop the server (Postgres + Redis containers keep running; use -Stop to tear down)." -ForegroundColor DarkGray
    Write-Host "    Host keys + carbonyl profiles persist in $DataDir." -ForegroundColor DarkGray
    Write-Host ""

    # nightms inside the container reaches the host-mapped Postgres/Redis via
    # host.docker.internal. --add-host wires that name on Linux engines; it's
    # a no-op on Docker Desktop for Windows where the resolver already knows it.
    $dbConn = "postgres://postgres:postgres@host.docker.internal:${PostgresPort}/bbs?sslmode=disable"
    $redisConn = "redis://host.docker.internal:${RedisPort}"

    # -v ${DataDir}:/data is what keeps the SSH host key stable across runs.
    # Without it every container start regenerates host keys and the user gets
    # the "REMOTE HOST IDENTIFICATION HAS CHANGED" warning from their client.
    & docker run --rm -ti `
        --name $AppContainer `
        --add-host=host.docker.internal:host-gateway `
        -p "${SshPort}:2222" `
        -p "${HttpPort}:5080" `
        -v "${DataDir}:/data" `
        -e NIGHTMS_BOOTSTRAP_SYSOP_HANDLE=$SysopHandle `
        -e NIGHTMS_DB_CONN=$dbConn `
        -e NIGHTMS_REDIS_CONN=$redisConn `
        $DockerImageTag
    return
}

# --- Native mode (fast iteration; Carbonyl works on Linux) --------------------
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

# --- Carbonyl bundle extraction (Linux native only) --------------------------
# The bundle ships via git LFS — a fresh clone without `git lfs pull` leaves a
# small text pointer in its place. Detect that and warn instead of failing in
# tar with an opaque error.
$carbonylReady = $false
if (-not $IsWin) {
    if (Test-Path $BundlePath) {
        $bundleSize = (Get-Item $BundlePath).Length
        if ($bundleSize -lt 1024) {
            Write-Host "    Note: $BundlePath looks like an LFS pointer ($bundleSize bytes)." -ForegroundColor Yellow
            Write-Host "          Run 'git lfs install && git lfs pull' to download the real bundle, then re-run." -ForegroundColor Yellow
        } elseif (-not (Test-Path $CarbonylBin)) {
            Write-Step "Extracting Carbonyl bundle to bin/carbonyl/ (one-time, ~560 MB)"
            New-Item -ItemType Directory -Path $CarbonylDir -Force | Out-Null
            & tar -C $CarbonylDir -xJf $BundlePath
            if ($LASTEXITCODE -ne 0) {
                throw "tar -xJf failed. Is xz-utils installed? (apt-get install xz-utils)"
            }
            Write-Note "Extracted: $CarbonylBin"
            $carbonylReady = $true
        } else {
            $carbonylReady = $true
        }
    }
}

# --- Host key directory -------------------------------------------------------
if (-not (Test-Path $HostKeyDir)) {
    New-Item -ItemType Directory -Path $HostKeyDir | Out-Null
}

# --- Run ----------------------------------------------------------------------
Write-Step "Starting nightms on ssh :$SshPort, http :$HttpPort"
Write-Host ""
Write-Host "    Connect with:" -ForegroundColor Green
Write-Host "      ssh -p $SshPort $SysopHandle@localhost" -ForegroundColor Green
Write-Host "    Or open:" -ForegroundColor Green
Write-Host "      http://localhost:$HttpPort/" -ForegroundColor Green
Write-Host ""
Write-Host "    Bootstrap sysop handle: $SysopHandle (promoted on boot if user exists)." -ForegroundColor DarkGray
Write-Host "    Press Ctrl+C to stop the server (containers keep running; use -Stop to tear down)." -ForegroundColor DarkGray
if ($IsWin) {
    Write-Host ""
    Write-Host "    Note: native Windows build cannot run Carbonyl (Linux-only binary)." -ForegroundColor Yellow
    Write-Host "          The 'Web' lobby item will not appear. Use -Docker to test rich mode." -ForegroundColor Yellow
} elseif ($carbonylReady) {
    Write-Host ""
    Write-Host "    Carbonyl: ready (Web lobby item will appear; pick it for full-browser mode)." -ForegroundColor Green
}
Write-Host ""

$env:NIGHTMS_BOOTSTRAP_SYSOP_HANDLE = $SysopHandle
$env:NIGHTMS_HOST_KEY_DIR = $HostKeyDir
$env:BBS_SSH_PORT = $SshPort
$env:BBS_HTTP_PORT = $HttpPort
$env:NIGHTMS_DB_CONN = "postgres://postgres:postgres@127.0.0.1:${PostgresPort}/bbs?sslmode=disable"
$env:NIGHTMS_REDIS_CONN = "redis://127.0.0.1:${RedisPort}"
if ($carbonylReady) {
    $env:NIGHTMS_CARBONYL_BIN_PATH = $CarbonylBin
}

& $BinPath
