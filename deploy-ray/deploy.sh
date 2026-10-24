#!/usr/bin/env bash
set -Eeuo pipefail

DEPLOY_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$DEPLOY_DIR/.." && pwd)
ENV_FILE="$DEPLOY_DIR/.env"
WAIT_TIMEOUT=${RAY_WAIT_TIMEOUT:-600}

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }

usage() {
  cat <<'USAGE'
Usage: bash deploy.sh COMMAND [TARGET]
  init                      Create .env with random secrets (never overwrite)
  build [master|salve|all]   Build the current checkout; default: all
  infra-up                  Start/wait for shared PostgreSQL + Redis
  up [master|salve|all]      Start/update an app; all also starts infrastructure
  stop master|salve|all     Stop apps only; retain infrastructure and volumes
  restart master|salve      Restart only the existing selected app
  logs master|salve|infra   Follow selected logs
  status                    Show the three Compose projects
  check                     Validate all Compose definitions without printing secrets
  backup                    Save a PostgreSQL custom-format dump in backups/

Individual up/restart commands require healthy infrastructure and never modify it.
RAY_WAIT_TIMEOUT=1200 bash deploy.sh up master  # extend startup wait if necessary
USAGE
}

init_env() {
  if [[ -e "$ENV_FILE" ]]; then
    printf '.env already exists; kept unchanged.\n'
    return
  fi
  need openssl
  umask 077
  local temp postgres_password redis_password jwt_secret totp_key admin_password
  postgres_password=$(openssl rand -hex 32)
  redis_password=$(openssl rand -hex 32)
  jwt_secret=$(openssl rand -hex 32)
  totp_key=$(openssl rand -hex 32)
  admin_password=$(openssl rand -hex 24)
  temp=$(mktemp "$DEPLOY_DIR/.env.tmp.XXXXXX")
  # The hard link below refuses to overwrite a concurrently created .env.
  trap 'rm -f -- "$temp"' EXIT
  sed \
    -e "s/__GENERATE_POSTGRES__/$postgres_password/g" \
    -e "s/__GENERATE_REDIS__/$redis_password/g" \
    -e "s/__GENERATE_JWT__/$jwt_secret/g" \
    -e "s/__GENERATE_TOTP__/$totp_key/g" \
    -e "s/__GENERATE_ADMIN__/$admin_password/g" \
    "$DEPLOY_DIR/.env.example" > "$temp"
  ln -- "$temp" "$ENV_FILE" || die '.env was created by another process; not overwritten'
  rm -f -- "$temp"
  trap - EXIT
  printf 'Created %s (mode 600). Edit ADMIN_EMAIL; ADMIN_PASSWORD is stored there.\n' "$ENV_FILE"
}

infra() {
  docker compose --project-name sub2api-ray-infra --env-file "$ENV_FILE" \
    -f "$DEPLOY_DIR/compose.infra.yml" "$@"
}

app() {
  local role=$1
  shift
  docker compose --project-name "sub2api-ray-$role" --env-file "$ENV_FILE" \
    -f "$DEPLOY_DIR/compose.app.yml" "$@"
}

valid_role() { [[ "$1" == master || "$1" == salve ]] || die "Target must be master or salve: $1"; }
valid_apps() { [[ "$1" == all ]] || valid_role "$1"; }

require_infra() {
  local service id state
  for service in ray-postgres ray-redis; do
    id=$(infra ps -q "$service")
    [[ -n "$id" ]] || die "$service is not running. Run: bash deploy.sh infra-up"
    state=$(docker inspect --format '{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id")
    [[ "$state" == 'true healthy' ]] || die "$service is not healthy ($state). Check: bash deploy.sh logs infra"
  done
}

image_for() { app "$1" config --images "$1"; }

up_app() {
  local role=$1 image
  valid_role "$role"
  require_infra
  image=$(image_for "$role")
  docker image inspect "$image" >/dev/null 2>&1 || die "Missing image $image. Run: bash deploy.sh build $role"
  app "$role" up -d --no-deps --no-build --pull never --wait --wait-timeout "$WAIT_TIMEOUT" "$role"
}

build_apps() {
  local target=$1 master_image salve_image
  valid_apps "$target"
  [[ -f "$REPO_DIR/Dockerfile" ]] || die 'Keep deploy-ray inside the complete source checkout'
  export RAY_GIT_COMMIT RAY_BUILD_DATE
  RAY_GIT_COMMIT=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || printf 'ray-local')
  RAY_BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  if [[ "$target" == all ]]; then
    app master build master
    master_image=$(image_for master)
    salve_image=$(image_for salve)
    if [[ "$master_image" != "$salve_image" ]]; then
      docker image tag "$master_image" "$salve_image"
    fi
  else
    app "$target" build "$target"
  fi
}

backup_db() {
  require_infra
  umask 077
  mkdir -p -- "$DEPLOY_DIR/backups"
  local temp destination
  temp=$(mktemp "$DEPLOY_DIR/backups/.partial.XXXXXX")
  destination="$DEPLOY_DIR/backups/sub2api-$(date -u +%Y%m%dT%H%M%SZ)-${temp##*.}.dump"
  if infra exec -T ray-postgres sh -c 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$temp"; then
    [[ -s "$temp" ]] || { rm -f -- "$temp"; die 'Empty database backup'; }
    mv -- "$temp" "$destination"
    printf 'Database backup: %s\n' "$destination"
  else
    rm -f -- "$temp"
    die 'Database backup failed'
  fi
}

command=${1:-help}
target=${2:-}
[[ $# -le 2 ]] || die 'Too many arguments'
case "$command" in
  help|-h|--help) usage; exit 0 ;;
  init) [[ -z "$target" ]] || die 'init takes no target'; init_env; exit 0 ;;
  build|up) valid_apps "${target:-all}" ;;
  stop) valid_apps "${target:-invalid}" ;;
  restart) valid_role "${target:-invalid}" ;;
  logs) [[ "$target" == infra ]] || valid_role "${target:-invalid}" ;;
  infra-up|status|check|backup) [[ -z "$target" ]] || die "$command takes no target" ;;
  *) usage >&2; die "Unknown command: $command" ;;
esac

[[ -f "$ENV_FILE" ]] || die 'Missing .env. Run: bash deploy.sh init'
if grep -q '__GENERATE_' "$ENV_FILE"; then die '.env still contains placeholders; generate real secret values first'; fi
[[ "$WAIT_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die 'RAY_WAIT_TIMEOUT must be a positive integer'
need docker
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2.20+ is required'
# .env is parsed only by Compose. Never source it as shell code.
case "$command" in
  check)
    infra config --quiet
    app master config --quiet master
    app salve config --quiet salve
    printf 'Compose configuration is valid.\n'
    ;;
  build) build_apps "${target:-all}" ;;
  infra-up) infra up -d --wait --wait-timeout "$WAIT_TIMEOUT" ;;
  up)
    if [[ "${target:-all}" == all ]]; then
      infra up -d --wait --wait-timeout "$WAIT_TIMEOUT"
      up_app master
      up_app salve
    else up_app "$target"; fi
    ;;
  stop)
    if [[ "$target" == all ]]; then app master stop master; app salve stop salve
    else app "$target" stop "$target"; fi
    ;;
  restart) require_infra; app "$target" restart "$target" ;;
  logs)
    if [[ "$target" == infra ]]; then infra logs -f --tail 100
    else app "$target" logs -f --tail 100 "$target"; fi
    ;;
  status)
    infra ps
    app master ps master
    app salve ps salve
    ;;
  backup) backup_db ;;
esac
