# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`ssh.night.ms` is a Go BBS, originally ported from the .NET `Night.Ms.SshServer` (the predecessor lives in this repo's pre-cutover git history). The Go and .NET stacks target the **same Postgres schema** — `internal/data/initializer.go` detects a `.NET`-bootstrapped schema and force-marks golang-migrate at version 1 to adopt it in place, which is how the cutover preserved the prod data volume. When changing migrations, keep this cross-stack compatibility in mind in case rollback is needed (`deploy/CUTOVER.md` is the runbook).

The single `nightms` binary serves both:
- An SSH listener (charmbracelet/wish + bubbletea) that drops authenticated users into a TUI lobby.
- An HTTP listener (chi) for landing/login/profile pages plus a WebSocket bridge (`/ws/bbs`) that runs the **same** bubbletea root model in-browser.

Both surfaces share auth (`internal/auth`), session services, providers, and the Postgres pool.

## Commands

Dev loop (Windows / PowerShell — the primary supported workflow):
```powershell
.\run.ps1                          # boots Postgres + Redis in Docker, builds, runs
.\run.ps1 -SysopHandle alice -Reset # wipe DB + reseed; user `alice` auto-promoted to sysop on boot
.\run.ps1 -Stop                    # tear down containers
.\run.ps1 -NoBuild                 # skip `go build`, use bin/nightms.exe as-is
```
Defaults: Postgres `:55432`, Redis `:56379`, SSH `:2222`, HTTP `:5080`.

Cross-platform equivalents:
```sh
go build -o bin/nightms ./cmd/nightms
go run ./cmd/nightms
```

Tests (no special harness — plain `go test`):
```sh
go test ./...                                       # full suite
go test ./internal/tui/components/...               # one package
go test -run TestCarousel ./internal/tui/components # single test
```

Code generation — sqlc reads `internal/data/queries/*.sql` and writes `internal/data/gen/`. After editing SQL or migrations, regenerate:
```sh
sqlc generate
```

Helper binaries under `cmd/`:
- `seeduser -handle X -password Y [-sysop]` — insert a dev user. `run.ps1` shows the exact invocation on startup.
- `loadtest seed|run|clean` — synthetic SSH load harness. Phase-7 gate is "200 sessions × 10 min × 0 failures" (see `deploy/CUTOVER.md`).
- `smoketest -host h:p -user X -password Y -expect "..."` — one-shot SSH client that asserts a sentinel string in the rendered output. Useful for non-interactive screen checks.
- `wsprobe` — exercises the `/ws/bbs` WebSocket bridge.
- `ansiconvert` — tooling for the `data/art/*.ans` files.

## Architecture

`cmd/nightms/main.go` is the composition root — there is no DI container. `main()` reads as a table of contents; each numbered phase is its own function. The startup order is load-bearing and matches the .NET app's:

1. `mustMigrate` — `data.RunMigrations` synchronously
2. `mustOpenPool` — `*pgxpool.Pool` + `gen.Queries`
3. `mustSeed` — `data.SeedDefaults` (creates `#lobby` etc.)
4. `mustBootstrapSysop` — `auth.BootstrapSysop`
5. `mustOpenRedis` — Redis client
6. `buildSessionDeps` — realtime services, providers (via `buildProviders`), art (via `buildArt`), policy. Returns the `session.Deps` shared by both surfaces plus the Hold'em registry.
7. `transport.NewServer` — SSH
8. `web.NewServer` — HTTP
9. `runListeners` — spawn the two listeners + wait for ctx cancel or error

Don't reorder without checking that downstream invariants still hold (sysop bootstrap needs `#lobby`; transport needs every provider).

### Layered packages

- `internal/config` — env-var binding (`NIGHTMS_*` and `BBS_*`). The same env vars work for both Go and .NET stacks, by design. Notable Go-only knobs: `NIGHTMS_LOG_LEVEL` (`debug|info|warn|error`, default `info`) and `NIGHTMS_DEBUG_ADDR` (optional `host:port` to mount `/debug/pprof` + `/debug/vars` — bind to localhost only).
- `internal/data` — migrations (`migrations/*.sql` embedded via `go:embed`), seed, and the sqlc-generated `gen` package. Use `Queries` for typed access; reach for `pgxpool.Pool` directly only when sqlc can't express the query.
- `internal/auth` — argon2id hashing, lookup pipeline, Redis-backed rate limiter, OAuth (Google + Microsoft), sysop bootstrap. `Decision` is a closed union (`Known | SignupRequired | Banned | RateLimited | Refused`) — consumers type-switch on it rather than reading a Kind field.
- `internal/realtime` — Redis pub/sub fabric. One goroutine per topic per subscriber; lifetimes are bound to the SSH session context. Named services: `ChatService`, `PresenceService`, `ProfileService`, `WallDispatcher`, `ForumService`, `LocationService`, `LeaderboardService`. The `WallDispatcher` runs as a background goroutine started in `buildSessionDeps`.
- `internal/transport` — the SSH server. `transport.Deps` is just `{Session session.Deps; Lookup *auth.Lookup}` — the per-session bag lives in `session.Deps`, transport adds only the auth lookup needed by the SSH handshake callbacks.
- `internal/web` — the HTTP server. Templates + static assets are `go:embed`-ed so the binary stays drop-anywhere. `wsbridge.go` adapts a websocket to `io.ReadWriter` so the same `tui.Root` model runs in the browser. `web.Deps` embeds `session.CoreDeps` and `session.RealtimeDeps` so handlers stay on flat field names (`h.deps.Pool`, `h.deps.Queries`). Construct with `web.NewDeps`. The WebSocket upgrade has an explicit Origin allowlist (PublicHost + localhost-on-HTTP-port).
- `internal/tui` — the bubbletea program.
  - `tui.Root` (`app.go`) is the per-session root model. It owns the wall-banner overlay, global Ctrl+C, and routes `nav.NavigateMsg` to the right screen via `route()`. When adding a screen, plumb a new `nav.Dest*` constant and a case in `route()`.
  - `tui/session` — `Deps` is the grouped process-singleton bag (`Core`, `Realtime`, `Providers`, `Art`, `Games`, `Policy` sub-structs). `Session` embeds those + a mutable `State` (Identity, Width/Height, PrimaryLocation, DisplayPrefs) so screens access fields flat (`sess.Pool`, `sess.Chat`, `sess.News`). `session.New` is the one factory used by SSH (transport) and web (wsbridge) entry points. Helpers: `sess.Ctx()` / `sess.CtxWithTimeout(d)` — prefer these over `context.Background()` in screen Cmds so goroutines cancel on disconnect.
  - `tui/screens` — one screen per logical area; large ones split into siblings: `chat.go` + `chat_{images,typing,presence,commands}.go`; `profile.go` + `profile_{password,keys,settings,locations,finger}.go`. Other screens (lobby, boards, news, browser, weather, gallery, finance, map, doors, slots, videopoker, blackjack, holdem, holdem_mp, alerts, register, sysop, leaderboards, timezones) are single-file. Unknown destinations and a non-sysop hitting `DestSysop` fall back to the lobby.
  - `tui/components` — reusable widgets (carousel, modal, sparkline, card art, slots cabinet, channel list, …).
  - `tui/art` — `.ans` loader + SGR parser. The lobby renders carousel cards from `data/art/lobby-icons/`; boards use `data/art/board-icons/`; gallery serves `data/art/gallery/`. All directories are filesystem-backed, not embedded, so operators can drop new art without a rebuild.
- `internal/providers` — outbound integrations: `news` (HackerNews), `weather` (OpenMeteo + NWS alerts), `finance` (Yahoo stocks + CoinGecko crypto + Frankfurter FX, plus Yahoo RSS news), `maptile`, `search` (DuckDuckGo), `bookmarks` (DB-backed), `geocoding` (OpenMeteo). All HTTP-backed providers share one `*http.Transport` (connection-pooled, HTTP/2) built in `buildProviders`. Each asset class in finance has its own `Cache` so a Yahoo outage doesn't poison CoinGecko results.
- `internal/providers/ttlcache` — generic `Cache[K,V]` with TTL + `singleflight` coalescing of concurrent fetches + optional `StaleOnError`. Used by news, weather/NWS, finance (3 sub-caches), maptile (TTL=0 = forever), and the chat/browser per-URL image render caches.
- `internal/doors` — game logic (`blackjack`, `cards`, `holdem`, `slots`, `videopoker`) plus `WalletService` (shared process-wide via `session.Games.Wallet`). `holdem/multiplayer` keeps live tables in a `Registry` that persists to Postgres on shutdown and `Restore`s on boot.
- `internal/imaging` — image fetch + half-block / quarter-block rendering. `imaging.Fetcher` is the raw HTTP+decode primitive; `imaging/asyncfetch.Pool` adds bounded concurrency + per-fetch timeout and is shared across all screens that paint inline images (chat, browser) via `session.Core.Images`.
- `internal/reader` — readability extraction for the browser's reader-mode view.
- `internal/browser` — URL parsing, history, and cache used by the browser screen.

### Cross-stack rules to remember

- Schema changes must keep the .NET stack readable (or be coordinated with a .NET-side migration). The Go side uses `schema_migrations` (golang-migrate); the .NET side uses `__EFMigrationsHistory`. Both tables coexist.
- Connection-string format on the Go side is the URL form (`postgres://user:pw@host:port/db?sslmode=...`), **not** the libpq `Host=...;` form used by the .NET stack's env var.
- `cookies`, host keys, profile pictures, and the local art gallery live under `/data` (or `data/` in dev). Re-using the .NET stack's volume preserves clients' `known_hosts` entries after cutover.

## Deployment

`Dockerfile` produces a static scratch image (~5 MB + ca-certs). `deploy/compose.yml` is the prod stack (app + Postgres + Redis on a private network). Host `:22 → :2222` and `:80 → :5080`; TLS terminates at Cloudflare (Flexible mode), so `NIGHTMS_WEB_SECURE_COOKIES=0` behind that proxy. The compose project name is `nightms`, matching the volume names inherited from the .NET stack so cutover was a drop-in replacement (see `deploy/CUTOVER.md`).

`NIGHTMS_COOKIE_SECRET` (hex, ≥ 32 bytes) is optional; when unset the app generates a key on first boot and persists it to `$NIGHTMS_HOST_KEY_DIR/cookie-secret` (mode 0600) so it survives restarts. With neither the env var nor a writable host-key dir, the fallback is an ephemeral key — fine for unit tests, breaks sessions on restart.
