# Multi-stage build for ssh.night.ms. The runtime image is now debian:bookworm-
# slim (was scratch) so it can host the Carbonyl child process — a Chromium-
# based terminal browser that ships as a bundle of ~410 .so files under
# /opt/carbonyl. The base+deps add ~80 MB, and the carbonyl bundle adds
# another ~400 MB, so the image is now ~500–600 MB total (was ~5 MB scratch).
# That's the cost of bundling a browser; the rich-mode feature is otherwise
# soft-disabled (NIGHTMS_CARBONYL_ENABLED defaults to 0).
#
# Build:  docker build -t ssh.night.ms:dev .
# Run:    docker run --rm -p 2222:2222 -p 5080:5080 -v nightms-data:/data \
#           -e NIGHTMS_DB_CONN=... -e NIGHTMS_REDIS_CONN=... ssh.night.ms:dev
#
# Bundle prerequisite: bundle/carbonyl-linux-x86_64.tar.xz must be present in
# the build context. The repo ships it via Git LFS; clone with LFS enabled
# (`git lfs install` then `git lfs pull`) before building. CI must also
# `git lfs install` before the docker build step.
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
# CGO disabled + tags=osusergo,netgo so the binary doesn't depend on the
# bookworm libc at link time — keeps it portable + lets the build stage stay
# on Alpine. The internal/data/migrations + internal/web/templates dirs are
# already embedded via //go:embed, so nothing outside the binary is needed.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -tags osusergo,netgo \
      -ldflags "-s -w" \
      -o /out/nightms ./cmd/nightms

# Carbonyl bundle extraction stage. Verifies the bundle against its committed
# sha256 file so a corrupted LFS download fails the build loud instead of
# producing a silently-broken image. The bundle layout (binary + .so closure
# + .pak + .dat + .bin files all at the top level) matches what carbonyl's
# RPAT=$ORIGIN expects — no LD_LIBRARY_PATH needed at runtime.
FROM debian:bookworm-slim AS carbonyl-stage
RUN apt-get update && apt-get install -y --no-install-recommends xz-utils ca-certificates && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /carbonyl
COPY bundle/carbonyl-linux-x86_64.tar.xz bundle/carbonyl-linux-x86_64.sha256 ./
RUN expected=$(cat carbonyl-linux-x86_64.sha256) && \
    actual=$(sha256sum carbonyl-linux-x86_64.tar.xz | awk '{print $1}') && \
    if [ "$expected" != "$actual" ]; then \
      echo "carbonyl bundle sha256 mismatch: expected $expected got $actual" >&2; \
      exit 1; \
    fi && \
    mkdir -p /opt/carbonyl && \
    tar -C /opt/carbonyl -xJf carbonyl-linux-x86_64.tar.xz && \
    chmod 0755 /opt/carbonyl/carbonyl

# Runtime: debian:bookworm-slim (was scratch). bookworm picks up newer glibc +
# NSS that Carbonyl/Chromium M148+ wants. The four packages below are the
# carbonyl Dockerfile's own runtime deps; their transitive dependencies cover
# the rest of the .so closure (glib stack, freetype, harfbuzz, pango, udev,
# zlib, brotli, NSPR).
FROM debian:bookworm-slim AS runtime
# Runtime libs for Carbonyl/Chromium M148+. The four "carbonyl Dockerfile"
# packages (libasound2 libexpat1 libfontconfig1 libnss3) cover the security/
# audio/text-shaping/TLS stack but DON'T transitively pull in the GLib stack
# in bookworm-slim — newer Chromium drags in libglib + libpango directly.
# Test: `docker run ... carbonyl --version` will surface any missing .so as a
# loader error; add the providing package here when it does.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        libasound2 libexpat1 libfontconfig1 libnss3 \
        libglib2.0-0 libpango-1.0-0 libudev1 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /out/nightms /nightms
COPY --from=carbonyl-stage /opt/carbonyl /opt/carbonyl

# /data is the volume mount point. Persistence required: host-keys (so
# clients' known_hosts entries don't trip), profile-pictures, art/gallery,
# and per-user carbonyl --user-data-dir state (cookies + history survive
# reconnect, deleted by the runtime if the carbonyl_enabled switch is left
# off — no orphan data accumulates while the feature is dark).
EXPOSE 2222 5080

ENV NIGHTMS_HOST_KEY_DIR=/data/host-keys \
    NIGHTMS_PFP_DIR=/data/profile-pictures \
    NIGHTMS_ART_DIR=/data/art/gallery \
    NIGHTMS_CARBONYL_BIN_PATH=/opt/carbonyl/carbonyl \
    NIGHTMS_CARBONYL_DATA_DIR=/data/carbonyl \
    BBS_SSH_PORT=2222 \
    BBS_HTTP_PORT=5080

ENTRYPOINT ["/nightms"]
