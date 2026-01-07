.PHONY: build build-release clean run test list serve help sign

# Binary name
BINARY := gopeep

# Build flags
CGO_FLAGS := CGO_ENABLED=1
LDFLAGS_RELEASE := -ldflags="-s -w"

# Default target
all: build

# Build for development
build:
	$(CGO_FLAGS) go build -o $(BINARY) .
	@echo "Signing binary..."
	codesign --sign - --force $(BINARY)

# Build optimized release binary
build-release:
	$(CGO_FLAGS) go build $(LDFLAGS_RELEASE) -o $(BINARY) .
	@echo "Signing binary..."
	codesign --sign - --force $(BINARY)

# Sign existing binary (ad-hoc signature for macOS Gatekeeper)
sign:
	codesign --sign - --force $(BINARY)

# Clean build artifacts
clean:
	rm -f $(BINARY)
	go clean

# Run in share mode (default)
run: build
	./$(BINARY)

# Run with window list
list: build
	./$(BINARY) --list

# Run in server-only mode
serve: build
	./$(BINARY) --serve

# Run tests
test:
	go test -v ./...

# Format code
fmt:
	go fmt ./...

# Run linter (requires golangci-lint)
lint:
	golangci-lint run

# Install to GOPATH/bin
install:
	$(CGO_FLAGS) go install .

# Help
help:
	@echo "GoPeep Makefile targets:"
	@echo ""
	@echo "  build         Build and sign the binary"
	@echo "  build-release Build optimized release binary (signed)"
	@echo "  sign          Sign existing binary (ad-hoc for macOS)"
	@echo "  clean         Remove build artifacts"
	@echo "  run           Build and run in share mode"
	@echo "  list          Build and list available windows"
	@echo "  serve         Build and run signal server only"
	@echo "  test          Run tests"
	@echo "  fmt           Format code"
	@echo "  lint          Run linter"
	@echo "  install       Install to GOPATH/bin"
	@echo "  help          Show this help"
