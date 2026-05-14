# ssh.night.ms

A modern .NET BBS over SSH, with a companion web surface. Public-key (or SSO)
gated; the TUI hosts public chat, a threaded messageboard, news, weather, an
ANSI art gallery, and a sysop console. Designed to feel like the 80s/90s era —
without forcing clients to install a CP437 font.

One process, two listeners: an SSH server for the TUI and a Kestrel HTTP
server for the web (profile pages, avatars, SSO, and an in-browser SSH
client). Both share state via Postgres + Redis.

## Stack

- .NET 10 SDK (pinned in `global.json`)
- `Microsoft.DevTunnels.Ssh` for the SSH protocol, extended in
  `Night.Ms.SshTransport` with `ed25519` user-auth (BouncyCastle) and a gated
  `curve25519-sha256` KEX
- Terminal.Gui v2 driven over the SSH channel via a custom
  `IComponentFactory<char>` (`SshChannelComponentFactory`)
- ASP.NET Core for the web surface — cookie auth + Google / Microsoft OIDC,
  plus a WebSocket transport that runs the same `BbsSessionRunner` in the
  browser at `/terminal`
- EF Core 10 + `EFCore.NamingConventions` against Postgres (snake_case + citext)
- `StackExchange.Redis` pub/sub for chat fan-out
- `Night.Ms.Imaging` — shared half-block image renderer powering profile
  avatars in the TUI and the ANSI art pipeline

## Running locally

Requires Docker Desktop running (for the Postgres + Redis containers).

```sh
./run.ps1
```

That spins up `nightms-pg` + `nightms-redis`, builds, and starts
`Night.Ms.SshServer` on:

- SSH — `:2222` → `ssh -p 2222 <handle>@localhost`
- HTTP — `:5080` → `http://localhost:5080/` (login, profile, `/terminal`)

First connection from an unknown public key lands on the TOFU `RegisterScreen`
— pick a handle, hit Submit, and the key is bound to the new account. The
handle in `NIGHTMS_BOOTSTRAP_SYSOP_HANDLE` (default `nick`) is auto-promoted to
sysop on registration (or at startup if it already exists).

`run.ps1 -Reset` drops and recreates the `bbs` database; `-Stop` tears the dev
containers down. See [`SETUP.md`](SETUP.md) for the full env-var reference,
SSO setup walkthrough, and production-deploy notes.

## Sysop console

Sysops see a Sysop button + `S` shortcut on the lobby. The console is
command-driven:

```
ban <handle>      | unban <handle>
sysop <handle>    | unsysop <handle>
refresh           | help
```

All actions write to the `audit_log` table.

## Production deployment

A docker-compose stack lives at [`deploy/compose.yml`](deploy/compose.yml) —
one app container + Postgres + Redis on a private network, designed for a
single VPS with Cloudflare terminating TLS. Copy `deploy/.env.example` to
`deploy/.env`, fill in secrets, then:

```sh
docker compose -f deploy/compose.yml --env-file deploy/.env up -d --build
```

The container exposes `2222/tcp` (SSH) and `5080/tcp` (HTTP); compose maps
them to host `:22` and `:80`. Move the VPS's own `sshd` off `:22` first, or
the docker-proxy bind will collide. See `SETUP.md` for the full deploy
checklist.

## Adding new ANSI art

`Night.Ms.Tools.AnsiConvert` converts a PNG/JPEG to a half-block `.ans` file
that the lobby banner and gallery can render:

```sh
dotnet run --project src/Night.Ms.Tools.AnsiConvert -- <input.png> \
  [--width 80] [--depth truecolor|256|16] [--dither none|floyd] [--out path]
```

Convert offline, commit the `.ans`, drop it into `NIGHTMS_ART_DIR` (gallery)
or point `NIGHTMS_LOGIN_ART_PATH` at it (login banner). The server never
converts raster images at runtime.

## Known limitations

- `curve25519-sha256` KEX is implemented but disabled — DevTunnels'
  `KeyExchangeService.ComputeExchangeHash` wraps `Q_C`/`Q_S` as bigints, which
  breaks RFC 8731 for X25519 keys with the high bit set. Clients fall back to
  `ecdh-sha2-nistp256` cleanly. Re-enabling needs an upstream patch.
- `MouseClick` is wired but not exercised in interactive testing.
- DM channels and `/join #private` are designed-for-but-deferred — only the
  default `#lobby` channel is exposed today.
- File area and door games — planned, not yet shipped.
