#!/usr/bin/env bash
set -euo pipefail
db_dump() { compose exec -T db pg_dump -U shop shop; }
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENV_FILE="$ROOT/.env.production"
[ -f "$ENV_FILE" ] || exit 1
source "$ROOT/deploy/scripts/lib/compose-files.sh"
shop_compose_files
COMPOSE_FILES=("${SHOP_COMPOSE_FILES[@]}")

compose() { docker compose --env-file "$ENV_FILE" "${COMPOSE_FILES[@]}" "$@"; }
compose_up() { compose up -d --remove-orphans --no-build --pull never "$@"; }

cd "$ROOT"
if shop_pitr_enabled; then compose pull db; fi
case "${1:-up}" in
  dump) db_dump ;;
  *) compose_up ;;
esac
compose ps
