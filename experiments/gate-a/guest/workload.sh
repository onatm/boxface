#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/workload"
marker_file="$HOME/.gate-a-marker"
psql() { docker compose exec -T db psql -U postgres -tAc "$1"; }

up() {
  local started=$(date +%s)
  docker compose up -d --build --wait
  echo "compose up seconds: $(( $(date +%s) - started ))"
  curl -fsS http://127.0.0.1:8080/ | grep -qx 'boxface gate a'
  echo "web reachable on published port"
}

case "${1:-}" in
  write)
    up
    marker="$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
    psql "CREATE TABLE IF NOT EXISTS gate_a (marker text)"
    psql "INSERT INTO gate_a VALUES ('$marker')"
    echo "$marker" > "$marker_file"
    echo "wrote marker $marker"
    ;;
  check)
    up
    marker="$(cat "$marker_file")"
    [ "$(psql "SELECT count(*) FROM gate_a WHERE marker = '$marker'")" = 1 ] || { echo "marker $marker missing" >&2; exit 1; }
    echo "marker $marker survived"
    ;;
  *) echo "usage: $0 write|check" >&2; exit 2 ;;
esac

volume="$(docker volume inspect --format '{{.Mountpoint}}' workload_db)"
owner="$(stat -c '%u:%g' "$volume")"
echo "db volume: $volume"
echo "db volume owner on disk: $owner (container uid 70 maps to 100069 under dev:100000:65536)"
[ "$owner" = 100069:100069 ]
