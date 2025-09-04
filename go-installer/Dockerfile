# Multi-stage build for OpenShift SNO Hub Installer

# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN make build

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    curl \
    openssh-client \
    sshpass \
    jq \
    && rm -rf /var/cache/apk/*

# Create non-root user
RUN adduser -D -s /bin/sh appuser

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/build/openshift-sno-hub-installer /usr/local/bin/

# Copy configuration template
COPY --from=builder /app/idrac_config.yaml /app/

# Create necessary directories
RUN mkdir -p /app/logs /app/workdir /app/abi-master-0

# Set permissions
RUN chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

# Expose port (if needed for web interface)
EXPOSE 8080

# Set default command
CMD ["openshift-sno-hub-installer", "help"]