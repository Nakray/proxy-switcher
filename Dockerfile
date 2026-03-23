# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata gcc musl-dev

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o /app/proxy-switcher ./cmd/

# Final stage
FROM alpine:3.19

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /app/proxy-switcher /app/proxy-switcher

# Copy default config
COPY configs/config.yaml /app/config.yaml

# Create data directory for SQLite database
RUN mkdir -p /app/data

# Create non-root user
RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -s /bin/sh -D appuser

# Change ownership
RUN chown -R appuser:appgroup /app
USER appuser

# Expose proxy ports
EXPOSE 1080 2080 9090

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:9090/health || exit 1

# Run the application
ENTRYPOINT ["/app/proxy-switcher"]
CMD ["-config", "/app/config.yaml"]
