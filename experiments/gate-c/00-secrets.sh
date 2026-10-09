#!/usr/bin/env bash
set -euo pipefail

OP_VERSION=2.40.0
OP_SHA256=74277219e8da60958c00f9aee9d2023225e98fdda8bfd2156a5d9e85e0edaab3

here="$(cd "$(dirname "$0")" && pwd)"
work="${WORK:-/var/tmp/boxface-gate-c}"
out="$work/out"
token_file="${OP_TOKEN_FILE:-/etc/boxface/op-service-account-token}"
mkdir -p "$work" "$out"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

apt-get update
apt-get install -y curl unzip socat

zip="op_linux_amd64_v${OP_VERSION}.zip"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$zip" "https://cache.agilebits.com/dist/1P/op2/pkg/v${OP_VERSION}/${zip}"
echo "${OP_SHA256}  $tmp/$zip" | sha256sum -c -
unzip -q -o "$tmp/$zip" op -d "$tmp"
install -m 0755 "$tmp/op" /usr/local/bin/op

[ -f "$token_file" ] || { echo "write the service-account token to $token_file first" >&2; exit 1; }
[ "$(stat -c '%u %a' "$token_file")" = "0 600" ] || { echo "$token_file must be owned by root with mode 0600" >&2; exit 1; }
case "$token_file" in "$here"/*|"$work"/*) echo "$token_file must be outside the repository and work directory" >&2; exit 1 ;; esac

export OP_SERVICE_ACCOUNT_TOKEN="$(cat "$token_file")"
op whoami > /dev/null
failed=0
while read -r id var ref; do
  case "$id" in ''|'#'*|missing) continue ;; esac
  if op read --no-newline "$ref" > /dev/null 2>&1; then echo "binding $id resolves"; else echo "binding $id does not resolve" >&2; failed=1; fi
done < "$here/bindings"
[ "$failed" -eq 0 ]

{
  echo "op: $(op --version)"
  echo "vaults: $(op vault list --format json | grep -o '"name": *"[^"]*"' | cut -d'"' -f4 | tr '\n' ' ')"
} | tee "$out/secrets-info.txt"
