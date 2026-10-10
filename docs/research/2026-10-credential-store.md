# Agent Credential Store: Acceptance

**Date:** 2026-10-10

**Result:** Pass for K1–K5 on the droplet; K6 and K7 by automated tests

**Plan:** [Agent Credential Store](../plans/2026-10-secret-store.md#acceptance-scenarios), S1–S5

---

## Summary

A Claude Code subscription token saved once in the web UI let Claude Code run in two environments that never signed in, interactively through `connect` and headless, with no credentials file written and no onboarding screens. Replacing a value reached the next launch without a restart, deleting it took effect at the next launch, and after a worker restart the next launch waited for the API's reconcile push and then succeeded. A value was found only in the environment of the process it was delivered to: not in any file on the host or in the guest, not in logs, not on any command line, and not in any other process's environment. The audit log records names, never values.

## Environment

| Item | Value |
|---|---|
| Host | The droplet, Tailscale exposure, services from commit `d4214d7` |
| Image | Gate C profile with the agent wrappers and `boxface-run` (static Go, 2.1 MB) |
| Environments | `cred` and `cred2`, 2 vCPU, 2048 MiB, created from the new image, never signed in to any agent |
| Client | macOS `boxfacectl`; web UI in headless Chromium and the owner's browser |

## Results

| ID | Scenario | Result |
|---|---|---|
| K1 | Owner saved a `claude setup-token` token under the Claude Code preset; Claude Code in `cred` and a newly created `cred2` | Pass: `claude -p` answered in both; interactive `claude` in a `connect` session went straight to its per-folder trust question, with no sign-in or theme screens; `~/.claude/.credentials.json` absent; the wrapper set `hasCompletedOnboarding` |
| K2 | Replace a value while the environment runs | Pass (throwaway value bound to `codex`): the next launch received the new value |
| K3 | Delete the secret | Pass: the next launch ran with the variable unset |
| K4 | Leak scan with the value delivered and a launched process holding it | Pass: the value was in the launched process's environment only; absent from every file under `/var/lib/boxface` (database, data disks, runtime state), `/run`, `/tmp`, and the image output on the host, from the API, worker, and VM journals, from all host and guest command lines, from every other host process environment, and from every guest file under `/run`, `/tmp`, the home, `/workspace`, the data mount, and `/var`. The SQLite database, WAL, and shared-memory files contain only ciphertext |
| K5 | Restart the worker while an environment runs | Pass: the next launch waited 20 s for the reconcile push and received the value |
| K6 | Another owner's environment requests the same consumer | Covered by tests: the pusher resolves bindings per owner, and the integration test shows a second environment receiving nothing pushed for the first |
| K7 | Start with a different key | Covered by tests: listing works and resolving fails with `ErrKey`; a short key file is refused |

The first leak scan reported one command-line match on each side; both were the scan's own `grep` command, which contained the search string. A pattern that cannot match itself found none.

## Measurements

| Measurement | Value |
|---|---|
| Launch delay added by the wrapper (pushed set) | Not separately measurable in `claude -p` timings |
| Launch after a worker restart | 20 s, waiting for the reconcile push |
| `boxface-api` binary growth from Go CDK `secrets` | 12.5 MB → 18.5 MB (gRPC and OpenTelemetry pulled in by the core package) |

## Findings

1. **Interactive Claude Code needs the onboarding flag.** With only `CLAUDE_CODE_OAUTH_TOKEN`, Claude Code would show its first-run screens; the wrapper's `hasCompletedOnboarding` write avoids them. Its per-folder trust question still appears, as for any new folder.
2. **A self-matching scan.** Searching `/proc/*/cmdline` for a string with a command that contains the string finds itself; acceptance scans must use a pattern such as `valu[e]`.
3. **Credentials are agent-specific in this version.** The owner decided afterwards that bindings must target any command or the whole session ([S7](../plans/2026-10-secret-store.md#s7-credentials-for-any-tool)).

## Credentials for Any Tool (S7)

**Result:** Pass on the droplet with a rebuilt image and the services from this change.

| Check | Result |
|---|---|
| Migration | The owner's `claude-code` binding became `claude`; the stored Claude Code token kept working |
| Command shim | In a `connect` session, `claude` and `node` resolved to shims in `/run/user/1000/boxface/bin`; Claude Code answered with the stored token; a credential bound to `node` reached `node` and not the shell; the `node` shim pointed at `/opt/boxface/profile/bin/node` |
| Session binding | Every new login shell had the value; tmux's global environment did not, because `connect` starts tmux from a non-login shell |
| Removing a session binding | A new shell that inherited the old value had it unset |
| Integration test | Shims and the session value in a login shell, none in a plain shell, none in tmux's environment, nothing in the other environment |

**Finding:** a session value enters tmux's own environment when tmux is started from a login shell, which `connect` never does but a user may. `boxface-run --shell-init` therefore records the names (never the values) of the session variables it exported and unsets those that are no longer bound in the next shell, so a deleted credential stops appearing in new windows.
