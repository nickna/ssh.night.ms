# Multi-stage build for the ssh.night.ms SshServer.
#
# Build:    docker build -f src/Night.Ms.SshServer/Dockerfile -t ssh.night.ms:dev .
# Run:      docker run -d --rm \
#             -p 2222:2222 \
#             -v $(pwd)/host-keys:/data/host-keys \
#             -e ConnectionStrings__bbs="Host=postgres;Port=5432;Database=bbs;Username=postgres;Password=postgres" \
#             -e ConnectionStrings__redis="redis:6379,abortConnect=false" \
#             -e NIGHTMS_HOST_KEY_DIR=/data/host-keys \
#             -e NIGHTMS_BOOTSTRAP_SYSOP_HANDLE=youroperator \
#             --name ssh.night.ms \
#             ssh.night.ms:dev
#
# Production: front port 2222 with iptables/nftables to expose on :22.

ARG DOTNET_VERSION=10.0
ARG DOTNET_SDK_VERSION=10.0-preview

FROM mcr.microsoft.com/dotnet/sdk:${DOTNET_SDK_VERSION} AS build
WORKDIR /src

# Copy manifests first so package restore caches across source changes.
COPY global.json ./
COPY src/Night.Ms.AppHost/Night.Ms.AppHost.csproj                 src/Night.Ms.AppHost/
COPY src/Night.Ms.ServiceDefaults/Night.Ms.ServiceDefaults.csproj src/Night.Ms.ServiceDefaults/
COPY src/Night.Ms.SshServer/Night.Ms.SshServer.csproj             src/Night.Ms.SshServer/
COPY src/Night.Ms.SshTransport/Night.Ms.SshTransport.csproj       src/Night.Ms.SshTransport/
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

# /data is the volume mount point for host keys (and any future runtime state).
RUN mkdir -p /data/host-keys && chown -R 1000:1000 /data
USER 1000:1000

# Default to the SSH listener on 2222 (host front-side maps :22 → :2222).
EXPOSE 2222

ENV NIGHTMS_HOST_KEY_DIR=/data/host-keys

ENTRYPOINT ["dotnet", "Night.Ms.SshServer.dll"]
