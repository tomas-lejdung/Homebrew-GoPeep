# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy module files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY *.go ./
COPY pkg/ ./pkg/
COPY cmd/ ./cmd/

# Build static binary for the signal server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gopeep-server ./cmd/server

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
