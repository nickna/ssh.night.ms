# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`ssh.night.ms` is a Go BBS. The single `nightms` binary serves both:
- An SSH listener (charmbracelet/wish + bubbletea) that drops authenticated users into a TUI lobby.
- An HTTP listener (chi) for landing/login/profile pages plus a WebSocket bridge (`/ws/bbs`) that runs the **same** bubbletea root model in-browser.

Both surfaces share auth (`internal/auth`), session services, providers, and the Postgres pool.

## Commands

Dev loop — `run.ps1` is the canonical entry point and runs on both Windows (PowerShell 5.1+) and Linux/macOS (PowerShell 7+). On Linux invoke as `pwsh ./run.ps1` (or `./run.ps1` if executable + pwsh shebang resolves).

```powershell
./run.ps1                          # boots Postgres + Redis in Docker, builds nightms natively, runs
./run.ps1 -Docker                  # builds + runs the prod Docker image instead (matches deployed env 1:1)
./run.ps1 -SysopHandle alice -Reset # wipe DB + reseed; user `alice` auto-promoted to sysop on boot
./run.ps1 -Stop                    # tear down containers
./run.ps1 -NoBuild                 # skip the build step (native: reuse bin/nightms[.exe]; -Docker: reuse last image)
```
Defaults: Postgres `:55432`, Redis `:56379`, SSH `:2222`, HTTP `:5080`.

**Carbonyl rich-mode browser availability by platform:**
- Linux native (`./run.ps1`): works. Script auto-extracts the LFS bundle from `bundle/` to `bin/carbonyl/` on first run and sets `NIGHTMS_CARBONYL_BIN_PATH`.
- Windows native (`./run.ps1`): hidden. The bundled Chromium is linux-x86_64 and the bridge syscalls (setsid/setctty/TIOCSWINSZ) are Linux-only. Use `-Docker`.
- Any platform with `-Docker`: works. Builds the prod image which extracts the bundle to `/opt/carbonyl/` inside the container.

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
- `loadtest seed|run|clean` — synthetic SSH load harness.
- `smoketest -host h:p -user X -password Y -expect "..."` — one-shot SSH client that asserts a sentinel string in the rendered output. Useful for non-interactive screen checks.
- `wsprobe` — exercises the `/ws/bbs` WebSocket bridge.
- `ansiconvert` — tooling for the `data/art/*.ans` files.

## Architecture

`cmd/nightms/main.go` is the composition root — there is no DI container. `main()` reads as a table of contents; each numbered phase is its own function. The startup order is load-bearing:

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

- `internal/config` — env-var binding (`NIGHTMS_*` and `BBS_*`). Notable knobs: `NIGHTMS_LOG_LEVEL` (`debug|info|warn|error`, default `info`) and `NIGHTMS_DEBUG_ADDR` (optional `host:port` to mount `/debug/pprof` + `/debug/vars` — bind to localhost only).
- `internal/data` — migrations (`migrations/*.sql` embedded via `go:embed`), seed, and the sqlc-generated `gen` package. Use `Queries` for typed access; reach for `pgxpool.Pool` directly only when sqlc can't express the query.
- `internal/auth` — argon2id hashing, lookup pipeline, Redis-backed rate limiter, OAuth (Google + Microsoft), sysop bootstrap. `Decision` is a closed union (`Known | SignupRequired | Banned | RateLimited | Refused`) — consumers type-switch on it rather than reading a Kind field. The rate limiter applies exponential backoff per-IP **and** per-handle (each lockout doubles the duration up to `NIGHTMS_LOCKOUT_BACKOFF_MAX`); after `NIGHTMS_PERSISTENT_BAN_THRESHOLD` IP lockouts in the sliding window, the IP is auto-promoted to a row in `security_ip_bans` via the `BanCache` (in-process map refreshed every 30s, push-invalidated over the Redis `security:ban-invalidate` channel).
- `internal/security/netlimit` — connection-level DoS controls applied to the SSH listener by wrapping `net.Listener`: per-IP concurrent connection cap, per-IP new-connection token bucket (`golang.org/x/time/rate`), and a process-wide cap on in-flight unauthenticated handshakes (the in-process MaxStartups). All per-IP keys collapse IPv6 to /64 so a /64-holding attacker can't trivially evade the limiter. The `DeadlineConn` wrapper implements `LoginGraceTime` — the handshake deadline is cleared by the auth callback on a successful Known / SignupRequired decision.
- `internal/security/audit` — structured security-event recorder. Writes synchronously to slog JSON (always; never lost) and asynchronously to the `security_events` Postgres table via a buffered channel + background goroutine (best-effort; drops on overflow with the `audit_events_dropped_total` expvar counter incrementing). Events are concrete structs (`AuthSuccess`, `AuthFailure`, `LockoutHandle`, `LockoutIP`, `PersistentBanAuto`, `PersistentBanManual`, `PersistentBanRevoke`, `ConnRejectedOverlimit`, `HandshakeFailed`) so each event's `details` jsonb payload is well-shaped. The recorder uses `context.Background` internally so a fast-disconnect attacker can't cancel their own audit row.
- `internal/realtime` — Redis pub/sub fabric. One goroutine per topic per subscriber; lifetimes are bound to the SSH session context. Named services: `ChatService`, `PresenceService`, `ProfileService`, `WallDispatcher`, `ForumService`, `LocationService`, `LeaderboardService`. The `WallDispatcher` runs as a background goroutine started in `buildSessionDeps`.
- `internal/transport` — the SSH server. `transport.Deps` is just `{Session session.Deps; Lookup *auth.Lookup}` — the per-session bag lives in `session.Deps`, transport adds only the auth lookup needed by the SSH handshake callbacks.
- `internal/web` — the HTTP server. Templates + static assets are `go:embed`-ed so the binary stays drop-anywhere. `wsbridge.go` adapts a websocket to `io.ReadWriter` so the same `tui.Root` model runs in the browser. `web.Deps` embeds `session.CoreDeps` and `session.RealtimeDeps` so handlers stay on flat field names (`h.deps.Pool`, `h.deps.Queries`). Construct with `web.NewDeps`. The WebSocket upgrade has an explicit Origin allowlist (PublicHost + localhost-on-HTTP-port).
- `internal/tui` — the bubbletea program.
  - `tui.Root` (`app.go`) is the per-session root model. It owns the wall-banner overlay, global Ctrl+C, and routes `nav.NavigateMsg` to the right screen via `route()`. When adding a screen, plumb a new `nav.Dest*` constant and a case in `route()`.
  - `tui/session` — `Deps` is the grouped process-singleton bag (`Core`, `Realtime`, `Providers`, `Art`, `Games`, `Policy`, `Security` sub-structs). `Session` embeds those + a mutable `State` (Identity, Width/Height, PrimaryLocation, DisplayPrefs) so screens access fields flat (`sess.Pool`, `sess.Chat`, `sess.News`, `sess.Bans`). `session.New` is the one factory used by SSH (transport) and web (wsbridge) entry points. Helpers: `sess.Ctx()` / `sess.CtxWithTimeout(d)` — prefer these over `context.Background()` in screen Cmds so goroutines cancel on disconnect.
  - `tui/screens` — one screen per logical area; large ones split into siblings: `chat.go` + `chat_{images,typing,presence,commands}.go`; `profile.go` + `profile_{password,keys,settings,locations,finger}.go`; `sysop.go` + `sysop_{events,events_filter,bans}.go` (three-tab moderation console: unified events feed, users, IP bans). Other screens (lobby, boards, news, web, weather, gallery, finance, map, doors, slots, videopoker, blackjack, holdem, holdem_mp, alerts, register, leaderboards, timezones) are single-file. Unknown destinations and a non-sysop hitting `DestSysop` fall back to the lobby.
  - `tui/components` — reusable widgets (carousel, modal, sparkline, card art, slots cabinet, channel list, …).
  - `tui/art` — `.ans` loader + SGR parser. The lobby renders carousel cards from `data/art/lobby-icons/`; boards use `data/art/board-icons/`; gallery serves `data/art/gallery/`. All directories are filesystem-backed, not embedded, so operators can drop new art without a rebuild.
- `internal/providers` — outbound integrations: `news` (HackerNews), `weather` (OpenMeteo + NWS alerts), `finance` (Yahoo stocks + CoinGecko crypto + Frankfurter FX, plus Yahoo RSS news), `maptile`, `geocoding` (OpenMeteo). All HTTP-backed providers share one `*http.Transport` (connection-pooled, HTTP/2) built in `buildProviders`. Each asset class in finance has its own `Cache` so a Yahoo outage doesn't poison CoinGecko results.
- `internal/providers/ttlcache` — generic `Cache[K,V]` with TTL + `singleflight` coalescing of concurrent fetches + optional `StaleOnError`. Used by news, weather/NWS, finance (3 sub-caches), maptile (TTL=0 = forever), and the chat per-URL image render cache.
- `internal/doors` — game logic (`blackjack`, `cards`, `holdem`, `slots`, `videopoker`) plus `WalletService` (shared process-wide via `session.Games.Wallet`). `holdem/multiplayer` keeps live tables in a `Registry` that persists to Postgres on shutdown and `Restore`s on boot.
- `internal/imaging` — image fetch + half-block / quarter-block rendering. `imaging.Fetcher` is the raw HTTP+decode primitive; `imaging/asyncfetch.Pool` adds bounded concurrency + per-fetch timeout and is shared across all screens that paint inline images (chat) via `session.Core.Images`.
- `internal/reader` — readability extraction. Used by the News screen to inline an HN story's article text when the user opens one.

### Operational notes

- Connection string is the libpq URL form (`postgres://user:pw@host:port/db?sslmode=...`). Migrations run via golang-migrate against the `schema_migrations` table.
- `cookies`, host keys, profile pictures, and the local art gallery live under `/data` (or `data/` in dev).
- `internal/data/initializer.go` adopts a pre-existing schema (detected by populated tables but no `schema_migrations` row) by force-marking golang-migrate at version 1. Useful when standing up against a database an earlier build already populated.

## Deployment

`Dockerfile` produces a ~750 MB `debian:bookworm-slim`-based image — the Carbonyl rich-mode bundle alone adds ~400 MB of stripped Chromium .so files. Without rich-mode it would be a ~5 MB scratch image; the size jump is the unavoidable cost of bundling a browser. `deploy/compose.yml` is the prod stack (app + Postgres + Redis on a private network). Host `:22 → :2222` and `:80 → :5080`; TLS terminates at Cloudflare (Flexible mode), so `NIGHTMS_WEB_SECURE_COOKIES=0` behind that proxy. The compose project name is `nightms`.

**Bundle prerequisite for `docker build`:** the Carbonyl runtime ships via Git LFS under `bundle/carbonyl-linux-x86_64.tar.xz` (~103 MB compressed, ~560 MB extracted). Clone with `git lfs install` then `git lfs pull` before building — CI must do the same. To regenerate after a Carbonyl source rebuild, run `scripts/package-carbonyl.sh [path-to-chromium/src/out/Release]`. Bundle layout + provenance: `bundle/README.md`.

`NIGHTMS_COOKIE_SECRET` (hex, ≥ 32 bytes) is optional; when unset the app generates a key on first boot and persists it to `$NIGHTMS_HOST_KEY_DIR/cookie-secret` (mode 0600) so it survives restarts. With neither the env var nor a writable host-key dir, the fallback is an ephemeral key — fine for unit tests, breaks sessions on restart.

`NIGHTMS_WEB_BASE_URL` is the web origin — used for CSRF trusted origins, the `/ws/bbs` Origin allowlist, and OAuth callback defaults. `NIGHTMS_SSH_HOST` is the host advertised in the "ssh -p 2222 you@&lt;host&gt;" snippets rendered on landing/login/profile pages. Set this when SSH and HTTP terminate on different hostnames — e.g. SSH direct at `ssh.night.ms` while HTTP runs behind Cloudflare at `k.night.ms`. Falls back to the bare host from `NIGHTMS_WEB_BASE_URL` when unset, which is correct for single-host deployments. Both accept either a bare host or a full URL — the scheme is stripped either way.

`NIGHTMS_SSH_PORT` is the externally-reachable SSH port shown alongside `NIGHTMS_SSH_HOST` in those same snippets. Defaults to the bind port (`BBS_SSH_PORT`, i.e. `2222` for `./run.ps1`) so dev keeps showing `ssh -p 2222 …`. Set it explicitly when host port-forwarding remaps the bind — e.g. prod compose binds container `:2222` to host `:22`, so `NIGHTMS_SSH_PORT=22` makes the page render `ssh you@host`. The `-p` flag is omitted entirely when the value is `22` (the SSH default).

## Security

The SSH listener is exposed direct to the public internet (no L4 proxy in front, unlike HTTP which terminates at Cloudflare). Hardening lives in three layers; each is independently overridable via env vars:

**Protocol-layer (`internal/transport/server.go`):** wish/gliderlabs callbacks plumbed onto the returned `*ssh.Server`.
- `NIGHTMS_SSH_MAX_AUTH_TRIES` — `gossh.ServerConfig.MaxAuthTries`. Default 3.
- `NIGHTMS_SSH_LOGIN_GRACE_SECONDS` — handshake deadline (LoginGraceTime). Default 30. Cleared by the auth callback on a successful decision.

**Network-layer (`internal/security/netlimit`):** `Listener` wraps `net.Listener.Accept()`; `Tracker` holds per-IP state. IPv6 collapsed to /64 for all per-IP keys.
- `NIGHTMS_SSH_MAX_CONN_PER_IP` — concurrent socket cap, decremented on conn-close. Default 10.
- `NIGHTMS_SSH_CONN_RATE_PER_IP` / `NIGHTMS_SSH_CONN_BURST_PER_IP` — `golang.org/x/time/rate` token bucket. Defaults 5/s burst 20.
- `NIGHTMS_SSH_MAX_UNAUTH_HANDSHAKES` — global cap on in-flight unauthenticated handshakes. Default 100. Enforced in `ConnCallback`; slot is released on auth-success OR conn-close (whichever first) via `DeadlineConn`'s onClose hook.

**Auth-layer (`internal/auth/ratelimit_redis.go` + `persistban.go`):**
- `NIGHTMS_LOCKOUT_HANDLE_THRESHOLD` / `NIGHTMS_LOCKOUT_IP_THRESHOLD` — failures within the sliding window that trip a lockout. Defaults 5 / 20.
- `NIGHTMS_LOCKOUT_WINDOW_SECONDS` — sliding fail-counter TTL. Default 900.
- `NIGHTMS_LOCKOUT_SECONDS` — **base** lock duration. The actual lock applied is `base * (1 << min(lockcount-1, BACKOFF_MAX))`. Default 900 (so 15m → 30m → 1h → 2h → 4h → 8h capped).
- `NIGHTMS_LOCKOUT_BACKOFF_MAX` — exponent cap. Default 5 (→ ×32 multiplier).
- `NIGHTMS_LOCKCOUNT_WINDOW_SECONDS` — sliding TTL on the per-IP / per-handle lockcount counter. Default 86400. Refreshed on every INCR so a patient attacker can't drop out of the window by spacing attempts.
- `NIGHTMS_PERSISTENT_BAN_THRESHOLD` — IP lockcount that auto-promotes to a `security_ip_bans` row. Default 3.
- `NIGHTMS_PERSISTENT_BAN_DURATION_SECONDS` — TTL applied to auto-promoted ban rows. Default 86400. Sysop-issued `ban-ip` commands take an explicit duration.

**Audit (`internal/security/audit`):** structured `Event`s land in `security_events` (Postgres, async + buffered) and slog JSON (synchronous, never lost). Event types: `auth_success`, `auth_failure`, `lockout_handle`, `lockout_ip`, `persistent_ban_auto`, `persistent_ban_manual`, `persistent_ban_revoke`, `conn_rejected_overlimit`, `handshake_failed`, `oauth_linked`, `oauth_unlinked`, `oauth_refreshed`, `oauth_refresh_failed`, `oauth_reauth_required`. `NIGHTMS_AUDIT_BUFFER_SIZE` (default 2048) sizes the in-process buffer; drops increment the `audit_events_dropped_total` expvar.

**Sysop UI (`internal/tui/screens/sysop*.go`):** three tabs cycled with `Tab` (or `1`/`2`/`3` when the input is empty):
- **Events** (`sysop_events.go`, default landing tab) — unified chronological feed merging `audit_log` (sysop actions) and `security_events` rows via a Postgres `UNION ALL`. Keyset-paginated (`ListUnifiedEvents`), with cursor + PgUp/PgDn navigation, auto-load-more as the cursor approaches the bottom, and a detail modal (Enter) showing the full jsonb payload + related events (same handle/ip within ±5 min via `ListUnifiedEventsRelated`). The textinput at the bottom is a live-debounced filter (150ms quiet window) parsed by `parseFilters` (`sysop_events_filter.go`) into chips: `severity:warn handle:alice ip:1.2.3.4 kind:auth_failure source:audit since:1h until:2026-05-23 text:foo`. Filtered queries take the hand-written `data.ListUnifiedEventsFiltered` path (`internal/data/events_filtered.go`); the dim names are a closed allowlist to bound the SQL surface. `/` re-grabs filter focus from list-navigation mode; `Esc` clears the filter when it's non-empty, otherwise falls through to the page-level back-to-lobby.
- **Users** (`sysop.go::renderUsers`) — alphabetical user list with flag column (S/B/K). Commands at the prompt: `ban`, `unban`, `sysop`, `unsysop`, `clear-passwordless`, `wall`.
- **Bans** (`sysop_bans.go`) — active `security_ip_bans` rows. Commands: `ban-ip <ip> [duration] [reason]`, `unban-ip <ip>`. Both go through `auth.BanCache` which push-invalidates over the Redis `security:ban-invalidate` channel so multiple replicas converge in <1s.

## OAuth account linking

Per-user Google + Microsoft account linking that feeds future Gmail / Drive / Outlook / OneDrive integrations. OAuth is **link-only** — there is no "sign in with Google" path. An SSH-authenticated user attaches OAuth identities; tokens are sealed at rest and renewed in the background.

**Two link entry points, one storage layer.**
- Web (`/profile/connections`): auth-code redirect flow via `/auth/{provider}/start` → IdP → `/auth/{provider}/callback`. Suitable for users already in a browser.
- SSH TUI (Profile screen → "connected accounts"): OAuth 2.0 device code (RFC 8628) via `internal/auth/devicecode`. Shows the user a short code + a verification URL; they enter the code in any browser, TUI polls and updates. The WebSocket-bridged web terminal uses the same in-process service — no HTTP surface needed.

**Storage.**
- `identity_credentials` (existing, migration `000001`) — the (provider, subject) UNIQUE index enforces "one OAuth identity → one SSH user" at the SQL level. Both surfaces surface `LinkLookupOtherUser` as "That {provider} account is linked to a different handle."
- `oauth_tokens` (migration `000011`) — 1:1 with `identity_credentials.id` via FK + ON DELETE CASCADE. Columns: `encrypted_access_token bytea`, `encrypted_refresh_token bytea` (nullable), `access_expires_at`, `scopes text[]`, `needs_reauth bool`, `refresh_failure_count int`, `last_refreshed_at`. Partial index `(access_expires_at) WHERE needs_reauth = false AND encrypted_refresh_token IS NOT NULL` drives the refresher's "soon-expiring" scan.

**Encryption (`internal/auth/tokenseal`).** AES-256-GCM with a key derived via HKDF-SHA256 from either `NIGHTMS_OAUTH_TOKEN_SECRET` (hex, ≥32 B) or — when unset — the existing cookie secret. Domain-separated via the info string `"nightms/oauth-token-v1"`. Sealed-blob layout: `[1B version=0x01][12B nonce][ciphertext+tag]`. **Operational consequence:** rotating the cookie secret invalidates every stored token unless `NIGHTMS_OAUTH_TOKEN_SECRET` was set independently — set it in prod for a rotation path.

**Refresher (`internal/auth/oauthrefresh`).** `go refresher.Run(ctx)` in `main.go` alongside `banCache.Run`. Per-tick (default `NIGHTMS_OAUTH_REFRESH_INTERVAL=60s`): `ListExpiringTokens` for rows expiring within `NIGHTMS_OAUTH_REFRESH_LEAD_TIME` (default `10m`), fan out to a 4-worker pool, decrypt refresh token, call `provider.RefreshToken`, re-seal + `UpsertOAuthToken`. Failure classification via `errors.As(&oauth2.RetrieveError)`: `invalid_grant`/`invalid_request` = hard fail (flip `needs_reauth`), 5xx/429/network = soft fail (bump `refresh_failure_count`; flip `needs_reauth` after ≥5 consecutive). The `COALESCE` in `UpsertOAuthToken` handles Microsoft's rotated refresh tokens AND Google's preserved-on-success behavior in one statement.

**Device-flow service (`internal/auth/devicecode`).** Flow state lives in Redis under `oauth:device:flow:{flow_id}` (TTL = device-code expiry) + `oauth:device:user:{user_id}:{provider}` for the single-in-flight invariant. Begin rate-limited per user to 6 starts per minute via `oauth:device:begin:{user_id}`. The `Poll` state machine maps provider responses to `ResultPending` / `ResultSlowDown` (bump interval +5s) / `ResultApproved` / `ResultDenied` / `ResultExpired` / `ResultDuplicate`. On Approved: `ResolveExistingLink` switches between fresh insert (in a tx with the token row) and re-auth upsert.

**Operator setup (cannot work without these).**
1. **Google**: register a *second* OAuth client of type "TVs and Limited Input devices" in Google Cloud Console — the regular Web-application client rejects device flow with `disabled_client`. Supply credentials as `NIGHTMS_GOOGLE_DEVICE_CLIENT_ID` + `NIGHTMS_GOOGLE_DEVICE_CLIENT_SECRET`. When unset, the TUI's add-Google flow surfaces "linking from terminal is unavailable" and the user is directed to the web `/profile/connections` page.
2. **Microsoft**: in the Azure app registration, enable "Allow public client flows" — the same client ID handles both browser (with secret) and device (without secret) flows.
3. **Google scopes**: the broad scope set (`gmail.readonly`, `drive.readonly`, `documents.readonly` on top of openid/email/profile) requires consent-screen approval once the OAuth project leaves "Testing" mode. Until then, only listed test users can complete the flow.

**Env vars** (all `NIGHTMS_*`):
- `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` — browser auth-code Google client.
- `GOOGLE_DEVICE_CLIENT_ID` / `GOOGLE_DEVICE_CLIENT_SECRET` — separate device-flow Google client (see operator setup).
- `MICROSOFT_CLIENT_ID` / `MICROSOFT_CLIENT_SECRET` — shared between both flows.
- `OAUTH_REDIRECT_BASE` — externally-reachable origin for the auth-code callbacks. Defaults to `http://<WebPublicHost>:<port>` for dev.
- `OAUTH_REFRESH_INTERVAL` (default `60s`) / `OAUTH_REFRESH_LEAD_TIME` (default `10m`) — refresher cadence + how far ahead of expiry to renew.
- `OAUTH_REFRESH_BATCH_SIZE` (default `50`) / `OAUTH_REFRESH_WORKERS` (default `4`) — per-tick row cap + worker pool size. Bump together when a sign-up wave packs more expiries than `BATCH_SIZE` into one `LEAD_TIME` window (each tick processes at most `BATCH_SIZE` rows, so the practical ceiling per window is `BATCH_SIZE × (LEAD_TIME / INTERVAL)`).
- `OAUTH_TOKEN_SECRET` — hex, ≥32 B; optional. When set, sealer key derives from this; otherwise from the cookie secret.

**Auth-code branches removed.** The old `/auth/finish` "pick a handle" signup form and the "Sign in with Google/Microsoft" buttons on the login page are gone. `oauthStart` requires an active session; `oauthCallback` resolves to one of three branches via `auth.ResolveExistingLink` (re-auth on same user, error on other user, fresh-link tx on new). Pre-flight check before deploying this cleanup to a fresh database: `SELECT COUNT(*) FROM users u WHERE u.password_hash IS NULL AND NOT EXISTS (SELECT 1 FROM identity_credentials c WHERE c.user_id = u.id AND c.provider = 'Ssh');` — must be 0 (any matches would be locked out by the cleanup).

**Audit events.** `oauth_linked` / `oauth_unlinked` fire from both link surfaces with `method: "browser" | "device"`. `oauth_refreshed` fires from each successful refresh; `oauth_refresh_failed` (soft) and `oauth_reauth_required` (hard or threshold-crossed) fire from the refresher and surface in the sysop events feed.

## Rich-mode browser (Carbonyl)

Lobby entry: **Web** (`internal/tui/screens/web.go`, `nav.DestWeb`, hotkey `w`) — Carbonyl-backed full Chromium browser. URL prompt → launch → Carbonyl owns the terminal until the user `q`'s out → return to the prompt. The lobby item only renders on SSH sessions (see "SSH-only" below); the screen itself surfaces remaining gates (binary loadable, kill switch on) as inline status messages so the user can see the option, click it, and learn why it won't run.

An earlier reader-mode browser sat next to this entry as "Reader" — Mozilla-Readability over plain HTTP, no JS. It was removed once Carbonyl was stable enough to be the sole browsing surface; news.go still uses `internal/reader` to inline HN article bodies.

**SSH-only.** The WebSocket-bridged web terminal can't host Carbonyl: no /dev/tty to give the child, and xterm.js wouldn't faithfully answer the DCS terminal-capability probes Carbonyl emits at startup. The "Web" lobby item is hidden on the WS path.

**Bundle.** Carbonyl ships as a component-build of patched Chromium M148+ (the user's fork at https://github.com/nickna/carbonyl). The `bundle/carbonyl-linux-x86_64.tar.xz` Git-LFS artifact contains the `carbonyl` binary + ~410 transitive .so + V8 snapshots + ICU data + .pak resources, deterministically packed by `scripts/package-carbonyl.sh`. The Dockerfile extracts it to `/opt/carbonyl/` after sha256 verification.

**Process model.** Per launch (`internal/carbonyl/runner.go::Runner.Launch`):
1. `tokens.Acquire(ip, uid)` reserves a slot under the three concurrency caps.
2. `pty.Open()` allocates a master/slave pair; initial winsize via `TIOCSWINSZ`.
3. `exec.CommandContext` spawns `/opt/carbonyl/carbonyl` with stdio on the slave + `Setsid + Setctty + Ctty: 0` so the child has its own session and a real controlling tty. Stderr is captured separately so Carbonyl's GPU warnings go to slog, not to the SSH terminal where they'd corrupt the rendered frame.
4. `bridgePTY` runs three goroutines: SSH-stdin → master, master → SSH-stdout, screen-owned resize chan → `TIOCSWINSZ(master)`. The browser screen's Update forwards every `tea.WindowSizeMsg` into the resize chan while `richModeActive`.
5. The screen's launch Cmd wraps the whole thing in `prog.ReleaseTerminal()` / `prog.RestoreTerminal()` so the bubbletea read loop pauses and resumes around Carbonyl's takeover. After restore, the screen writes a recovery escape sequence (`\x1b[?1049l\x1b[?25h\x1b[?1000l\x1b[?1006l`) to wipe alt-screen + cursor-hide + mouse-tracking state Carbonyl leaves behind.

**Exit keys.** From inside Carbonyl two ways out:
- `Ctrl+C` — Carbonyl's own normal exit. Carbonyl's input parser (`carbonyl/src/input/parser.rs:80`) maps `0x03` to `Event::Exit` and shuts down cleanly. SSH stays connected; user lands back at the Web screen URL prompt.
- `Ctrl+\` — emergency exit intercepted by our PTY bridge (`internal/carbonyl/ptybridge_linux.go`) BEFORE the byte reaches Carbonyl. The bridge consumes the chord byte and calls `cancelLaunch()` which SIGKILLs the child via `exec.CommandContext`. Use when Carbonyl is unresponsive (a stuck WebGL canvas, a runaway JS loop) and Ctrl+C isn't getting through. The chord is a single byte (ASCII FS, `0x1c`), chosen because browsers never bind it.

**Env vars** (all `NIGHTMS_CARBONYL_*`):
- `NIGHTMS_CARBONYL_BIN_PATH` — absolute path to the binary. Default `/opt/carbonyl/carbonyl`. Missing binary → soft disable (BBS still boots; rich-mode toasts "unavailable").
- `NIGHTMS_CARBONYL_DATA_DIR` — parent dir for per-user `--user-data-dir`. Default `/data/carbonyl`. Lazy-created at mode 0700 per uid; cookies and logins persist across reconnects.
- `NIGHTMS_CARBONYL_ENABLED` — kill switch default. `1` (on) by default — the binary's mere presence on disk is the implicit gate, so an operator who didn't intend to enable rich mode would also not have the bundle. Set to `0` to force-disable independent of bundle presence.
- `NIGHTMS_CARBONYL_MAX_GLOBAL` / `NIGHTMS_CARBONYL_MAX_PER_IP` / `NIGHTMS_CARBONYL_MAX_PER_HANDLE` — concurrency caps. Defaults 2 / 1 / 1. Each Chromium child is 200–400 MB resident; runaway launches OOM the container.

**Runtime settings (sysop UI):** `carbonyl_enabled`, `carbonyl_max_global`, `carbonyl_max_per_ip`, `carbonyl_max_per_handle`. The `settings.Cache.OnChange` hook in `main.go` pushes new caps into `Runner.UpdateLimits` live, no restart needed.

**Security model (v1):** in-process URL allowlist + Chromium hardening flags.
- `internal/carbonyl/urlpolicy.go::ValidateURL` rejects `file://`, `chrome://`, `view-source:`, any private/loopback/link-local IP literal, and `localhost`/`ip6-localhost` by name. Runs before the child is forked.
- Args (`internal/carbonyl/args.go::buildArgs`) always emit `--no-sandbox --disable-dev-shm-usage --user-data-dir=<profile> --disable-extensions --host-resolver-rules="MAP localhost ~NOTFOUND,..."`. The host-resolver flag blocks loopback from inside the running Chromium too, defending against the user typing `http://127.0.0.1/` into Carbonyl's own address bar after launch.
- Known limitation: once running, Carbonyl's address bar can navigate to any public URL. DNS rebinding mid-session is not defended against. Acceptable for invite-only / trusted-BBS use; revisit with an egress-proxy sidecar if abuse appears.

**Package map (`internal/carbonyl/`):**
- `runner.go` — `Runner` singleton + per-launch process supervision.
- `limits.go` — three-axis concurrency token bucket (global/per-IP/per-handle).
- `urlpolicy.go` — `ValidateURL` + private-IP gates.
- `args.go` — Chromium argv builder; pure func, unit-tested.
- `ptybridge.go` — three goroutine bridge between OS PTY master and SSH channel.
- `session_io.go` — `SessionIO` interface keeping `gliderlabs/ssh` out of the package.
- `runner_test.go` — limits/url-policy/args tests.

The SSH-side `carbonyl.SessionIO` implementation lives at `internal/transport/sshsession.go`. The wish-supplied SIGWINCH channel can NOT be drained from this package because wish's middleware already drains it to send `tea.WindowSizeMsg` — instead the screen forwards WindowSizeMsg into its own per-launch chan, which is the chan the SessionIO returns from WindowChanges().
