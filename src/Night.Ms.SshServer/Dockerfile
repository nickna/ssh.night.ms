# Multi-stage build for the ssh.night.ms SshServer.
#
# Build:    docker build -f src/Night.Ms.SshServer/Dockerfile -t ssh.night.ms:dev .
#
# The intended way to run this in prod is via deploy/compose.yml, which wires
# the app together with Postgres and Redis. This Dockerfile only builds the app
# image; TLS for the web side is expected to terminate at Cloudflare's edge.

ARG DOTNET_VERSION=10.0
ARG DOTNET_SDK_VERSION=10.0-preview

# Nightly registry — has the 10.0.3xx preview SDK band that global.json pins.
# The public mcr.microsoft.com/dotnet/sdk:10.0-preview image is still on
# 10.0.1xx, which doesn't satisfy `rollForward: latestFeature`.
FROM mcr.microsoft.com/dotnet/nightly/sdk:${DOTNET_SDK_VERSION} AS build
WORKDIR /src

# Copy manifests first so package restore caches across source changes.
COPY global.json ./
COPY src/Night.Ms.Imaging/Night.Ms.Imaging.csproj           src/Night.Ms.Imaging/
COPY src/Night.Ms.SshServer/Night.Ms.SshServer.csproj       src/Night.Ms.SshServer/
COPY src/Night.Ms.SshTransport/Night.Ms.SshTransport.csproj src/Night.Ms.SshTransport/
RUN dotnet restore src/Night.Ms.SshServer

COPY . .
RUN dotnet publish src/Night.Ms.SshServer \
      --no-restore \
      --configuration Release \
      --output /app/publish \
      /p:UseAppHost=false

FROM mcr.microsoft.com/dotnet/aspnet:${DOTNET_VERSION} AS runtime
WORKDIR /app
COPY --from=build /app/publish ./

# /data is the volume mount point for runtime state that must survive container
# restarts: SSH host keys (so reconnects don't trip clients' known_hosts warning)
# and uploaded profile pictures.
RUN mkdir -p /data/host-keys /data/pfp && chown -R 1000:1000 /data
USER 1000:1000

EXPOSE 2222 5080

ENV NIGHTMS_HOST_KEY_DIR=/data/host-keys \
    NIGHTMS_PFP_DIR=/data/pfp \
    BBS_SSH_PORT=2222 \
    BBS_HTTP_PORT=5080

ENTRYPOINT ["dotnet", "Night.Ms.SshServer.dll"]
