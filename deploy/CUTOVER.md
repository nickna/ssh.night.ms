# Cutover runbook: .NET → Go

This runbook captures the big-bang cutover from the .NET BBS to the Go port.
Both stacks target the same Postgres schema, so the cutover is a deploy swap;
data does not move.

## Preflight (≥ 24h before)

1. Smoke the Go binary against staging (or a copy of prod's Postgres on a
   dev VPS):
   ```sh
   docker build -t ghcr.io/nickna/ssh.night.ms:rc1 .
   docker push ghcr.io/nickna/ssh.night.ms:rc1
   docker compose -f deploy/compose.yml --env-file deploy/.env up -d
   ssh -p 22 nick@<staging-host>
   ```
   Expected: lobby + chat + boards + news + weather + gallery + finance +
   map + slots + video poker + blackjack + hold'em + sysop screen all
   render. Sysop screen + audit log entries write through.
2. Run the loadtest harness on staging at the planned cutover concurrency.
   The Phase 7 exit gate is **200 sessions, 10 minutes, 0 failures**, run
   under each scenario in turn — idle alone doesn't exercise the Redis
   chat fan-out or the forums read path, which are the most likely
   failure modes under real users:
   ```sh
   ./bin/loadtest seed -count 200
   # Steady-state RSS + connect/handshake.
   ./bin/loadtest run -count 200 -host <staging>:22 -ramp 30s -duration 10m -scenario idle
   # Chat fan-out via Redis pub/sub.
   ./bin/loadtest run -count 200 -host <staging>:22 -ramp 30s -duration 10m -scenario chat
   # Forums read path + post_reads markers.
   ./bin/loadtest run -count 200 -host <staging>:22 -ramp 30s -duration 10m -scenario forums
   # Realistic blend — primary go/no-go.
   ./bin/loadtest run -count 200 -host <staging>:22 -ramp 30s -duration 10m -scenario mix
   ./bin/loadtest clean
   ```
3. Verify both stacks read the same DB without writing. Spin up .NET on
   staging against the staging DB **read-only** (or use a snapshot) and
   compare any chat history a Go session produced.

## Cutover (during the maintenance window)

Estimated downtime: 5–10 minutes. Schema is unchanged — no migration delay.

1. Announce in `#lobby` from the .NET stack.
2. Stop the .NET stack on the prod VPS:
   ```sh
   docker compose -f /srv/ssh.night.ms/deploy/compose.yml down
   ```
3. (Optional, but cheap) snapshot Postgres so rollback is instant:
   ```sh
   docker run --rm -v nightms_pg-data:/from -v "$PWD":/to alpine \
     tar czf /to/pg-pre-cutover-$(date +%Y%m%d-%H%M).tgz -C /from .
   ```
4. Deploy the Go stack. Preferred path is the `deploy` workflow in the
   Actions tab (manual `workflow_dispatch`) — it builds the image, pushes
   to GHCR, SCPs the new `compose.yml`, and runs `docker compose pull &&
   up -d`. Manual fallback:
   ```sh
   docker compose -f deploy/compose.yml --env-file deploy/.env pull
   docker compose -f deploy/compose.yml --env-file deploy/.env up -d
   ```
   The compose `name:` is `nightms` (matching the .NET stack), so the new
   app container mounts the existing `nightms_pg-data`, `nightms_app-data`,
   and `nightms_redis-data` volumes automatically — no manual `external:
   true` remap. Postgres data, SSH host keys, and uploaded profile pictures
   carry over; clients' `known_hosts` entries continue to validate.
5. Verify (≤ 5 min):
   - `ssh -p 22 nick@night.ms` → lobby; SYSOP badge; chat works.
   - `curl -sf https://night.ms/healthz` (Cloudflare → :80 → :5080).
   - Web login + open terminal: lobby renders in-browser via WebSocket.
   - `docker compose logs -f app` shows `migrations: up-to-date` and no
     ERROR-level entries.

## Rollback (if step 5 fails)

The schema is unchanged across the swap, so rollback is just redeploying the
.NET stack against the same Postgres volume. Any rows written by the Go
binary stay valid — schema is identical.

The .NET source no longer lives on `main` after the code-flip; the rollback
path is to re-run the `deploy` workflow against the last pre-cutover commit
(use `git log` to find the SHA that still has `src/Night.Ms.SshServer/`),
which rebuilds and pushes the .NET image and redeploys it:

```sh
docker compose -f deploy/compose.yml down
# Trigger the deploy workflow with ref=<pre-cutover-sha>, then:
docker compose -f deploy/compose.yml --env-file deploy/.env up -d
```

Sanity check: `ssh -p 22 nick@night.ms` → lobby renders from .NET.

## Post-cutover (≤ 7 days)

- Watch `docker compose logs -f app` for ERROR-level entries during the
  first 24h. Anything in `internal/realtime/` (chat fan-out) or
  `internal/transport/` (auth + handshake) is highest priority.
- Spot-check Postgres for any unexpected rows: the .NET stack's
  `__EFMigrationsHistory` table is harmless under Go (golang-migrate uses
  `schema_migrations`); both coexist.
- After 7 days of clean operation, tag the last pre-cutover commit (e.g.
  `git tag dotnet-final <sha> && git push origin dotnet-final`) so the
  rollback path stays discoverable. Volumes need no cleanup — both stacks
  share the same `nightms_*` names.
