# Boxface API

**Status:** Draft — proposed routes, not yet implemented; implementation decisions confirmed in the [Durable Server Lifecycle](../plans/2026-10-durable-lifecycle.md#decisions) plan

---

## Overview

The Boxface API authenticates clients, authorises access, validates configuration, stores immutable revisions, creates durable operations, and exposes environment state. The CLI and web UI are both clients of this API. It owns the SQLite metadata store.

## Context & Problem

**Background:** Every client must see the same recipes, environments, and operations, and every privileged action must pass through validation before reaching the host worker. The API is that single control-plane entry point; it does not execute client-supplied privileged shell commands.

## Goals

- One versioned API for CLI and web UI
- Authentication on every operation and per-resource ownership checks
- Transactional metadata with enforced uniqueness and immutable revisions
- Asynchronous, idempotent mutations through [Operations](./operations.md)

## Technical Design

### Routes

All routes are proposed and versioned under `/v1`. Require authentication for every operation and authorise ownership per resource.

| Method and route | Purpose |
| --- | --- |
| `GET /health` | Minimal liveness; no sensitive metadata |
| `GET /v1/capabilities` | Runtime, architecture, schema and supported feature information |
| `POST /v1/recipes` | Validate/store recipe revision |
| `POST /v1/profiles` | Validate/store user-profile revision |
| `POST /v1/images/build` | Create asynchronous image-build operation |
| `GET /v1/images` | List ready/failed image revisions |
| `POST /v1/environments` | Create from saved or submitted definition |
| `GET /v1/environments` | List authorised environments |
| `GET /v1/environments/{id}` | Inspect revision, state, resources, operations |
| `POST /v1/environments/{id}/start` | Start retained environment |
| `POST /v1/environments/{id}/stop` | Graceful shutdown |
| `DELETE /v1/environments/{id}` | Remove environment; preserve data unless explicit purge |
| `POST /v1/environments/{id}/connections` | Obtain authorised SSH connection information |
| `POST /v1/environments/{id}/sessions` | Start named user session |
| `GET /v1/environments/{id}/sessions` | List sessions |
| `POST /v1/environments/{id}/backups` | Back up stopped data disk |
| `POST /v1/backups/{id}/restore` | Create independent restored environment |
| `POST /v1/environments/{id}/upgrade` | Explicit revision replacement with backup |
| `GET /v1/operations/{id}` | Inspect durable operation |
| `GET /v1/operations/{id}/events` | Stream resumable operation events |

Long-running mutations return `202 Accepted`, operation ID, and target ID. Idempotency and serialization rules are in [Operations](./operations.md#interfaces--contracts).

### Implementation

- `boxface-api` runs as an unprivileged `boxface` user and is the only process that opens SQLite.
- HTTP uses the Go standard library `net/http`; operation events are server-sent events.
- SQLite uses `modernc.org/sqlite` (pure Go) in WAL mode, with schema migrations embedded in the binary.
- The API calls the [Host Worker](./host-worker.md#interfaces--contracts) over its Unix socket and runs durable operations itself.

### Authentication

- For the single-owner MVP, use a one-time local bootstrap flow to issue client credentials.
- Store revocable token hashes server-side; do not ship a default password.
- Browser access uses an authenticated secure session and CSRF protection.
- Full OIDC is deferred.

### Data Model

Use SQLite transactions for local metadata.

| Record | Essential fields |
| --- | --- |
| Owner | ID, authentication records, permitted bindings |
| Recipe revision | ID, source YAML, canonical spec, parent/name, creation time |
| Profile revision | ID, tools, pinned dotfiles, package inputs |
| Image | ID/digest, build key, artifact paths, ready/failed status, compatibility |
| Environment | UUID, owner, name, image ID, resource limits, desired/observed state, disk ID |
| Data disk | ID, path, allocated capacity, retention state, attached environment |
| Session | UUID, environment, name, kind, state, guest session identifier |
| Operation | UUID, type, target, idempotency key, steps, progress, error |
| Credential binding | ID, provider reference, permitted consumer/scope, owner; no plaintext value |
| Backup | ID, source environment/image, checksum, data version, storage location |

- Enforce owner/name uniqueness for environments and immutable revision records.
- VM process identity is the worker's systemd unit name, not a PID; see [Host Worker](./host-worker.md#vm-processes).
- State machines are owned by [Host Worker](./host-worker.md#environment-state-machine) (environment), [Operations](./operations.md#lifecycle) (operation), and [Access and Sessions](./access-and-sessions.md#session-state) (session).

### Security & Privacy

- `GET /health` exposes no sensitive metadata.
- Credential binding records never contain plaintext values.
- Environment create/delete and secret use are auditable without recording secret values.

## Open Questions

- [x] Choose implementation language and HTTP/RPC libraries. Go with the standard library `net/http` and server-sent events.

## References

- [Architecture](../ARCHITECTURE.md)
- [Operations](./operations.md)
- [boxfacectl](./cli.md)
- [Web UI](./web-ui.md)
