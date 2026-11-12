# ---------- Build stage ----------
# Always runs on the host's native platform, cross-compiles for the target
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /delete-job .

# ---------- Runtime stage ----------
# distroless/static is published for both amd64 and arm64
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /delete-job /delete-job

USER nonroot:nonroot

ENTRYPOINT ["/delete-job"]
