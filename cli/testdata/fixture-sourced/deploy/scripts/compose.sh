#!/usr/bin/env bash
# Runs docker compose with the production host's files: compose.sh ps, compose.sh logs api.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
source "$ROOT/deploy/scripts/lib/compose-files.sh"
shop_compose_files
exec docker compose --env-file "$ROOT/.env.production" "${SHOP_COMPOSE_FILES[@]}" "$@"
