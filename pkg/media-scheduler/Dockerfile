# ====================================================================
# Media Scheduler Dockerfile
# ====================================================================
# This Dockerfile builds a container image for the media-scheduler
# with the profile configuration baked into the image.
# ====================================================================

# Build stage - compile the Go binary
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /src

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the scheduler binary
# -ldflags flags reduce binary size and remove debug info
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o /bin/media-scheduler \
    ./pkg/media-scheduler/cmd/scheduler

# ====================================================================
# Final stage - minimal runtime image
# ====================================================================
FROM gcr.io/distroless/static:nonroot

# Copy binary from builder
COPY --from=builder /bin/media-scheduler /media-scheduler

# Copy scheduler profile configuration
COPY --chown=nonroot:nonroot pkg/media-scheduler/config/media-scheduler.profile.yaml /config/media-scheduler.profile.yaml

# Switch to nonroot user
USER nonroot:nonroot

# Set default configuration path
ENV CONFIG_PATH=/config/media-scheduler.profile.yaml

# Expose metrics port (optional)
EXPOSE 10251

# Default health check port
EXPOSE 10251

# Run the scheduler with the baked-in configuration
ENTRYPOINT ["/media-scheduler"]
CMD ["--config=/config/media-scheduler.profile.yaml"]