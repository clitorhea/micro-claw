# Stage 1: Build the static binary
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency files and download
COPY go.mod ./
RUN go mod download || true

# Copy source code
COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o nas-watchdog ./cmd/nas-watchdog

# Stage 2: Minimal runtime image
FROM alpine:latest

# Install Docker CLI and ZFS utilities so os/exec commands work
RUN apk add --no-cache docker-cli zfs ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/nas-watchdog .

# Run the watchdog
ENTRYPOINT ["./nas-watchdog"]
