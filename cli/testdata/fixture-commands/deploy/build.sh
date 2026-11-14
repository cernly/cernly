#!/usr/bin/env bash
set -euo pipefail
tag="${1:-dev}"
docker build --platform linux/amd64 -f backend/Dockerfile --target api \
	-t "${REGISTRY}/acme/api:${tag}" .
docker build -f backend/Dockerfile --target worker -t "${REGISTRY}/acme/worker:${tag}" .
