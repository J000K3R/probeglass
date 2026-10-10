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
# the nonroot user when a fresh named volume is created from this image.
RUN mkdir -p /out/data

# ---------------------------------------------------------------------------
# runtime
# ---------------------------------------------------------------------------
# distroless static has no shell, no package manager and no libc to speak of:
# the attack surface is the binary and nothing else. It runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Weatherglass" \
      org.opencontainers.image.description="Lightweight self-hosted monitoring for the homelab: service sensors, a dashboard, long-term history and alerts." \
      org.opencontainers.image.url="https://github.com/j000k3r/weatherglass" \
      org.opencontainers.image.source="https://github.com/j000k3r/weatherglass" \
      org.opencontainers.image.documentation="https://github.com/j000k3r/weatherglass/blob/main/README.md" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/weatherglass /usr/local/bin/weatherglass

# Everything mutable lives under /data: the SQLite database and its WAL. The
# directory is created here, owned by the nonroot user, so a named volume
# created from this image is writable without any --user gymnastics.
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532

ENV WEATHERGLASS_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080

# The binary probes itself: distroless has no curl, and adding one would defeat
# the point of a distroless image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/weatherglass", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/weatherglass"]
CMD ["serve", "--listen", ":8080"]
