# Agent Credential Store

**Status:** S1–S6 done ([acceptance](../research/2026-10-credential-store.md)); S7 (credentials for any tool) planned

**Roadmap:** [Usable configuration and operations](../ROADMAP.md#usable-configuration-and-operations), candidate bet "built-in secret store"

**Design:** [Credential Broker](../designs/credential-broker.md), [Access and Sessions](../designs/access-and-sessions.md#credential-delivery-to-sessions), [Boxface API](../designs/api.md), [Host Worker](../designs/host-worker.md), [Guest](../designs/guest.md), [Web UI](../designs/web-ui.md)

---

## Goal

The owner saves an agent credential once in the web UI, and every environment's agent sessions can use it with no per-environment sign-in. Values are encrypted at rest with an open-source library, never written in plaintext to any disk on the host or in the guest, and reach only the agent process that needs them. The store works the same on a public-address host, a Tailscale host, and a later hosted version.

## Current State

- Each agent signs in once per environment, and its login sits in a mode-600 file in the home on the data disk ([agent authentication](../research/2026-10-agent-auth.md)). A new environment means four sign-ins, and disk backups carry four usable logins.
- [Gate C](../research/2026-10-gate-c.md) proved per-process delivery over vsock: a guest launcher asked a host broker for bindings by ID, and the broker answered from 1Password. Its socket-path identity was a stub, and nothing stored secrets in Boxface.
- The worker already listens on each VM's own vsock socket (port 1024) for readiness, so a connection on that socket identifies the environment without any guest-supplied identity.
- Storage is decided: Go CDK `secrets`, with OpenBao and 1Password as later providers ([Credential Broker](../designs/credential-broker.md#storage)).

## What Can Be Stored

A stored credential is copied to many environments at once, so it must not change when used.

| Credential | Store it? | Reason |
|---|---|---|
| Claude Code `claude setup-token` token (`CLAUDE_CODE_OAUTH_TOKEN`) | **Yes** | One-year subscription token with no refresh; Anthropic documents it for headless use |
| Provider API keys (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, and others the agents read) | **Yes** | Static; usage-billed rather than subscription |
| Project secrets (for example `GITHUB_TOKEN`) | **Yes**, through the same mechanism | Static values a command needs |
| ChatGPT subscription login (`~/.codex/auth.json`, OpenCode's and Pi's ChatGPT OAuth) | **No** | Refresh tokens rotate when used; OpenAI says not to share `auth.json` across machines or concurrent jobs ([Codex CI/CD auth](https://learn.chatgpt.com/docs/auth/ci-cd-auth)). These stay per-environment sign-ins, which already work through `connect`'s automatic `localhost:1455` forward |
| Codex access tokens (`CODEX_ACCESS_TOKEN`) | Later | ChatGPT Enterprise only |

Using an Anthropic subscription token in a third-party agent such as Pi is outside what Anthropic permits, so presets bind the Claude setup token to Claude Code only.

## Options Considered

### Delivery to the guest

| Option | How | Verdict |
|---|---|---|
| A. Boot drive | At every start the worker adds the owner's bound values to a read-only drive, as it does for SSH keys; the guest copies them to `/run` | Simplest, but a changed secret needs a restart, values sit in a host file while the VM runs, and every process in the guest can read the copy |
| **B. Broker in the worker, pushed by the API** | The API decrypts the owner's bound values and pushes them to the worker for each running environment; the worker keeps them in memory only and answers guest requests on the VM's own vsock socket (port 1025). A launcher in the guest asks for one consumer's values and execs the agent with them in its environment | **Recommended.** No plaintext on any disk, per-process delivery, changes reach new sessions without a restart, and identity comes from the per-VM socket the worker owns. It is Gate C's design with the stub identity and the 1Password lookup replaced |
| C. Guest fetches from the API | The guest calls the API with a per-environment token | Guests must never reach the management plane |

### Storage

Decided: Go CDK `secrets` with a local key now, keeper URL for KMS or OpenBao Transit later ([comparison](../designs/credential-broker.md#storage)).

## Decisions

Confirmed by the owner on 2026-10-10, with the `shell` consumer deferred and the first acceptance target narrowed to the Claude Code setup token.

| Decision | Proposal | Reason |
|---|---|---|
| Encryption | `gocloud.dev/secrets` envelope encryption in `boxface-api`. `-secrets-keeper` defaults to a local key in `/var/lib/boxface/api/secrets.key` (32 random bytes, mode 0600, owned by `boxface`, created on first start); a `awskms://`, `gcpkms://`, `azurekeyvault://`, or `hashivault://` (Vault or OpenBao Transit) URL selects a KMS instead. All four drivers are compiled in, so the platform owner decides at install time; `boxface-api secrets rekey -from URL -to URL` re-encrypts stored values when the keeper changes | Open source, no extra service on one host, and a hosted version swaps the keeper without changing records |
| Records | `secrets (id, owner_id, name, ciphertext, keeper, created_at, updated_at)` and `secret_bindings (secret_id, consumer, variable)`. A secret can bind to several consumers, each under its own variable | Matches the design's binding model: provider reference, consumer, delivery as an environment variable |
| Consumers | `claude-code`, `codex`, `opencode`, `pi`. A `shell` consumer for other commands is deferred | The four agents get transparent wrappers; nothing needs secrets in every shell yet |
| Write-only values | The API and web UI accept values and never return them; listings show name, consumers, variables, and update time | The design's "never displays resolved credential values" |
| Push | The API pushes an environment's resolved set to the worker after `start_vm`, whenever a secret or binding changes for running environments, and on every reconcile pass for environments the worker reports without one. After a worker restart, a guest request waits up to 35 seconds for that push instead of failing. Values are never recorded in operation parameters, logs, or audit records | Worker restarts lose memory by design; the worker never calls the API, so reconcile restores it and the wait hides the gap |
| Broker protocol | One JSON request per vsock connection on port 1025: `{"consumer": "claude-code"}`; reply `{"env": {"VAR": "value"}}` or `{"error": "…"}`. The worker serves only the set pushed for the environment whose socket received the connection | Identity is the socket, not anything the guest says |
| Guest launcher | `boxface-run` in the image (Go, static, a few hundred lines) and wrappers `claude`, `codex`, `opencode`, `pi` in `/opt/boxface/bin`, placed first on PATH, that run `boxface-run --consumer <agent> -- <real binary> "$@"` | Agents just work in any shell or tmux window; values exist only in the agent process environment |
| Failure | With no binding for a consumer, the wrapper runs the agent unchanged, so per-environment sign-ins keep working. With a binding that cannot be fetched (broker unreachable or the set not yet pushed), the launch fails with a message naming the consumer; it never falls back to another credential | The design's failure rule, without breaking agents that have no stored credential |
| Claude Code onboarding | When a `claude-code` binding sets `CLAUDE_CODE_OAUTH_TOKEN`, the wrapper ensures `hasCompletedOnboarding` is set in `~/.claude.json` | Claude Code otherwise starts its interactive sign-in even with a valid token |
| Audit | `secret.create`, `secret.update`, `secret.delete`, and one `secret.deliver` per push (environment and secret names, never values) | Auditable without values |
| Web UI | A Credentials page: add or replace a value, choose a preset or a custom consumer and variable, delete; shows which agents are covered. Presets: Claude Code subscription token, OpenAI API key, Anthropic API key, OpenRouter API key, custom | Configuration stays in the web UI; no new CLI command |
| Key backup | The key file is not in the database; losing it loses the stored values, which must then be re-entered. Documented next to the database in the README | Keeps ciphertext and key apart |

## Scope

**In scope:** encrypted storage, write-only API and web UI, worker broker with push, guest launcher and agent wrappers, image rebuild, audit, acceptance on the droplet.

**Out of scope:** OpenBao and 1Password providers (the keeper interface leaves room), KMS drivers, per-environment or per-project scoping of bindings (all of an owner's bindings reach all of the owner's environments), file delivery, sharing ChatGPT subscription logins, rotation reminders, and Codex access tokens.

## Milestones

| Step | Work | Verify |
|---|---|---|
| S1. Store *(done)* | Migration; `internal/secrets` with the keeper, create/replace/delete/list, and `Resolve(owner)` returning consumer → variable → value; key file creation; routes `GET`/`POST /v1/secrets`, `PUT`/`DELETE /v1/secrets/{id}` | Tests: ciphertext differs from the value and decrypts only with the key; listings never include values; another owner cannot read, replace, or delete; a wrong key fails clearly; audit has no values |
| S2. Broker *(done)* | Worker RPC `PUT /v1/environments/{id}/secrets` (memory only), inventory flag for a loaded set, vsock listener on port 1025 per environment, request handling and limits | Tests: a request on one environment's socket never returns another's values; unknown consumer returns an error; unloaded set returns an error; the RPC body is never logged |
| S3. Push *(done)* | API pushes after `start_vm`, on secret changes for running environments, and on reconcile when the worker lacks a set; never stores values in operations | Tests: start pushes; a change reaches a running environment; after a simulated worker restart the next reconcile pushes again; operations and logs contain no value |
| S4. Guest *(done)* | `boxface-run` (cross-compiled into the image), wrappers, PATH change, onboarding fix; rebuild the image | Integration test on the droplet: a bound variable reaches the child process and not the parent shell, `/run`, `/tmp`, or the data disk; an unbound consumer runs unchanged; a bound but unpushed set fails the launch |
| S5. Web UI *(done)* | Credentials page with presets | Values never rendered back; cross-origin rules as for the other pages |
| S6. Acceptance *(done)* | Scenarios below on the droplet | Results recorded in `docs/research/` |

## S7. Credentials for Any Tool

Decided by the owner on 2026-10-10 after S1–S5: credentials are not specific to coding agents. Someone may bind a token for `gh`, `terraform`, or `aws` as readily as for `claude`.

| Decision | Proposal | Reason |
|---|---|---|
| Binding target | A binding names either a command (any name matching `^[a-z0-9][a-z0-9._+-]{0,63}$`, such as `gh` or `claude`) or `session` | The four fixed consumers become ordinary command names; nothing in the store is agent-specific |
| Command delivery (default) | When a login shell starts (every `connect` window, since tmux starts login shells), a profile script asks the broker which commands have bindings, resolves each to its real path on PATH, and writes shims to `/run/user/1000/boxface/bin` (tmpfs), placed first on PATH. A shim runs `boxface-run --command NAME -- REAL "$@"`, which adds only that command's variables | Only the named tool and what it starts see the value; any tool works without changing the image |
| Session delivery (opt-in per credential) | The same script exports `session` bindings into the shell's own environment, fetched fresh for each new shell; never set in tmux's server environment | For values wanted everywhere, such as a git token; the web UI marks them visible to every process in the session |
| Broker protocol | Adds `{"list": true}` → `{"commands": [...], "session": true/false}`; `{"consumer": NAME}` keeps its reply | One round trip per shell start, one per launch |
| Image | The fixed wrappers in `/opt/boxface/bin` are removed; `boxface-run` gains `--list`, `--session-env` (shell-quoted `export` lines on stdout), and `--command` | Wrappers come from bindings, not from the image |
| Migration | Existing bindings `claude-code`, `codex`, `opencode`, `pi` become `claude`, `codex`, `opencode`, `pi` | Same behaviour for the presets |
| Web UI | "Given to" chooses commands (free text, with the agents and common tools suggested) or "every session", with a warning on the latter | Presets stay as shortcuts |

Trade-offs accepted: a tool started by absolute path, or with a PATH that skips the shim directory, receives nothing; a shell opened before a binding was added gets the shim from its next start; session values stay in already-open shells after rotation or deletion.

| Step | Work | Verify |
|---|---|---|
| S7. General bindings | Migration and validation for command names and `session`; broker `list`; `boxface-run` modes; profile script; web UI changes; image rebuild | Tests: a `gh` binding reaches `gh` and not `git` or the parent shell; a `session` binding reaches a new shell and not tmux's server environment; removed bindings disappear from the next shell; presets still work after migration |

## Acceptance Scenarios

| ID | Scenario | Pass |
|---|---|---|
| K1 | Save a `claude setup-token` token in the web UI; create a new environment; `connect` and run `claude` | Claude Code answers with no sign-in, in that environment and in a second one |
| K2 | Replace the token's value while an environment runs | The next `claude` launch uses the new value without a restart |
| K3 | Delete the secret | The next launch runs Claude Code unchanged (its own sign-in, if any) |
| K4 | Leak scan after K1–K3 | The value is absent from SQLite plaintext, operation parameters, API and worker logs, every file under `/var/lib/boxface` and `/run` on the host, the guest's data disk and `/run`, and every process command line; present only in the agent process environment |
| K5 | Restart the worker while an environment runs | Within one reconcile pass, a new `claude` launch gets the value again |
| K6 | A second owner's environment requests `claude-code` | Receives only its own owner's bindings |
| K7 | Start the API with a different key file | Listing works; resolving fails with a clear key error; nothing is overwritten |

## Open Questions

- Whether a `shell` binding should also be offered to `connect` sessions automatically (for example a `GITHUB_TOKEN` for `git push`), which would put it in every shell's environment.
- Whether to scope bindings per environment or per project before a hosted version.
