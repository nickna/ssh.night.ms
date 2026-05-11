# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build / run / test

This is a .NET 10 solution (SDK pinned in `global.json`). All commands assume the repo root.

```sh
dotnet build                                                 # full solution
dotnet test                                                  # all tests
dotnet test tests/Night.Ms.SshServer.Tests                   # one project
dotnet test --filter "FullyQualifiedName~ChatServiceTests"   # one class
dotnet test --filter "FullyQualifiedName~ChatServiceTests.JoinPublicChannelAsync_CreatesChannel"  # one test
```

Two ways to run the server:

- **Aspire orchestrator** (default for full-stack work — spawns its own Postgres + Redis + pgAdmin containers): `dotnet run --project src/Night.Ms.AppHost`. SSH lands on `:2223`.
- **Standalone via `run.ps1`** (faster iterate cycle — starts bare Postgres + Redis containers `nightms-pg` / `nightms-redis`, builds, and runs SshServer on `:2222`). Use `-Reset` to drop+recreate the `bbs` database; `-Stop` to tear containers down.

Both paths require Docker Desktop running. Server connections: `ssh -p <port> <handle>@localhost`. First connection from an unknown key lands on the TOFU `RegisterScreen`.

EF Core migrations target `AppDbContext`. The project uses `AppDbContextDesignFactory` (hardcoded localhost:5432), so design-time commands need a local Postgres reachable there:

```sh
dotnet ef migrations add <Name> --project src/Night.Ms.SshServer
dotnet ef database update --project src/Night.Ms.SshServer
```

At runtime, `DatabaseInitializer` (hosted service) applies pending migrations and seeds `#lobby` + the `General` forum on startup — you do not need to run `database update` for the app itself, only for design-time scaffolding.

## Architecture

The system is one SSH server process whose only "API" is an interactive Terminal.Gui TUI rendered over the SSH channel. There's no HTTP surface.

### Process boundary: `Program.cs` wires three hosted services in order

1. `DatabaseInitializer` — applies migrations, seeds defaults.
2. `SysopBootstrap` — promotes the handle in `NIGHTMS_BOOTSTRAP_SYSOP_HANDLE` if the user row already exists. Re-runs on every boot.
3. `SshHost` — instantiates `BbsSshServer` and starts listening.

Order matters — schema must exist before bootstrap, and bootstrap must complete before the first login can land.

### SSH transport — `Night.Ms.SshTransport`

Built on `Microsoft.DevTunnels.Ssh` (NuGet). Two notable extensions live here:

- `Crypto/Ed25519.cs` — adds ed25519 user-auth (DevTunnels ships RSA/ECDSA only). Built on `BouncyCastle.Cryptography`.
- `Crypto/Curve25519KeyExchange.cs` — implemented but **disabled** at `BbsSshServer.cs:50`. DevTunnels' `KeyExchangeService.ComputeExchangeHash` wraps `Q_C`/`Q_S` as bigints, which breaks RFC 8731 for X25519 keys with the high bit set. Clients fall back to `ecdh-sha2-nistp256` cleanly. Re-enabling needs an upstream patch — don't toggle it back on without one.

`BbsSshServer` exposes a `SessionStarted` event raised after `shell` is requested on a `session` channel. Other channel types and port forwarding are rejected (`OnChannelOpening`). Public-key auth flows through `_options.AuthLookup` (a delegate — `AuthLookupService.LookupAsync` provides the implementation).

The transport project deliberately knows nothing about the BBS, the database, or the TUI — it only hands you an authenticated `BbsSession` (channel stream + claims + `PtyInfo`) and an `AuthDecision` (`Known` / `Unknown` / `Banned`).

### TUI driver — `Tui/Drivers/`

Terminal.Gui v2 ships built-in drivers for tty/Win32 consoles. To render to an SSH channel stream we plug in a custom `IComponentFactory<char>` (`SshChannelComponentFactory`). Two gotchas:

- `ApplicationImpl(IComponentFactory, ITimeProvider)` is **internal** in Terminal.Gui v2 — the only public path (`Application.Create`) hardcodes the built-in driver names. `BbsSessionRunner.ApplicationImplCtor` reflects to call the internal constructor. If TG internals shift, that reflection is where you'll see it break first.
- `SshChannelComponentFactory()` (parameterless, required by `DriverRegistry`) reads from `SshChannelDriverContext.CurrentOrThrow`, an `AsyncLocal`. The session runner **must** `Push` the context before calling `app.Init()`.

PTY resize (`window-change`) updates `session.Pty` in `BbsSshServer.HandleChannelRequest`; the driver's `GetSize` delegate reads the latest value each tick.

### Per-session flow — `Tui/BbsSessionRunner.cs`

Each SSH session runs `Task.Run` → push driver context → init Terminal.Gui → either `RegisterScreen` (unknown key, TOFU) or `LobbyScreen`. The lobby returns a `LobbyNavigation` enum that drives a loop through `ChatScreen` / forum loop / `ProfileEditScreen` / `NewsScreen` / `AdminScreen`. Each navigation creates a fresh DI scope to avoid sharing `AppDbContext` instances across screens.

### Data layer

EF Core 10 + `EFCore.NamingConventions` (snake_case) against Postgres. The `citext` extension is enabled in `OnModelCreating` and used for `User.Handle` + `Channel.Name`, so handle/channel comparisons are case-insensitive in the database. Sysop actions write to `audit_log` (jsonb `Details` column).

`DatabaseInitializer.SeedAsync` ensures `#lobby` and the `General` forum exist. Tests use `PostgresFixture` (Testcontainers + `postgres:17-alpine`) — one container per test class, fresh database per test via `CreateFreshDatabaseAsync`.

### Realtime — `Realtime/`

`IRealtimeBus` + `RedisRealtimeBus` wrap Redis pub/sub for chat fan-out. `ChatService` handles channel discovery and DM resolution: DM channels use a deterministic name `dm-{lo}-{hi}` (alphabetical) so the same pair always lands on the same channel regardless of who initiated. Public channels are auto-created on first join (BBS-style).

### External-data providers — `Providers/`

`INewsProvider` (Hacker News) and `IWeatherProvider` (Open-Meteo) are pluggable behind interfaces. Implementations are no-key public APIs; both ship with in-process caches (per-instance, no Redis). Swap them by re-binding the interface in `Program.cs`.

## Project-specific conventions worth knowing

- **Don't add an Aspire endpoint for the SSH port.** `WithEndpoint(scheme: "tcp", port: …)` makes Aspire's DCP launcher bind the same port for proxying, which collides with SshServer's own bind (loopback bind wins, connections land on DCP and hang during banner exchange). The current setup passes `BBS_SSH_PORT` via env and lets SshServer bind directly — `SshHost.ResolveListenerPort` prefers that env var, falls back to parsing Aspire's auto-injected service URL, then `2222`. The trade-off is the SSH service has no clickable endpoint on the Aspire dashboard.
- `MouseClick` is wired but not exercised in interactive testing.
- DM channels and `/join #private` are designed-for-but-deferred — only the default `#lobby` is exposed today.
