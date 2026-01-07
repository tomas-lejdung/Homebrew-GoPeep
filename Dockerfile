# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy server module files (separate from main project)
COPY cmd/server/go.mod cmd/server/go.sum ./
RUN go mod download

# Copy source code
COPY cmd/server/*.go ./
COPY cmd/server/viewer.html ./

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gopeep-server .

# Runtime stage
FROM alpine:3.20

# Add ca-certificates for HTTPS if needed
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/gopeep-server .

# Expose port
EXPOSE 8080

# Run
ENTRYPOINT ["./gopeep-server"]
CMD ["--port", "8080"]
