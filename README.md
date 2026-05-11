# ssh.night.ms

A modern .NET BBS over SSH. Public-key gated; the TUI hosts public chat and a
threaded messageboard. Designed to feel like the 80s/90s era — without forcing
clients to install a CP437 font.

## Stack

- .NET 10 + Aspire (AppHost orchestrates Postgres + Redis + the SshServer)
- `Microsoft.DevTunnels.Ssh` (NuGet) for the SSH protocol, extended in
  `Night.Ms.SshTransport` with an `ed25519` user-auth algorithm built on
  `BouncyCastle.Cryptography`
- Terminal.Gui v2 driven over the SSH channel via a custom
  `IComponentFactory<char>` (`SshChannelComponentFactory`)
- EF Core 10 + `EFCore.NamingConventions` against Postgres (snake_case + citext)
- `StackExchange.Redis` pub/sub for chat fan-out

## Running locally with Aspire

Requires Docker Desktop running.

```sh
dotnet run --project src/Night.Ms.AppHost
```

Then `ssh nick@localhost -p 2222` from another terminal.

## Running standalone (no Aspire)

Useful for quick iteration when you already have Postgres + Redis running:

```sh
ConnectionStrings__bbs="Host=127.0.0.1;Port=5432;Database=bbs;Username=postgres;Password=postgres" \
ConnectionStrings__redis="127.0.0.1:6379,abortConnect=false" \
NIGHTMS_HOST_KEY_DIR="$PWD/data/host-keys" \
NIGHTMS_BOOTSTRAP_SYSOP_HANDLE=nick \
  dotnet run --project src/Night.Ms.SshServer
```

The first SSH connection from any unknown public key lands on a `RegisterScreen`
(TOFU). Pick a handle, hit Submit, and that key is bound to the new account.
The handle in `NIGHTMS_BOOTSTRAP_SYSOP_HANDLE` is auto-promoted to sysop on
registration (or at startup if it already exists).

## Sysop console

Sysops see a Sysop button + `S` shortcut on the lobby. The console is
command-driven:

```
ban <handle>      | unban <handle>
sysop <handle>    | unsysop <handle>
refresh           | help
```

All actions write to the `audit_log` table.

## Container build

```sh
docker build -f src/Night.Ms.SshServer/Dockerfile -t ssh.night.ms:dev .
docker run -d --rm \
  -p 2222:2222 \
  -v $(pwd)/data/host-keys:/data/host-keys \
  -e ConnectionStrings__bbs="Host=postgres;Port=5432;Database=bbs;Username=postgres;Password=postgres" \
  -e ConnectionStrings__redis="redis:6379,abortConnect=false" \
  -e NIGHTMS_BOOTSTRAP_SYSOP_HANDLE=youroperator \
  --name ssh.night.ms \
  ssh.night.ms:dev
```

Front the container's `:2222` with iptables/nftables to expose on `:22`.

## Known limitations (carryovers)

- `curve25519-sha256` KEX is implemented but disabled — DevTunnels'
  `KeyExchangeService.ComputeExchangeHash` wraps `Q_C`/`Q_S` as bigints, which
  breaks RFC 8731 for X25519 keys with the high bit set. Clients fall back to
  `ecdh-sha2-nistp256` cleanly. Re-enabling needs an upstream patch.
- Mouse-click handling is wired but not exercised in interactive testing.
- `window-change` PTY resize isn't wired — the initial `pty-req` size is the
  fixed render size for the session.
- DM channels and `/join #private` aren't implemented; one default `#lobby`
  channel only.
- News, weather, file area, profiles/finger, ANSI gallery, door games — all
  designed-for-but-deferred per the original plan.
