# Multi-stage build for ssh.night.ms: produces a tiny scratch image with
# the single statically-linked nightms binary. The volume mount /data carries
# SSH host keys + uploaded profile pictures + the local art gallery; the
# images directly under /app/templates are baked into the binary via go:embed.
#
# Build:  docker build -t ssh.night.ms:dev .
# Run:    docker run --rm -p 2222:2222 -p 5080:5080 -v nightms-data:/data \
#           -e NIGHTMS_DB_CONN=... -e NIGHTMS_REDIS_CONN=... ssh.night.ms:dev
#
# The intended way to run this in prod is via deploy/compose.yml, which wires
# the app together with Postgres + Redis. TLS for the web side is expected to
# terminate at Cloudflare's edge.

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO disabled + tags=osusergo,netgo for a fully-static binary that runs in
# scratch. The internal/data/migrations + internal/web/templates dirs are
# already embedded via //go:embed, so nothing outside the binary is needed.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -tags osusergo,netgo \
      -ldflags "-s -w" \
      -o /out/nightms ./cmd/nightms

# scratch is ~5MB total image. ca-certificates pulled in so https provider
# calls (open-meteo, hackernews, coingecko) verify TLS correctly.
FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch AS runtime
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/nightms /nightms

# /data is the volume mount point. Persistence required: host-keys (so
# clients' known_hosts entries don't trip), profile-pictures, art/gallery.
# scratch doesn't have a shell to mkdir these, so the runtime creates them
# on first use; documenting them here is just a reminder for compose.
EXPOSE 2222 5080

ENV NIGHTMS_HOST_KEY_DIR=/data/host-keys \
    NIGHTMS_PFP_DIR=/data/profile-pictures \
    NIGHTMS_ART_DIR=/data/art/gallery \
    BBS_SSH_PORT=2222 \
    BBS_HTTP_PORT=5080

ENTRYPOINT ["/nightms"]
