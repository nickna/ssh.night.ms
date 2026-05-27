# ssh.night.ms
<img width="3341" height="1650" alt="image" src="https://github.com/user-attachments/assets/dab04685-1624-4196-a43a-b6a6b7715cfc" />


A retro BBS written in Go, served over SSH and the web. Connect via:

```sh
ssh -p 2222 <handle>@<host>      # SSH terminal client
# or open the web terminal in a browser
```

Authenticated users land in a TUI lobby with chat, message boards, news
(Hacker News), weather, an ANSI gallery, finance (stocks / crypto / FX),
a world map, doors games (slots, video poker, blackjack, multiplayer
hold'em), and a real Chromium-based browser ("Rich Mode" via Carbonyl).

## Architecture in one paragraph

A single `nightms` binary runs two listeners that share auth, providers,
and the Postgres pool: an SSH server (charmbracelet/wish + bubbletea) and
an HTTP server (chi) whose `/ws/bbs` WebSocket bridges the **same**
bubbletea root model into the browser. Persistent state lives in Postgres
(sqlc-generated queries, golang-migrate migrations embedded via
`go:embed`); realtime fan-out (chat, presence, wall, leaderboards, live
hold'em tables) goes through Redis pub/sub. The composition root in
`cmd/nightms/main.go` reads as a numbered table of contents; deeper
architecture notes live in [`CLAUDE.md`](./CLAUDE.md).

## Quick start

The dev loop is driven by `run.ps1` (works on Windows PowerShell 5.1+ and
on Linux/macOS via PowerShell 7+). It boots Postgres and Redis in Docker,
builds the binary natively, and runs it. Defaults: SSH on `:2222`, HTTP on
`:5080`, Postgres `:55432`, Redis `:56379`.

```sh
# Linux / macOS
pwsh ./run.ps1                          # boot dependencies + build + run
pwsh ./run.ps1 -SysopHandle alice -Reset # wipe DB + seed; alice becomes sysop
pwsh ./run.ps1 -Docker                  # build + run the prod image instead
pwsh ./run.ps1 -Stop                    # tear down containers
```

```powershell
# Windows
./run.ps1
```

On first run, fetch the Carbonyl rich-mode bundle (Git LFS, ~103 MB):

```sh
git lfs install
git lfs pull
```

Without the bundle the BBS still boots fine; the Web lobby entry just
prints "rich mode unavailable".

> **Note on Git LFS bandwidth.** The Carbonyl bundle lives in Git LFS and
> counts against the *forking account's* LFS bandwidth quota (GitHub's free
> tier is 1 GB/month). If you're forking and don't need rich-mode browsing,
> you can leave the bundle out — `git clone --filter=blob:none` or simply
> not running `git lfs pull` skips the download, and the BBS still boots
> with rich mode disabled. If you're publishing your own image, expect the
> bundle to dominate clone time on cold CI runs.

Pure-Go alternative without Docker (you supply Postgres + Redis yourself
and set the `NIGHTMS_*` env vars):

```sh
go build -o bin/nightms ./cmd/nightms
go run ./cmd/nightms
```

## Tests and codegen

```sh
go test ./...                                  # full suite, plain `go test`
go test -run TestCarousel ./internal/tui/components  # one test
sqlc generate                                  # after editing SQL or migrations
```

## Configuration

Everything is env-var driven; see `internal/config/options.go` for the
canonical list. Important knobs:

- `NIGHTMS_DATABASE_URL` — Postgres URL (`postgres://user:pw@host:port/db?sslmode=...`)
- `NIGHTMS_REDIS_ADDR` — Redis address
- `NIGHTMS_SSH_LISTEN_ADDR`, `NIGHTMS_HTTP_LISTEN_ADDR` — listener binds
- `NIGHTMS_PUBLIC_HOST` — used for CSRF + WebSocket origin allowlist
- `NIGHTMS_COOKIE_SECRET` — optional; auto-generated on first boot if a host-key
  directory is writable, ephemeral otherwise
- `NIGHTMS_LOG_LEVEL` — `debug | info | warn | error` (default `info`)
- `NIGHTMS_DEBUG_ADDR` — optional `host:port` to mount `/debug/pprof` and
  `/debug/vars`. Leave unset in production, or bind to localhost only.

OAuth (Google + Microsoft) is enabled by setting the corresponding client
ID / secret pairs.

## Deployment

`deploy/compose.yml` runs the prod stack (app + Postgres + Redis on a
private Docker network). `Dockerfile` produces a ~750 MB
`debian:bookworm-slim`-based image; the Carbonyl Chromium bundle accounts
for ~400 MB of that. CI builds + deploys via `.github/workflows/deploy.yml`
(it uses GitHub Secrets and will only work in the project's own GitHub repo
unless you wire up your own secrets).

## Security

`SECURITY.md` covers how to report a vulnerability. `CLAUDE.md` has a long
"Security" section documenting the three layers of defense (protocol,
network, auth) and the audit pipeline. The SSH listener is exposed direct
to the public internet; HTTPS terminates at a CDN (we use Cloudflare in
Flexible mode).

## License

MIT — see [`LICENSE`](./LICENSE). Bundled third-party software (Carbonyl,
Chromium components, Go dependencies) carries its own attribution; see
[`NOTICE`](./NOTICE).
