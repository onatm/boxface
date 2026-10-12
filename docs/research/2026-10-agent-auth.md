# Agent Authentication in Environments

**Date:** 2026-10-10

**Result:** Pass. Claude Code, Codex, OpenCode, and Pi each signed in with a subscription inside an environment over `boxfacectl connect` and completed a small task; their logins survived a stop and start.

**Roadmap:** [Daily terminal workflow](../ROADMAP.md#daily-terminal-workflow) success signal; carried forward from [Gate C](./2026-10-gate-c.md#not-covered)

---

## Summary

All four agents support a subscription sign-in that works from inside an environment with no browser in the guest. Claude Code shows a URL and takes a pasted code. Codex, OpenCode, and Pi sign in with ChatGPT, whose OAuth flow redirects the client's browser to `http://localhost:1455/auth/callback`; the listener is inside the guest, so the redirect fails unless the client's `localhost:1455` is forwarded to the guest. `connect` now forwards it automatically. Each agent keeps its login in a mode-600 file in the persistent home, so logins are per environment, survive restarts, and are part of the data disk.

## Environment

| Item | Value |
|---|---|
| Host | The droplet from the [connect results](./2026-10-connect.md), Tailscale exposure |
| Environment | `travel`, 2 vCPU, 2048 MiB, Gate C image with tmux |
| Client | macOS, `boxfacectl connect travel` |
| Agents | Claude Code 2.1.223, Codex CLI 0.146.0, OpenCode 1.15.10, Pi 0.75.4 |
| Accounts | Claude subscription for Claude Code; ChatGPT subscription for Codex, OpenCode (OpenAI provider), and Pi (`openai-codex`) |

## Results

| Agent | Sign-in path that worked | Login state | Task | Survived stop and start |
|---|---|---|---|---|
| Claude Code | `/login`, URL opened on the client, code pasted back | `~/.claude/.credentials.json`, 600, 504 bytes | Pass, 14 s | Pass |
| Codex | `codex login` (ChatGPT) with `localhost:1455` forwarded | `~/.codex/auth.json`, 600, 4614 bytes | Pass, 18 s | Pass |
| OpenCode | `opencode auth login`, OpenAI/ChatGPT, with `localhost:1455` forwarded | `~/.local/share/opencode/auth.json`, 600, 2190 bytes | Pass, 35 s | Pass |
| Pi | `/login`, ChatGPT, with `localhost:1455` forwarded | `~/.pi/agent/auth.json`, 600, 2196 bytes | Pass, 12 s | Pass |

The task was the same for each agent in its own clone of a small Node module: fix `slugify` so two failing `node --test` tests pass, without editing the tests. Each agent was run headless (`claude -p`, `codex exec --full-auto`, `opencode run`, `pi -p`) after the owner signed in interactively; every run changed only `slug.js` and left both tests passing. After `stop` and `start`, each agent answered a prompt without signing in again.

## Findings

1. **ChatGPT sign-in needs the callback port forwarded.** Without it, Codex's subscription login, OpenCode's OpenAI login, and Pi's ChatGPT login all failed: the browser on the client was redirected to `localhost:1455`, where nothing listened. A manual `--forward 1455` fixed all three, but the owner found the extra flag unpleasant, so `connect` now forwards the client's `localhost:1455` to the guest whenever that port is free on the client, and prints one line when it is not. The tunnel still dials only guest port 22; the forward rides the SSH connection and binds client loopback only. `codex login --device-auth` avoids the redirect but requires device-code sign-in to be enabled in the ChatGPT account.
2. **Logins are per environment.** Each new environment needs its own sign-in for every agent, and a backup of the data disk carries usable tokens for all four. A Boxface secret store would remove the repeated sign-ins; see the [Credential Broker](../designs/credential-broker.md#storage) decision.
3. **OpenCode migrates its database on first use** ("one time database migration"), which took under a second here.

## Not Covered

- Interactive use on the tablet itself; the client was macOS.
- API-key paths, and Claude Code's `setup-token` path inside `connect` sessions (validated in Gate C with the stub broker).
- Token expiry and refresh over long periods.
