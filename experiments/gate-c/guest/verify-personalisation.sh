#!/usr/bin/env bash
set -uo pipefail

mode="${1:-}"
case "$mode" in first|restart) ;; *) echo "usage: $0 first|restart" >&2; exit 2 ;; esac

state="$HOME/.local/state/boxface/personalisation"
failed=0
check() {
  local name="$1"; shift
  if "$@" > /dev/null 2>&1; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}

for _ in $(seq 1 30); do [ -f "$state" ] && break; sleep 1; done

check "chezmoi resolves to the profile" test "$(command -v chezmoi)" = /opt/boxface/profile/bin/chezmoi
check "dotfiles source is read-only" sh -c 'touch /usr/share/boxface/dotfiles/.probe 2>&1 | grep -q "Read-only file system"'
check "personalisation state recorded" test -f "$state"
check "state names the image's dotfiles revision" grep -qx "revision: $(cat /usr/share/boxface/dotfiles-revision)" "$state"
for f in .gitconfig .bashrc .claude/CLAUDE.md; do
  check "$f is managed" grep -qx "  $f" "$state"
done
check "agent state is not managed" sh -c "! grep -qE '^  \\.claude(\\.json|/projects|/\\.credentials\\.json)' '$state'"
check "~/.claude is private" test "$(stat -c %a "$HOME/.claude")" = 700

if [ "$mode" = first ]; then
  check "personalisation ran this boot" test "$(systemctl --user show -p ConditionResult --value boxface-personalise.service)" = yes
  check "managed files match the source" chezmoi verify
else
  check "personalisation skipped this boot" test "$(systemctl --user show -p ConditionResult --value boxface-personalise.service)" = no
  check "user edit to CLAUDE.md survived" grep -qx 'Gate C user edit' "$HOME/.claude/CLAUDE.md"
  check "~/.claude.json survived" test -s "$HOME/.claude.json"
  check "Claude Code project history survived" sh -c 'test -n "$(ls -A "$HOME/.claude/projects" 2>/dev/null)"'
fi

echo
echo "personalisation state:"
sed 's/^/  /' "$state" 2>/dev/null
echo "chezmoi status:"
chezmoi status 2>&1 | sed 's/^/  /'

exit "$failed"
