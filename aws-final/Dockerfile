# ---------- Build stage ----------
# Always run the compiler natively on the build machine (no QEMU),
# and let Go cross-compile for the target platform.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

WORKDIR /app

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy source (main.go, AWS/, kubernetes/)
COPY . .

# Buildx sets these automatically per target platform
ARG TARGETOS
ARG TARGETARCH

# Static binary, so it runs on a minimal image
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /ec2-launcher .

# ---------- Runtime stage ----------
FROM alpine:3.20

# CA certs are required for HTTPS calls to AWS APIs
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /ec2-launcher /ec2-launcher

ENTRYPOINT ["/ec2-launcher"]