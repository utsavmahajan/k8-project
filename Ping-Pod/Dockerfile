# syntax=docker/dockerfile:1

# Build stage runs on the host's native arch and cross-compiles,
# which is much faster than emulating arm64 under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Cache dependencies separately from source
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY main.go ./

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ping-pod .

# Final image: small, non-root, has CA certs
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/ping-pod /ping-pod

USER nonroot:nonroot
ENTRYPOINT ["/ping-pod"]