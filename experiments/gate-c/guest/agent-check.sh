#!/usr/bin/env bash
set -uo pipefail

failed=0
check() {
  local name="$1"; shift
  if "$@" > /dev/null 2>&1; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}
prompt='Reply with the single word OK.'
answered() { [ "$status" -eq 0 ] && grep -qiw OK <<< "$output"; }
lacks() { ! grep -q "$1" <<< "$output"; }

case "${1:-}" in
  token)
    home="$(mktemp -d /tmp/gate-c-home.XXXXXX)"
    scan='
      claude -p "$1" > "$HOME/reply.txt" 2>&1; status=$?
      cat "$HOME/reply.txt"
      grep -rlD skip -Ff <(printf "%s\n" "$CLAUDE_CODE_OAUTH_TOKEN") /home /workspace "$HOME" 2>/dev/null | sed "s/^/token in file: /"
      exit $status
    '
    output="$(HOME="$home" boxface-run claude-token -- bash -c "$scan" _ "$prompt" 2>&1)"
    status=$?
    echo "$output" | sed 's/^/      /'
    check "claude answers with the broker-delivered setup token" answered
    check "no login credentials file written" test ! -e "$home/.claude/.credentials.json"
    check "setup token in no guest file" lacks 'token in file'
    rm -rf "$home"
    ;;
  login)
    credentials="$HOME/.claude/.credentials.json"
    check "login credentials stored" test -s "$credentials"
    check "credentials on the data disk" test "$(findmnt -no SOURCE --target "$credentials")" = "$(findmnt -no SOURCE --target /home/dev)"
    check "credentials mode 600" test "$(stat -c %a "$credentials")" = 600
    check "setup token not in the environment" test -z "${CLAUDE_CODE_OAUTH_TOKEN:-}"
    output="$(claude -p "$prompt" 2>&1)"
    status=$?
    echo "      $output"
    check "claude answers using the stored login" answered
    echo "credentials: $credentials ($(stat -c '%a %U:%G %s bytes' "$credentials"))"
    ;;
  *) echo "usage: $0 token|login" >&2; exit 2 ;;
esac

exit "$failed"
