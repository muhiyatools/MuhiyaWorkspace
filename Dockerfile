# Build stage
# NOTE: pin these tags to a digest (golang:1.22-bookworm@sha256:...) once you
# have a registry to `docker pull` from and capture the current digest with
# `docker inspect --format='{{index .RepoDigests 0}}'` - it can't be guessed
# offline, but the major.minor + Debian codename below at least avoids
# `latest`'s unbounded drift.
FROM golang:1.22-bookworm AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the Go app (CGO is not strictly needed for pq, but we can set
# CGO_ENABLED=0 to build a static binary). Building "." rather than
# "./main.go" compiles the whole package main, not just one file - safe now
# and won't silently drop a second file added to package main later.
ENV CGO_ENABLED=0
RUN go build -o muhiyallm .

# Final stage
FROM debian:bookworm-slim

WORKDIR /app

# Install CA certificates for HTTPS requests + curl for health checks
RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*

# Run as a non-root user: a container escape or RCE in a root-owned process
# has host-level blast radius it doesn't need here.
RUN groupadd -r muhiyallm && useradd -r -g muhiyallm -d /app muhiyallm

# Copy the binary and static files from the builder
COPY --from=builder /app/muhiyallm .
COPY --from=builder /app/static ./static

RUN chown -R muhiyallm:muhiyallm /app
USER muhiyallm

# Expose the port your app runs on
EXPOSE 8090

# Health check — verifies DB is connected and app is serving
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -f http://localhost:8090/health || exit 1

# Run the executable
CMD ["./muhiyallm"]
