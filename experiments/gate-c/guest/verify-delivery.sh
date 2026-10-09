#!/usr/bin/env bash
set -uo pipefail

failed=0
check() {
  local name="$1"; shift
  if "$@" > /dev/null 2>&1; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}
lacks() { ! grep -q "$1" <<< "$output"; }
expect_failure() {
  local name="$1" message="$2"; shift 2
  local marker="/tmp/gate-c-started-$RANDOM" output
  output="$(boxface-run "$@" -- touch "$marker" 2>&1)"
  local status=$?
  echo "      $output"
  check "$name: launch fails" test "$status" -ne 0
  check "$name: command not started" test ! -e "$marker"
  check "$name: error names the binding and reason" grep -qF "$message" <<< "$output"
}

if [ "${1:-}" = broker-down ]; then
  expect_failure "broker stopped" "binding 'dummy': credential broker unreachable" dummy
  exit "$failed"
fi

scan='
  pattern() { printf "%s\n" "$GATE_C_DUMMY"; }
  echo "sha256: $(printf "%s" "$GATE_C_DUMMY" | sha256sum | cut -d" " -f1)"
  for f in /proc/[0-9]*/cmdline; do tr "\0" " " < "$f" 2>/dev/null; echo; done | grep -qFf <(pattern) && echo "value on a command line"
  grep -rlD skip -Ff <(pattern) /home /workspace /tmp /run 2>/dev/null | sed "s/^/value in file: /"
  true
'
output="$(boxface-run dummy -- bash -c "$scan" 2>&1)"
status=$?
echo "$output" | sed 's/^/      /'
check "dummy binding delivered" test "$status" -eq 0
check "delivered value hashed" grep -q '^sha256: [0-9a-f]\{64\}$' <<< "$output"
check "value on no process command line" lacks 'value on a command line'
check "value in no guest file" lacks 'value in file'
check "variable absent from the login shell" sh -c '! bash -lc "env" | grep -q "^GATE_C_DUMMY="'

expect_failure "unapproved binding" "binding 'not-approved': binding not approved for this environment" not-approved
expect_failure "invalid binding id" "binding '../x': invalid binding id" ../x
expect_failure "missing 1Password item" "binding 'missing': 1Password lookup failed" missing

exit "$failed"
