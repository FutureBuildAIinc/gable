# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY core/go.mod core/go.sum ./
RUN go mod download

# Copy the source code
COPY core/ .

# Build the one binary. Every role is a subcommand of it: `core serve` is
# the image's default command, and `core migrate` / `core seed` are what
# DO App Platform post-deploy jobs invoke against the production image
# without needing a separate build step.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o core ./cmd/core

# Run stage
FROM alpine:3.20

RUN apk --no-cache add ca-certificates curl && \
    addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

# Copy the pre-built binary from the builder stage.
COPY --from=builder /app/core ./core

# Copy migrations for runtime use
COPY --from=builder /app/migrations ./migrations

# Create writable directories for file uploads and EDI exports
RUN mkdir -p /app/uploads /app/edi_out && \
    chown -R appuser:appgroup /app/uploads /app/edi_out

# Run as non-root user
USER appuser

# Expose the port the app runs on
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/healthz/live || exit 1

# Command to run the executable
CMD ["./core", "serve"]
