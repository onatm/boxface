#!/usr/bin/env bash
set -uo pipefail

tools="claude codex opencode pi mise node npm rustc cargo cc git"
failed=0
check() {
  local name="$1"; shift
  if "$@" > /dev/null 2>&1; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}

check "/opt/boxface/profile is a root-owned symlink" sh -c 'test -L /opt/boxface/profile && test "$(stat -c %u /opt/boxface/profile)" = 0'
check "profile resolves into /nix/store" sh -c 'readlink -f /opt/boxface/profile | grep -q "^/nix/store/"'
check "write to /nix/store fails with EROFS" sh -c 'touch /nix/store/.boxface-probe 2>&1 | grep -q "Read-only file system"'
check "write to /opt/boxface fails with EROFS" sh -c 'touch /opt/boxface/.boxface-probe 2>&1 | grep -q "Read-only file system"'
check "profile link cannot be replaced" sh -c '! ln -sfn /tmp /opt/boxface/profile'
check "no Nix daemon socket" test ! -e /nix/var/nix/daemon-socket/socket
check "nix not on PATH" sh -c '! command -v nix'
check "home-manager not on PATH" sh -c '! command -v home-manager'
check "no Nix or Home Manager state in /home/dev" sh -c \
  'for p in .nix-profile .nix-defexpr .local/state/nix .local/state/home-manager .config/home-manager; do test ! -e "$HOME/$p" || exit 1; done'

check "LANG is C.UTF-8" test "${LANG:-}" = C.UTF-8
check "client LC_ALL not accepted" test -z "${LC_ALL:-}"
check "no setlocale warnings" sh -c '! { bash -c true; cargo --version; pi --version; } 2>&1 | grep -q setlocale'

for t in $tools; do
  check "$t resolves to the profile (non-interactive)" test "$(command -v "$t")" = "/opt/boxface/profile/bin/$t"
  check "$t resolves to the profile (login shell)" test "$(bash -lc "command -v $t")" = "/opt/boxface/profile/bin/$t"
  check "$t starts" "$t" --version
done

if [ "${OFFLINE:-0}" = 1 ]; then
  check "network unreachable" sh -c '! curl -fsS -m 5 -o /dev/null https://deb.debian.org'
fi

echo
echo "versions:"
for t in $tools; do echo "  $t: $("$t" --version 2>&1 | head -1)"; done
echo "agent launchers:"
for t in claude codex opencode pi; do
  launcher="$(readlink -f "$(command -v "$t")")"
  echo "  $t: $launcher $(head -1 "$launcher" | grep -a '^#!' || echo '(binary)')"
done
echo "/nix/store: $(du -sh /nix/store 2>/dev/null | cut -f1)"
echo "memory:"
free -m | sed 's/^/  /'

exit "$failed"
