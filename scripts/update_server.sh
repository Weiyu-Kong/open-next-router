#!/usr/bin/env bash
set -Eeuo pipefail

# Update a START.md-style root deployment without overwriting secrets by default.

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ETC_DIR="/etc/onr"
BACKUP_ROOT="/root/onr-backups"
CONFIG_FILE=""
SKIP_BUILD=0
NO_RESTART=0
SYNC_RUNTIME_CONFIG=0
SYNC_PUBLIC_CONFIG=1
RUN_TESTS=0

usage() {
  cat <<'EOF'
Usage: scripts/update_server.sh [options]

Default actions:
  validate the repository and production config
  build and atomically install bin/onr and bin/onr-admin
  sync models.yaml -> /etc/onr/models.yaml
  sync config/price.public.yaml -> /etc/onr/price.public.yaml
  restart ONR and the admin web service

Secret-bearing files are never overwritten by default:
  /etc/onr/onr.yaml and /etc/onr/keys.yaml

Options:
  --repo DIR              repository (default: script's repository)
  --config FILE           production config (default: /etc/onr/onr.yaml)
  --skip-build            do not compile or replace binaries
  --no-sync-public-config do not copy models.yaml or price.public.yaml
  --run-tests             run go test ./... before installing binaries
  --no-restart            install/sync only; do not signal or restart services
  --sync-runtime-config   overwrite /etc/onr/onr.yaml from repo/onr.yaml after backup
  -h, --help              show this help

Run as root on the server, for example:
  /home/open-next-router/scripts/update_server.sh
EOF
}

die() { echo "error: $*" >&2; exit 1; }

while (($#)); do
  case "$1" in
    --repo) [[ $# -ge 2 ]] || die "--repo requires a directory"; REPO_DIR="$2"; shift 2 ;;
    --config) [[ $# -ge 2 ]] || die "--config requires a file"; CONFIG_FILE="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --no-sync-public-config) SYNC_PUBLIC_CONFIG=0; shift ;;
    --run-tests) RUN_TESTS=1; shift ;;
    --no-restart) NO_RESTART=1; shift ;;
    --sync-runtime-config) SYNC_RUNTIME_CONFIG=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[[ "$(id -u)" == 0 ]] || die "run as root"
REPO_DIR="$(cd "$REPO_DIR" && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$ETC_DIR/onr.yaml}"
[[ -f "$CONFIG_FILE" ]] || die "production config not found: $CONFIG_FILE"
[[ -f "$REPO_DIR/models.yaml" ]] || die "repository models.yaml not found"
[[ -f "$REPO_DIR/config/price.public.yaml" ]] || die "repository public price file not found"
if [[ -x /usr/local/go/bin/go ]]; then
  export PATH="/usr/local/go/bin:$PATH"
fi

timestamp="$(date -u +%Y%m%d-%H%M%S)"
install -d -m 0700 "$BACKUP_ROOT"
backup_dir="$(mktemp -d "$BACKUP_ROOT/$timestamp-XXXXXX")"
chmod 0700 "$backup_dir"
trap 'echo "update failed; backup retained at: $backup_dir" >&2' ERR
for file in "$CONFIG_FILE" "$ETC_DIR/keys.yaml" "$ETC_DIR/models.yaml" "$ETC_DIR/price.public.yaml"; do
  if [[ -f "$file" ]]; then
    cp -a "$file" "$backup_dir/"
  fi
done
for binary in /usr/local/bin/onr /usr/local/bin/onr-admin; do
  if [[ -f "$binary" ]]; then
    cp -a "$binary" "$backup_dir/"
  fi
done
echo "backup: $backup_dir"

cd "$REPO_DIR"

if [[ "$SYNC_RUNTIME_CONFIG" == 1 ]]; then
  [[ -f "$REPO_DIR/onr.yaml" ]] || die "--sync-runtime-config requested but $REPO_DIR/onr.yaml is missing"
  echo "sync: $REPO_DIR/onr.yaml -> $CONFIG_FILE"
  install -o root -g root -m 0600 "$REPO_DIR/onr.yaml" "$CONFIG_FILE.new"
  mv -f "$CONFIG_FILE.new" "$CONFIG_FILE"
fi

if [[ "$SYNC_PUBLIC_CONFIG" == 1 ]]; then
  echo "sync: models.yaml -> $ETC_DIR/models.yaml"
  install -o root -g root -m 0644 "$REPO_DIR/models.yaml" "$ETC_DIR/models.yaml.new"
  mv -f "$ETC_DIR/models.yaml.new" "$ETC_DIR/models.yaml"
  echo "sync: config/price.public.yaml -> $ETC_DIR/price.public.yaml"
  install -o root -g root -m 0644 "$REPO_DIR/config/price.public.yaml" "$ETC_DIR/price.public.yaml.new"
  mv -f "$ETC_DIR/price.public.yaml.new" "$ETC_DIR/price.public.yaml"
fi

if [[ "$SKIP_BUILD" == 0 ]]; then
  command -v go >/dev/null 2>&1 || die "go is required to build (or use --skip-build)"
  if [[ "$RUN_TESTS" == 1 ]]; then
    echo "test: go test ./..."
    GOCACHE="${GOCACHE:-/tmp/onr-go-build-cache}" GOMODCACHE="${GOMODCACHE:-/tmp/onr-go-mod-cache}" go test ./...
  fi
  echo "build: make build"
  GOCACHE="${GOCACHE:-/tmp/onr-go-build-cache}" GOMODCACHE="${GOMODCACHE:-/tmp/onr-go-mod-cache}" make build
  "$REPO_DIR/bin/onr-admin" validate all --config "$CONFIG_FILE"
  "$REPO_DIR/bin/onr" -t -c "$CONFIG_FILE"
  install -o root -g root -m 0755 "$REPO_DIR/bin/onr" /usr/local/bin/onr.new
  install -o root -g root -m 0755 "$REPO_DIR/bin/onr-admin" /usr/local/bin/onr-admin.new
  mv -f /usr/local/bin/onr.new /usr/local/bin/onr
  mv -f /usr/local/bin/onr-admin.new /usr/local/bin/onr-admin
else
  /usr/local/bin/onr-admin validate all --config "$CONFIG_FILE"
  /usr/local/bin/onr -t -c "$CONFIG_FILE"
fi

if [[ "$NO_RESTART" == 0 ]]; then
  systemctl daemon-reload
  if [[ "$SKIP_BUILD" == 0 || "$SYNC_RUNTIME_CONFIG" == 1 ]]; then
    systemctl restart onr.service
    gateway_action="restarted"
  else
    systemctl reload onr.service
    gateway_action="reloaded"
  fi
  systemctl restart onr-admin-web.service
  systemctl is-active --quiet onr.service || die "onr.service is not active"
  systemctl is-active --quiet onr-admin-web.service || die "onr-admin-web.service is not active"
  echo "services: onr $gateway_action; onr-admin-web restarted"
else
  echo "services: not restarted (--no-restart)"
fi

echo "update complete"
echo "backup retained at: $backup_dir"
trap - ERR
