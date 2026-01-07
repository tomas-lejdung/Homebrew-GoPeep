#!/bin/bash
set -e

# Docker Hub username
DOCKER_USER="${DOCKER_USER:-tomaslejdung}"
IMAGE_NAME="gopeep-server"

# Check if version is provided
if [ -z "$1" ]; then
    echo "Usage: $0 <version>"
    echo "Example: $0 1.0.0"
    exit 1
fi

VERSION=$1

echo "Building and publishing ${DOCKER_USER}/${IMAGE_NAME}:${VERSION}"

# Build and push for linux/amd64 (Unraid typically runs on amd64)
docker buildx build \
    --platform linux/amd64 \
    -t ${DOCKER_USER}/${IMAGE_NAME}:${VERSION} \
    -t ${DOCKER_USER}/${IMAGE_NAME}:latest \
    --push \
    .

echo ""
echo "Successfully published:"
echo "  ${DOCKER_USER}/${IMAGE_NAME}:${VERSION}"
echo "  ${DOCKER_USER}/${IMAGE_NAME}:latest"
echo ""
echo "To run on Unraid:"
echo "  docker run -d -p 8080:8080 ${DOCKER_USER}/${IMAGE_NAME}:${VERSION}"
