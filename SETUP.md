# Setup

Everything you need to run ssh.night.ms locally or stand it up on a server. The codebase is one binary that owns two listeners — an SSH server (BBS) and a Kestrel HTTP server (web auth + in-browser terminal + profile/avatar surfaces). Both run in the same .NET process and share state via Postgres + Redis.

- [Prerequisites](#prerequisites)
- [Quick start (dev)](#quick-start-dev)
- [Configuration reference](#configuration-reference)
- [Setting up Google + Microsoft SSO](#setting-up-google--microsoft-sso)
- [First-run sysop](#first-run-sysop)
- [Database migrations](#database-migrations)
- [Where data lives on disk](#where-data-lives-on-disk)
- [Production deployment notes](#production-deployment-notes)

## Prerequisites

| Tool          | Version                                  | Notes                                                                     |
|---------------|------------------------------------------|---------------------------------------------------------------------------|
| .NET SDK      | 10.x (pinned in `global.json`)           |                                                                            |
| Docker        | Docker Desktop running                    | `run.ps1` pulls `postgres:17-alpine` + `redis:7-alpine`                   |
| PowerShell    | pwsh 7+ (Windows ships it; pwsh on mac/Linux works too) | `run.ps1` is the canonical dev launcher                            |
| `psql` (optional) | Any                                    | For inspecting the dev database — `docker exec nightms-pg psql -U postgres -d bbs` works without it |

## Quick start (dev)

```sh
./run.ps1
```

That:

1. Spins up `nightms-pg` + `nightms-redis` containers if they aren't running.
2. Waits for Postgres to accept connections.
3. Builds the solution.
4. Sets the env vars listed in [Configuration reference](#configuration-reference) and starts `Night.Ms.SshServer`.

Default ports:

- SSH: **`:2222`** — `ssh -p 2222 <handle>@localhost`
- HTTP: **`:5080`** — `http://localhost:5080/`

(Port 5000 is held by Docker Desktop on Windows and AirPlay Receiver on macOS, hence the 5080 default. Override with `-HttpPort` if you want something else.)

`run.ps1` options:

```
-SysopHandle <name>     Handle promoted to sysop on first registration (default 'nick')
-PostgresPort <n>       Host port for the Postgres container (default 55432)
-RedisPort <n>          Host port for the Redis container (default 56379)
-SshPort <n>            BBS SSH port (default 2222)
-HttpPort <n>           Kestrel HTTP port (default 5080)
-Reset                  Drop + recreate the bbs database before starting
-Stop                   Tear down the dev containers and exit
```

The first connection from an unknown SSH key drops you into the TOFU `RegisterScreen`. Pick a handle, hit Submit, and that key is bound to the new account.

## Configuration reference

The server reads from environment variables and from `appsettings.json` / user secrets — a single typed object (`NightMsOptions`) holds the merged result so callers don't repeat the env-or-config fallback themselves. Almost everything has a sensible default; only the connection strings are mandatory.

### Required

| Env var                       | Equivalent appsetting key                | Purpose                                                                                                       |
|-------------------------------|------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| `ConnectionStrings__bbs`      | `ConnectionStrings:bbs`                  | Npgsql connection string for the BBS database. Example: `Host=127.0.0.1;Port=55432;Database=bbs;Username=postgres;Password=postgres` |
| `ConnectionStrings__redis`    | `ConnectionStrings:redis`                | StackExchange.Redis connection string. Example: `127.0.0.1:56379,abortConnect=false`                          |

### Listeners

| Env var          | Default | Purpose                                                                                  |
|------------------|---------|------------------------------------------------------------------------------------------|
| `BBS_SSH_PORT`   | `2222`  | TCP port the BBS SSH listener binds to.                                                  |
| `BBS_HTTP_PORT`  | `5080`  | TCP port Kestrel binds to for the web surface (`/`, `/login`, `/terminal`, `/u/...`).    |

### Identity & sysop

| Env var                            | Default | Purpose                                                                                                    |
|------------------------------------|---------|------------------------------------------------------------------------------------------------------------|
| `NIGHTMS_BOOTSTRAP_SYSOP_HANDLE`   | unset   | If set, that handle is promoted to sysop on first registration (or at startup if the row already exists).  |
| `NIGHTMS_HOST_KEY_DIR`             | unset   | Directory in which SSH host keys are persisted (created on first boot, 0600 on Linux). Without this, host keys are ephemeral and regenerate every restart — every reconnect looks like a fresh server to the client. |

### Google + Microsoft SSO (optional)

When both client ID + secret are set for a provider, the "Sign in with X" button appears on `/login` and `/`. When unset, the provider is silently disabled and the rest of the BBS is unaffected.

| Env var                          | Equivalent appsetting key                       |
|----------------------------------|-------------------------------------------------|
| `NIGHTMS_GOOGLE_CLIENT_ID`       | `Authentication:Google:ClientId`                |
| `NIGHTMS_GOOGLE_CLIENT_SECRET`   | `Authentication:Google:ClientSecret`            |
| `NIGHTMS_MICROSOFT_CLIENT_ID`    | `Authentication:Microsoft:ClientId`             |
| `NIGHTMS_MICROSOFT_CLIENT_SECRET`| `Authentication:Microsoft:ClientSecret`         |

See [Setting up Google + Microsoft SSO](#setting-up-google--microsoft-sso) for IdP-side configuration.

### Storage paths

| Env var               | Equivalent appsetting key   | Default                                            | Purpose                                                                       |
|-----------------------|-----------------------------|----------------------------------------------------|-------------------------------------------------------------------------------|
| `NIGHTMS_LOGIN_ART_PATH` | `LoginArt:Path`           | unset (in-code ASCII fallback)                     | Path to a `.ans` (color half-block) or plain text banner shown on the lobby + register screen. |
| `NIGHTMS_ART_DIR`     | `ArtGallery:Path`           | `{AppContext.BaseDirectory}/art/gallery/`          | Directory the `Gallery` screen enumerates for `.ans` art pieces.              |
| `NIGHTMS_PFP_DIR`     | `ProfilePictures:Path`      | `{AppContext.BaseDirectory}/data/pfp/`             | Directory where profile-picture PNGs are stored (`{user_id}.png`).            |

### Weather defaults

The status bar can render weather for users who haven't set their own location. These supply the global fallback.

| Env var                  | Purpose                                                          |
|--------------------------|------------------------------------------------------------------|
| `NIGHTMS_WEATHER_LABEL`  | Display label (e.g. `NYC`).                                      |
| `NIGHTMS_WEATHER_LAT`    | Latitude in decimal degrees.                                     |
| `NIGHTMS_WEATHER_LON`    | Longitude in decimal degrees.                                    |

### Public-facing URL (display only)

| Env var                    | Equivalent appsetting key | Purpose                                                                                                              |
|----------------------------|---------------------------|----------------------------------------------------------------------------------------------------------------------|
| `NIGHTMS_PUBLIC_BASE_URL`  | `PublicBaseUrl`           | The externally-visible origin, e.g. `https://night.ms`. Used by display strings only — the "Connect via SSH" hint on `/` and `/u/{handle}`, plus the "upload at …" line in the TUI ProfileEditScreen. Not required for SSO callbacks (they derive from the request URL via `UseForwardedHeaders`). |

### Logging

ASP.NET's standard logging is wired up — pick a level via `Logging__LogLevel__Default` (e.g. `Debug`, `Information`, `Warning`).

## Setting up Google + Microsoft SSO

Both flows are standard OAuth 2.0 / OIDC. The redirect-URI paths the server registers are **`/signin-google`** and **`/signin-microsoft`** — they're owned by ASP.NET Core's authentication handlers, not by your code, so don't try to route them yourself.

### Google

1. Open [Google Cloud Console → APIs & Services → Credentials](https://console.cloud.google.com/apis/credentials).
2. Create **OAuth client ID** → Application type **Web application**.
3. Authorized JavaScript origins:
   - Dev: `http://localhost:5080`
   - Prod: `https://your.domain`
4. Authorized redirect URIs:
   - Dev: `http://localhost:5080/signin-google`
   - Prod: `https://your.domain/signin-google`
5. Copy the client ID + secret into `NIGHTMS_GOOGLE_CLIENT_ID` / `NIGHTMS_GOOGLE_CLIENT_SECRET`.

The handler asks for the `openid` + `email` scopes by default. The `email_verified` claim Google emits is used to gate the cross-provider auto-link.

### Microsoft

1. Open [Microsoft Entra ID → App registrations](https://entra.microsoft.com/) → **New registration**.
2. Supported account types: pick **Personal Microsoft accounts only** (consumer) or **Accounts in any organizational directory and personal Microsoft accounts** (multi-tenant + consumer).
3. Redirect URI → **Web** → `http://localhost:5080/signin-microsoft` (dev) or `https://your.domain/signin-microsoft` (prod).
4. Once the app is registered, go to **Certificates & secrets** → **New client secret** and copy the value into `NIGHTMS_MICROSOFT_CLIENT_SECRET`.
5. **Overview → Application (client) ID** is `NIGHTMS_MICROSOFT_CLIENT_ID`.

Microsoft Accounts require email verification at signup; the handler doesn't surface a separate `email_verified` claim, so the server treats the presence of an email as verified.

### Identity model & auto-link

- Each user owns N rows in `identity_credentials` keyed `(Provider, Subject)`. One user can have an SSH key, a Google identity, and a Microsoft identity all linked to the same handle.
- If a verified email from an incoming SSO ticket matches an existing user's `users.email`, the new credential is **auto-linked** to that user (with an `identity.linked` audit row). Unverified emails never auto-link.
- The user can also link/unlink credentials manually from `/profile`. The last remaining credential can't be unlinked (we don't lock anyone out of their own account).

## First-run sysop

There's no UI for promoting yourself to sysop. Two paths:

1. **Bootstrap before signup.** Set `NIGHTMS_BOOTSTRAP_SYSOP_HANDLE=alice` and start the server; when `alice` registers (TOFU on SSH first connect, or web SSO onboarding picker), the new row is created with `is_sysop = true`. If `alice` already exists, the same env var promotes her at startup.
2. **Promote later via SQL.** `UPDATE users SET is_sysop = true WHERE handle = 'alice';`. The next time `alice` enters the lobby, the Sysop console becomes available.

Sysop actions write `audit_log` rows.

## Database migrations

EF Core 10 handles schema. `DatabaseInitializer` (a hosted service) runs `MigrateAsync` and seeds `#lobby` + the `General` forum on every startup, so you don't normally need to run anything manually. To author a new migration:

```sh
# Set ConnectionStrings__bbs so the design factory finds a live Postgres.
# Match whatever your dev container is using (run.ps1 default is 55432):
$env:ConnectionStrings__bbs = "Host=127.0.0.1;Port=55432;Database=bbs;Username=postgres;Password=postgres"

dotnet ef migrations add <Name> --project src/Night.Ms.SshServer
dotnet ef database update      --project src/Night.Ms.SshServer
```

`AppDbContextDesignFactory` reads `ConnectionStrings__bbs` and falls back to `localhost:5432` if unset — there's no separate hard-coded port to keep in sync.

## Where data lives on disk

Defaults are under `data/` relative to `AppContext.BaseDirectory` (i.e. the build output dir). Override the parent dir per-env-var if you want them somewhere else.

```
data/
  host-keys/    Persistent SSH host keys (RSA + ECDSA + ed25519). Set NIGHTMS_HOST_KEY_DIR.
  pfp/          Profile pictures, one PNG per user keyed on user id. Set NIGHTMS_PFP_DIR.
```

Mount these into your container as a volume so they survive restarts; `data/` is git-ignored.

## Production deployment notes

The server is HTTP-only — TLS terminates upstream at a reverse proxy (Caddy, Cloudflare, nginx). The `UseForwardedHeaders` middleware is wired so the OIDC handlers and the cookie auth see the original scheme/host from `X-Forwarded-Proto` / `X-Forwarded-For`. Behind a real proxy:

- Cookies use `SameSite=Lax`, `SecurePolicy=SameAsRequest`. With TLS upstream, the cookie is marked Secure automatically.
- SSO redirect URIs must match the externally-visible URL (`https://your.domain/signin-google`).
- `NIGHTMS_PUBLIC_BASE_URL` is purely cosmetic — it's what the UI puts in the "Connect via SSH" hint and the "upload at …" line. The auth path doesn't need it.

Example deploy environment:

```sh
ConnectionStrings__bbs=Host=postgres;Port=5432;Database=bbs;Username=bbs;Password=…
ConnectionStrings__redis=redis:6379,abortConnect=false
BBS_SSH_PORT=2222
BBS_HTTP_PORT=5080
NIGHTMS_HOST_KEY_DIR=/var/lib/nightms/host-keys
NIGHTMS_PFP_DIR=/var/lib/nightms/pfp
NIGHTMS_BOOTSTRAP_SYSOP_HANDLE=nick
NIGHTMS_PUBLIC_BASE_URL=https://night.ms
NIGHTMS_GOOGLE_CLIENT_ID=…
NIGHTMS_GOOGLE_CLIENT_SECRET=…
NIGHTMS_MICROSOFT_CLIENT_ID=…
NIGHTMS_MICROSOFT_CLIENT_SECRET=…
```

`run.ps1` is dev-only; production usually means systemd or a container. The included `src/Night.Ms.SshServer/Dockerfile` is a multi-stage build; expose `2222/tcp` and `5080/tcp` and mount `NIGHTMS_HOST_KEY_DIR` + `NIGHTMS_PFP_DIR` as volumes so data survives restarts.

## Continuous deployment

`.github/workflows/deploy.yml` is a **manually-triggered** workflow that builds the Docker image on a GitHub runner, pushes it to GHCR (`ghcr.io/nickna/ssh.night.ms`), `scp`'s the latest `deploy/compose.yml` to the VPS, then SSHes in and runs `docker compose pull && up -d`. Triggered from the Actions tab → **deploy** → **Run workflow** (optional `ref` input to deploy a non-`main` branch / tag / SHA).

The VPS only needs the `.env` file and the deploy user's SSH access — no git checkout, no source code. `compose.yml` is delivered fresh on every run.

### One-time GitHub setup

1. **Generate a deploy SSH keypair.** On a workstation:
   ```sh
   ssh-keygen -t ed25519 -f ./nightms-deploy -C "nightms-deploy" -N ""
   ```
   This produces `nightms-deploy` (private) and `nightms-deploy.pub` (public).

2. **Authorize the public key on the VPS.** As the deploy user (e.g. `deploy`):
   ```sh
   # On the VPS:
   mkdir -p ~/.ssh && chmod 700 ~/.ssh
   cat >> ~/.ssh/authorized_keys <<'KEY'
   <paste contents of nightms-deploy.pub here>
   KEY
   chmod 600 ~/.ssh/authorized_keys
   ```
   The deploy user needs `docker` group membership and write access to `DEPLOY_PATH`.

3. **Add secrets to the repo.** Settings → Secrets and variables → Actions → New repository secret. Create a `production` environment first (Settings → Environments → New environment → `production`) if you want the deploy job gated by environment protection rules; otherwise the secrets can live on the repo directly.

   | Secret              | Example value                          | Notes                                                                                          |
   |---------------------|----------------------------------------|------------------------------------------------------------------------------------------------|
   | `SSH_PRIVATE_KEY`   | contents of `nightms-deploy`           | Whole file including the `-----BEGIN…/END…-----` lines.                                        |
   | `SSH_HOST`          | `vps.night.ms` or the IP               | Resolvable from the GitHub runner.                                                              |
   | `SSH_USER`          | `deploy`                               | The Linux user the runner will `ssh` in as.                                                     |
   | `SSH_PORT`          | `2200`                                 | Optional; defaults to `22`. Set if you moved sshd off `:22` to free it for the BBS container.   |
   | `DEPLOY_PATH`       | `/home/deploy/nightms`                 | Absolute path on the VPS. The workflow drops `compose.yml` here; you put `.env` here.           |

4. **Make the GHCR package public** (one-time, after the first successful workflow run). GHCR creates packages as private by default. Open `https://github.com/users/nickna/packages/container/ssh.night.ms/settings` → **Change visibility** → **Public**. Otherwise the VPS needs to `docker login ghcr.io` with a PAT to pull.

### One-time VPS prep

Stand up the directory the workflow will deploy into, and put the `.env` file there. From your **workstation**:

```sh
scp deploy/.env.example <user>@<vps>:/home/<user>/nightms/.env
# (or copy + edit on the VPS directly)
ssh <user>@<vps>
chmod 600 ~/nightms/.env
nano ~/nightms/.env   # fill in POSTGRES_PASSWORD at minimum
```

That's it — no `git clone` on the VPS. On the **first** workflow run the deploy step uploads `compose.yml`, pulls the image, and starts the stack. Postgres and Redis volumes persist across subsequent deploys.

### Rolling back

The build job tags every image with both `latest` and `sha-<short>`. To roll back without touching the workflow, SSH to the VPS and override the tag:

```sh
cd ~/nightms
# Edit compose.yml: change `image: ghcr.io/nickna/ssh.night.ms:latest`
#                       to `image: ghcr.io/nickna/ssh.night.ms:sha-abc1234`
docker compose --env-file .env pull app
docker compose --env-file .env up -d
```

The next workflow run will overwrite `compose.yml` and put you back on `:latest`. Or re-run the workflow against the previous good commit SHA via the `ref` input — the build job tags the new image with that ref's short SHA.
