#!/bin/bash
# GoPeep Installer for macOS
# Usage: curl -fsSL https://raw.githubusercontent.com/YOUR_REPO/main/install.sh | bash

set -e

BINARY_URL="https://github.com/YOUR_REPO/releases/latest/download/gopeep"
INSTALL_DIR="/usr/local/bin"

echo "Installing GoPeep..."

# Download binary
curl -fsSL "$BINARY_URL" -o /tmp/gopeep

# Remove quarantine attribute (bypasses Gatekeeper)
xattr -d com.apple.quarantine /tmp/gopeep 2>/dev/null || true

# Make executable
chmod +x /tmp/gopeep

# Move to install directory (may need sudo)
if [ -w "$INSTALL_DIR" ]; then
    mv /tmp/gopeep "$INSTALL_DIR/gopeep"
else
    echo "Need sudo to install to $INSTALL_DIR"
    sudo mv /tmp/gopeep "$INSTALL_DIR/gopeep"
fi

echo ""
echo "GoPeep installed successfully!"
echo "Run 'gopeep --help' to get started."
