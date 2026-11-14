# Compose files of the production host, in -f order.
shop_pitr_enabled() { [ -f /etc/shop/pitr.enabled ]; }

shop_compose_files() {
  SHOP_COMPOSE_FILES=( -f docker-compose.prod.yml -f deploy/docker-compose.vps.yml -f deploy/docker-compose.edge.vps.yml )
  shop_pitr_enabled || return 0
  SHOP_COMPOSE_FILES+=(-f deploy/docker-compose.pitr.yml)
}
