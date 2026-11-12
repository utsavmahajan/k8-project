# ---------- Build stage ----------
# Runs natively on the build machine (no QEMU); Go cross-compiles.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

WORKDIR /src

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

# Injected automatically by buildx for each target platform
ARG TARGETOS
ARG TARGETARCH

# Static, stripped binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/app .

# CA certs are needed for the HTTPS git clone
# (runs natively; the cert bundle is the same for every architecture)
RUN apk add --no-cache ca-certificates

# ---------- Final stage ----------
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/app /app

WORKDIR /work

ENTRYPOINT ["/app"]