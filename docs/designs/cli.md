# boxfacectl

**Status:** Draft — lifecycle subset implemented; the rest is a proposed command contract

---

## Overview

`boxfacectl` is the native CLI for Boxface. It manages servers, recipes, profiles, and environments through the [Boxface API](./api.md) and opens terminals and port forwards over SSH. It needs only network access and authentication; it requires no local checkout or build tooling and runs on a Linux arm64 client against an x86-64 host.

## Goals

- Every lifecycle action available from a tablet terminal
- Scriptable structured output and consistent exit codes
- No silent upgrades and no accidental data deletion

## Technical Design

### Commands

```bash
boxfacectl server add home https://boxface.example
boxfacectl login
boxfacectl doctor

boxfacectl recipe apply --file recipe.yaml
boxfacectl profile apply --file profile.yaml
boxfacectl create travel --recipe development --profile personal
boxfacectl create experiment --config boxface.yaml
boxfacectl create quick

boxfacectl list
boxfacectl inspect travel
boxfacectl connect travel
boxfacectl connect travel --session coding
boxfacectl exec travel -- git -C /workspace/project status
boxfacectl forward travel 3000:3000

boxfacectl stop travel
boxfacectl start travel
boxfacectl backup travel
boxfacectl upgrade travel --image IMAGE_ID
boxfacectl delete travel
boxfacectl delete experiment --purge-data
```

### Implemented Subset

The [lifecycle plan](../plans/2026-10-durable-lifecycle.md) implements `login`, `image import`, `image list`, `create`, `list`, `inspect`, `start`, `stop`, `delete`, and `operation`. The rest of the commands above remain proposed.

- `login --server URL [--code CODE]` exchanges a one-time code from `boxface-api bootstrap` for a token. It prompts for the code when stdin is a terminal. One server is stored, with its token, in `$BOXFACE_CONFIG` or the user config directory (`boxface/config.json`, mode 0600); `server add` and named servers are deferred.
- `create`, `start`, `stop`, and `delete` follow the operation and print each completed step, then the environment; `--no-wait` returns once the operation is accepted. Each invocation sends a fresh idempotency key and reuses it when retrying a request after a network failure.
- Ctrl-C stops following, leaves the operation running on the server, and prints `boxfacectl operation <id> --follow` to resume. The event stream reconnects with `Last-Event-ID` after a dropped connection.
- Environments are named by name or ID. A name matches the live environment first, then a deleted one whose disk is retained, so `delete NAME --purge-data` can purge it.
- `delete` asks for the environment name to be typed unless `--yes` is given, and refuses when stdin is not a terminal. `--purge-data` also deletes the disk.
- `--json` prints the result as JSON. Exit codes: 0 success, 1 error or failed operation, 2 usage, 130 interrupted.

### Behavior Rules

- `create quick` requires configured server defaults. A new environment resolves defaults once and records exact revisions.
- `connect` to a stopped environment reports its state; an explicit `--start` option can start and wait. Avoid silent image upgrades on connect.
- `exec` transports an argument array, working directory, and explicit environment bindings. It does not concatenate untrusted strings into a host shell command. Interactive shell execution is a distinct operation.
- `forward` binds client loopback by default; see [Access and Sessions](./access-and-sessions.md#port-forwarding).

### Output and Progress

- Structured JSON output and consistent exit codes.
- Displays server-owned progress and allows reconnection to operation events. Disconnection does not cancel a build or create.

### Destructive Actions

Destructive actions require an explicit flag or interactive confirmation. Stopping is distinct from deleting data, and deleting is distinct from purging.

## References

- [Boxface API](./api.md)
- [Access and Sessions](./access-and-sessions.md)
- [Remote Development Environments](../features/remote-development.md)
