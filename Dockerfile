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

# These two carry the platform being built for, put there by buildx, and they must
# be declared with no value. An ARG that has one keeps it: `ARG TARGETARCH=amd64`
# made every platform compile amd64, so the arm64 and arm/v7 images shipped an
# x86-64 binary under an arm64 and an arm label — a container that dies with "exec
# format error" on the Pi, and on a machine that does have an emulator it runs, at
# emulator speed, crashing now and then in the garbage collector. BuildKit sees the
# three variants ask for identical arguments and folds them into a single build step,
# which is why all three published images share one build ID. The three VERSION args
# below keep their defaults because they are ordinary build arguments, which a
# workflow's `build-args` does override.
ARG TARGETOS
ARG TARGETARCH
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
        -X github.com/j000k3r/probeglass/internal/model.Version=${VERSION} \
        -X github.com/j000k3r/probeglass/internal/model.Commit=${COMMIT} \
        -X github.com/j000k3r/probeglass/internal/model.BuildDate=${BUILD_DATE}" \
      -o /out/probeglass ./cmd/probeglass

# The mistake above was silent for as long as it existed because nothing ever looked
# at what came out. The two bytes at offset 18 of an ELF header are its machine type —
# 3e00 is x86-64, b700 is AArch64, 2800 is ARM — so the binary can be asked what it is
# without being run, which is the only way to ask it here: the machine that builds the
# arm64 image is usually an amd64 one and cannot execute the answer.
#
# The comparison is against TARGETPLATFORM, the platform the image is for, and not
# against TARGETARCH, which is the same thing until the day it is shadowed by a default
# — then it said amd64 for an arm64 build, so the two would have agreed with each other
# while both disagreed with the image being made. It is declared below, beside the check
# that reads it, and without a value, which is what leaves buildx's own in place.
ARG TARGETPLATFORM
RUN case "${TARGETPLATFORM}" in \
      linux/amd64)  want=3e00 ;; \
      linux/arm64)  want=b700 ;; \
      linux/arm/*)  want=2800 ;; \
      *) echo "no machine type known for ${TARGETPLATFORM}"; exit 1 ;; \
    esac; \
    got=$(od -An -tx1 -j18 -N2 /out/probeglass | tr -d ' \n'); \
    test "$got" = "$want" \
      || { echo "the binary's machine type is $got, but ${TARGETPLATFORM} wants $want"; exit 1; }

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

LABEL org.opencontainers.image.title="ProbeGlass" \
      org.opencontainers.image.description="Lightweight self-hosted monitoring for the homelab: service sensors, a dashboard, long-term history and alerts." \
      org.opencontainers.image.url="https://github.com/j000k3r/probeglass" \
      org.opencontainers.image.source="https://github.com/j000k3r/probeglass" \
      org.opencontainers.image.documentation="https://github.com/j000k3r/probeglass/blob/main/README.md" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/probeglass /usr/local/bin/probeglass

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
    PROBEGLASS_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080

# The binary probes itself: distroless has no curl, and adding one would defeat
# the point of a distroless image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/probeglass", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/probeglass"]
CMD ["serve", "--listen", ":8080"]
