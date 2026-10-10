#!/usr/bin/env bash
set -euo pipefail

bin="${1:?usage: $0 <directory with boxface-api and boxface-worker>}"
here="$(cd "$(dirname "$0")" && pwd)"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
[ -x /usr/local/bin/firecracker ] || { echo "install Firecracker at /usr/local/bin/firecracker first" >&2; exit 1; }

id boxface > /dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin boxface
id boxface-vm > /dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin boxface-vm

first=0
[ -e /var/lib/boxface/api/boxface.db ] || first=1

install -d -o root -g root -m 0755 /var/lib/boxface
install -d -o boxface -g boxface -m 0700 /var/lib/boxface/api
install -d -o boxface -g boxface -m 0755 /var/lib/boxface/import /var/lib/boxface/images

install -m 0755 "$bin/boxface-api" "$bin/boxface-worker" /usr/local/bin/
install -m 0644 "$here/boxface-worker.service" "$here/boxface-api.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable boxface-worker.service boxface-api.service
systemctl restart boxface-worker.service boxface-api.service

echo "installed $(/usr/local/bin/boxface-api -version)"
if [ "$first" -eq 1 ]; then
  owner="${BOXFACE_OWNER:-${SUDO_USER:-owner}}"
  code="$(runuser -u boxface -- /usr/local/bin/boxface-api bootstrap -owner "$owner" | tail -1)"
  echo
  echo "Finish setup in a browser within 15 minutes, signed in as $owner:"
  echo "  http://127.0.0.1:7443/login?code=$code"
  echo "From another machine, tunnel first (ssh -L 7443:127.0.0.1:7443 root@<host>), or use your"
  echo "https address once the API is exposed: https://<address>/login?code=$code"
else
  echo "create a login code with: runuser -u boxface -- boxface-api bootstrap -owner <name>"
fi
