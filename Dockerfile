# syntax=docker/dockerfile:1
#
# Plugin image for the fluent-bit-output-go OTLP output plugin.
#
# The image is intended to run as a Kubernetes initContainer alongside a
# fluent-bit container. Its only job is to copy the pre-built go-out.so plugin
# onto a shared emptyDir volume so fluent-bit can load it via `-e`.
#
# Build:  docker build -t ghcr.io/nickytd/fluent-bit-output-go:local .
# Run:    docker run --rm -v $(pwd)/out:/output IMAGE
# The resulting go-out.so lands in ./out/.

# --- Stage 1: build the cgo plugin against glibc ---------------------------
#
# The plugin is cgo-linked and must match the libc of the fluent-bit runtime
# container. Fluent Bit's official images (fluent/fluent-bit) are glibc-based
# (debian), so we build against a glibc toolchain here.

ARG GO_VERSION=1.26.3
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS plugin-builder

# Cross-compile the plugin for the target platform. CGO cross builds need the
# matching cross toolchain; we install both arm64 and amd64 cross-compilers so
# TARGETPLATFORM can be either.
RUN apt-get update && apt-get install -y --no-install-recommends \
      gcc-aarch64-linux-gnu \
      libc6-dev-arm64-cross \
      gcc-x86-64-linux-gnu \
      libc6-dev-amd64-cross \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETPLATFORM
RUN --mount=type=cache,target=/root/.cache/go-build \
    case "${TARGETPLATFORM}" in \
      "linux/amd64") export GOARCH=amd64 CC=x86_64-linux-gnu-gcc ;; \
      "linux/arm64") export GOARCH=arm64 CC=aarch64-linux-gnu-gcc ;; \
      *) echo "unsupported platform: ${TARGETPLATFORM}"; exit 1 ;; \
    esac; \
    CGO_ENABLED=1 GOOS=linux go build -buildmode=c-shared -o /out/go-out.so .

# --- Stage 2: build the static copy tool -----------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS copy-builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETPLATFORM
RUN --mount=type=cache,target=/root/.cache/go-build \
    case "${TARGETPLATFORM}" in \
      "linux/amd64") export GOARCH=amd64 ;; \
      "linux/arm64") export GOARCH=arm64 ;; \
      *) echo "unsupported platform: ${TARGETPLATFORM}"; exit 1 ;; \
    esac; \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags='-s -w' \
      -o /out/copy-plugin \
      ./cmd/copy-plugin

# --- Stage 3: final scratch image ------------------------------------------
FROM scratch

# Metadata (OCI labels; populated at build time by the release workflow).
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="fluent-bit-output-go" \
      org.opencontainers.image.description="OTLP output plugin for Fluent Bit, delivered as an initContainer that copies go-out.so onto a shared volume." \
      org.opencontainers.image.source="https://github.com/nickytd/fluent-bit-output-go" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

COPY --from=plugin-builder /out/go-out.so /plugin/go-out.so
COPY --from=copy-builder /out/copy-plugin /copy-plugin

# Default entrypoint copies /plugin/go-out.so to /output/go-out.so. The user
# is expected to mount an emptyDir (or another writable volume) at /output.
ENTRYPOINT ["/copy-plugin"]
CMD ["-src=/plugin/go-out.so", "-dst=/output"]
