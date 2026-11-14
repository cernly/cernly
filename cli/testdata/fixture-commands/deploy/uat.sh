#!/usr/bin/env bash
FILES=(
  -f docker-compose.prod.yml
  -f deploy/docker-compose.vps.yml
)
compose() {
  docker compose --env-file uat.env "${FILES[@]}" "$@"
}
compose pull
compose up -d
