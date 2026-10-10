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
# the container user when a fresh named volume is created from this image.
RUN mkdir -p /out/data

# ---------------------------------------------------------------------------
# runtime
# ---------------------------------------------------------------------------
# distroless static has no shell, no package manager and no libc to speak of:
# the attack surface is the binary and nothing else. It runs as uid 500, not
# root.
#
# 500 rather than distroless's own 65532, which would be the tidier default:
# the platforms this actually gets installed on create the appdata folder as
# 500:500, and a folder the process does not own is a container that exits a
# second after it starts with nothing useful in the log. Matching them means a
# fresh install works with no chown. See docs/operations/troubleshooting.md.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Weatherglass" \
      org.opencontainers.image.description="Lightweight self-hosted monitoring for the homelab: service sensors, a dashboard, long-term history and alerts." \
      org.opencontainers.image.url="https://github.com/j000k3r/weatherglass" \
      org.opencontainers.image.source="https://github.com/j000k3r/weatherglass" \
      org.opencontainers.image.documentation="https://github.com/j000k3r/weatherglass/blob/main/README.md" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/weatherglass /usr/local/bin/weatherglass

# Everything mutable lives under /data: the SQLite database and its WAL. The
# directory is created here, owned by the container user, so a named volume
# created from this image is writable without any --user gymnastics.
COPY --from=build --chown=500:500 /out/data /data

# The uid is fixed here rather than configurable, because it cannot be
# otherwise: the container runtime resolves USER before exec, and an
# environment variable is read by a process that is already running as that
# uid. To run as somebody else, override it where it is actually decided --
# `user:` in compose, `--user` for docker run, the user field in the platform's
# own template. Anything else would be an environment variable that appears to
# work and quietly does nothing.
USER 500:500

ENV WEATHERGLASS_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080

# The binary probes itself: distroless has no curl, and adding one would defeat
# the point of a distroless image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/weatherglass", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/weatherglass"]
CMD ["serve", "--listen", ":8080"]
