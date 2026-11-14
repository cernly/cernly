#!/usr/bin/env bash
# Checks that the PITR overlay validates: compose_files_test.sh --pitr
set -euo pipefail
source "$(dirname "$0")/lib/compose-files.sh"
[ "${1:-}" = "--pitr" ] && shop_compose_files
docker compose "${SHOP_COMPOSE_FILES[@]}" config --quiet
