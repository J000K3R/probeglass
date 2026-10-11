# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------
# The builder is pinned to the same Go release the module declares, on the
# build platform, and cross-compiles to the target. CGO is off, so no C
# toolchain for the target is needed: the SQLite driver is pure Go.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first, so a change to the code does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# -trimpath keeps the build path out of the binary, which makes builds
# reproducible and crashes readable. The version is stamped into the model
# package, where the dashboard reads it from.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/j000k3r/weatherglass/internal/model.Version=${VERSION} \
        -X github.com/j000k3r/weatherglass/internal/model.Commit=${COMMIT} \
        -X github.com/j000k3r/weatherglass/internal/model.BuildDate=${BUILD_DATE}" \
      -o /out/weatherglass ./cmd/weatherglass

# An empty directory whose ownership the runtime image can copy. Docker copies
# the ownership of a copied directory, which is how /data ends up writable by
# the container user when a fresh named volume is created from this image. 500 is
# the default PUID, and the entrypoint corrects it whenever PUID says otherwise.
RUN mkdir -p /out/data

# ---------------------------------------------------------------------------
# runtime
# ---------------------------------------------------------------------------
# distroless static has no shell, no package manager and no libc to speak of:
# the attack surface is the binary and nothing else.
#
# It starts as root, and does not stay there. The data directory is a bind mount on
# the platforms this gets installed on, and those platforms create that folder as
# root before the container ever sees it. A container that starts as anybody else
# cannot write in it, and cannot repair it either, because only root can change who
# owns a file. So the daemon hands the folder to PUID:PGID and becomes that user
# before it opens the database or binds a port. It is what the LinuxServer images do,
# and the reason an install of one of those needs no chown.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Weatherglass" \
      org.opencontainers.image.description="Lightweight self-hosted monitoring for the homelab: service sensors, a dashboard, long-term history and alerts." \
      org.opencontainers.image.url="https://github.com/j000k3r/weatherglass" \
      org.opencontainers.image.source="https://github.com/j000k3r/weatherglass" \
      org.opencontainers.image.documentation="https://github.com/j000k3r/weatherglass/blob/main/README.md" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/weatherglass /usr/local/bin/weatherglass

# Everything mutable lives under /data: the SQLite database and its WAL. A copied
# directory carries its ownership along, so a named volume created from this image is
# writable by the default PUID with nobody having to fix it first. A bind mount brings
# the host folder's ownership instead, and that one is handed to PUID by the entrypoint
# before it drops.
COPY --from=build --chown=500:500 /out/data /data

# Root, so that the daemon can take the data directory over and then become PUID:PGID.
# That is the whole reason this is not 500:500 as it once was, and it is not a matter of
# taste: the container runtime resolves USER before exec, so a container started as 500
# could never have claimed ownership of the folder, whatever was set inside it. PUID,
# PGID and UMASK are read by the running process, after it is root — which is why they
# work here, and why they do nothing at all when the container is started with --user
# or on a runtime that refuses to run it as root.
USER 0:0

ENV PUID=500 \
    PGID=500 \
    UMASK=022 \
    WEATHERGLASS_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080

# The binary probes itself: distroless has no curl, and adding one would defeat
# the point of a distroless image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/weatherglass", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/weatherglass"]
CMD ["serve", "--listen", ":8080"]
