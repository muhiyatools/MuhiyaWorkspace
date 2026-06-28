# Build stage
FROM golang:1.22-bookworm AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the Go app (CGO is not strictly needed for pq, but we can set CGO_ENABLED=0 to build a static binary)
ENV CGO_ENABLED=0
RUN go build -o muhiyallm ./main.go

# Final stage
FROM debian:bookworm-slim

WORKDIR /app

# Install CA certificates for HTTPS requests + curl for health checks
RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*

# Copy the binary and static files from the builder
COPY --from=builder /app/muhiyallm .
COPY --from=builder /app/static ./static

# Expose the port your app runs on
EXPOSE 8090

# Health check — verifies DB is connected and app is serving
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -f http://localhost:8090/health || exit 1

# Run the executable
CMD ["./muhiyallm"]
