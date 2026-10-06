# boxfacectl

**Status:** Draft — proposed command contract

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
