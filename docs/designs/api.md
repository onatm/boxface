# Boxface API

**Status:** Draft — routes marked *Implemented* exist; the rest are proposed

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

Routes are versioned under `/v1`. Every route except `GET /health` and `POST /v1/login` requires a bearer token, and ownership is authorised per resource. Routes marked *implemented* exist; the rest are proposed.

| Method and route | Purpose |
| --- | --- |
| `GET /health` | Minimal liveness; no sensitive metadata. *Implemented* |
| `POST /v1/login` | Exchange a one-time bootstrap code for an API token, `{"code", "name"}`; the name identifies the client. Without a code, sign in with the request's bound identity (Tailscale socket only). *Implemented* |
| `POST /v1/login-codes` | Issue a one-time login code for the owner, valid 15 minutes, for a new device. *Implemented* |
| `GET /web/session`, `POST /web/login`, `POST /web/logout` | Web UI sign-in: `POST /web/login` takes `{"code"}`, or no code for a bound identity on the Tailscale socket, and sets an `HttpOnly`, `SameSite=Strict` session cookie; the cookie authenticates `/v1` requests like a token. `GET /web/session` reports whether the browser is signed in and the request's Tailscale login, if any. *Implemented* |
| `GET /v1/devices`, `DELETE /v1/devices/{id}` | The owner's logged-in devices: each active API token with name, creation, last use (to the minute), whether it made the request, and the SSH keys it registered (`ssh_keys`). Revoking a device revokes its token and deletes those keys in one transaction. *Implemented* |
| `GET /v1/identities`, `POST /v1/identities`, `DELETE /v1/identities/{id}` | The owner's bound sign-in identities; binding uses the identity of the request itself, so `POST` works only through `tailscale serve` from a user-owned device. `201` when new, `200` when already bound, `409 identity_taken` when bound to another owner. *Implemented* |
| `GET /v1/capabilities` | Version, architecture, hypervisor, and capacity limits. *Implemented* |
| `POST /v1/recipes` | Validate/store recipe revision |
| `POST /v1/profiles` | Validate/store user-profile revision |
| `POST /v1/images/build` | Create asynchronous image-build operation |
| `POST /v1/images` | Register an artifact set staged in the import directory, `{"source": "<name>"}`; `201` when new, `200` with the existing image when the content digest matches. *Implemented* |
| `GET /v1/images` | List ready/failed image revisions with their aliases. *Implemented* |
| `DELETE /v1/images/{id}` | Remove an image's files; refused with `409 in_use` while an alias points to it or a live environment uses it. The record stays for deleted environments that reference it; importing the same content restores it. *Implemented* |
| `GET /v1/storage`, `DELETE /v1/storage/build-cache` | Host disk: total and free, and space allocated to images, profile drives, build caches, and data disks; clearing the owner's Nix build cache makes the next build cold. *Implemented* |
| `GET /v1/image-imports` | Directories in the import directory holding both artifacts, ready to register. *Implemented* |
| `PUT /v1/image-aliases/{name}` | Point an alias such as `main` at an image, `{"image_id": "..."}`; existing environments keep their image. *Implemented* |
| `GET /v1/templates`, `GET /v1/templates/{name}`, `PUT /v1/templates/{name}` | The owner's templates for new environments: `name`, `image` (ID or alias), `profile`, `vcpus`, `memory_mib`, `disk_gib`, and `saved`. Only `default` exists; `create` uses it for every field the request omits, and other names return 404. Unsaved, it is `main`, `main`, 2, 2048, 20. *Implemented* |
| `GET /v1/profiles`, `POST /v1/profiles`, `GET /v1/profiles/{id}`, `POST /v1/profiles/{id}/revisions`, `DELETE /v1/profiles/{id}` | User profiles: `{"name", "spec"}` creates a profile and its first revision; a new spec adds an immutable revision. A spec is either `{"packages": [nixpkgs attribute names]}` or `{"module": "<Home Manager module>"}`, plus `"unfree"` and optional `"dotfiles": {"repo": "https://…", "ref"}`. `chezmoi`, `git`, and `tmux` are always included. Revisions start `pending` with a build key over the owner, the normalized spec, the server's pinned inputs, and the builder version, and the API builds them one at a time in a builder VM (`-build-vcpus`, `-build-memory-mib`, `-build-timeout`) into `building`, then `ready` or `failed` with the error and log tail ([profile builder results](../research/2026-10-profile-builder.md)). A revision whose build key already has a ready drive is ready at once. Deleting a profile is refused while it is the default or a live environment uses it, and frees its name. Profile drives are pruned automatically: each profile's latest ready drive, drives that live environments use, and anything changed in the last 15 minutes are kept; pruned revisions are marked `pruned` and rebuild if saved again. *Implemented* |
| `GET /v1/secrets`, `POST /v1/secrets`, `PUT /v1/secrets/{id}`, `DELETE /v1/secrets/{id}` | Agent credentials: `{"name", "value", "bindings": [{"consumer", "variable"}]}`. Values are write-only: responses carry names, bindings, and times only. A consumer is a command name (`^[a-z0-9][a-z0-9._+-]{0,63}$`, such as `gh` or `claude`) or `session`; the listing also returns suggested command names. Variables are upper-case names other than reserved ones such as `PATH`, `HOME`, and `LD_*`; a consumer and variable can be bound by one secret only. Every change is pushed to the owner's running environments. *Implemented* |
| `GET /v1/ssh-keys`, `POST /v1/ssh-keys`, `DELETE /v1/ssh-keys/{id}` | The owner's SSH public keys, `{"name", "public_key"}`; one key line without options; `201` when new, `200` with the existing key when the fingerprint matches. A key added with a device's API token is linked to that device (`device_id`) and moves to it if it already existed; a key added from a web session is not linked. *Implemented* |
| `POST /v1/environments` | Create from saved or submitted definition. *Implemented* for `name` and optional `image_id` (ID or alias), `profile` (profile name, default from the owner's defaults, `main` built in), `vcpus`, `memory_mib`, `disk_gib`; the profile resolves to its latest ready revision and the environment records it, so later revisions never change it; if the default profile does not exist the environment has no profile drive, and a named profile that is missing or not yet built is refused (`not_found`, `profile_not_ready`); omitted fields come from the owner's defaults, are resolved once, and the environment records the concrete values. The owner's SSH keys at submission are recorded with the operation and given to the VM; a create with no registered key is rejected |
| `GET /v1/environments` | List authorised environments, including deleted ones whose disk is retained. *Implemented* |
| `GET /v1/environments/{id}` | Inspect revision, state, resources, operations. *Implemented* |
| `POST /v1/environments/{id}/start` | Start retained environment. The owner's SSH keys at submission are recorded with the operation and given to the VM, so keys added or removed since the last start apply; a start with no registered key is rejected. *Implemented* |
| `POST /v1/environments/{id}/stop` | Graceful shutdown. *Implemented* |
| `DELETE /v1/environments/{id}` | Remove environment; preserve data unless `?purge=true`. *Implemented* |
| `GET /v1/environments/{id}/connection` | SSH connection information: `{"environment_id", "user", "host_key", "key_fingerprints"}`, where `key_fingerprints` are the keys the environment last started with, the pinned public host key generated with the disk. Refused for a deleted environment. *Implemented* |
| `GET /v1/environments/{id}/ssh` | SSH tunnel: with `Connection: Upgrade` and `Upgrade: boxface-ssh` over HTTP/1.1, returns `101` and splices the connection to port 22 of the owned environment's guest. Only a `running` environment is dialed; the worker supplies the address and the API never dials another port. Each tunnel is recorded as `environment.connect`. *Implemented* |
| `POST /v1/environments/{id}/sessions` | Start named user session |
| `GET /v1/environments/{id}/sessions` | List sessions |
| `POST /v1/environments/{id}/backups` | Back up stopped data disk |
| `POST /v1/backups/{id}/restore` | Create independent restored environment |
| `POST /v1/environments/{id}/upgrade` | Explicit revision replacement with backup |
| `GET /v1/operations/{id}` | Inspect durable operation. *Implemented* |
| `GET /v1/operations/{id}/events` | Stream resumable operation events. *Implemented* |

Long-running mutations return `202 Accepted` with `operation_id` and `environment_id`, and a `Location` header for the operation. The `Idempotency-Key` header carries the idempotency key. Idempotency and serialization rules are in [Operations](./operations.md#interfaces--contracts).

- Request bodies with unknown fields are rejected.
- Errors are `{"error": {"code", "message", "retryable"}}`. Codes: `unauthorized` (401), `invalid_request` (400), `not_found` (404), and with 409 `name_taken`, `environment_deleted`, `image_not_ready`, `profile_not_ready`, `idempotency_conflict`, `capacity_exceeded`, `environment_not_running`, `identity_taken`; `guest_unreachable` (502, retryable) when the guest's SSH port does not answer. Other failures return `internal_error` (500) without internal detail.
- Operation events are server-sent events: one `step` event per completed step with IDs from 1, then a final `state` event carrying the operation. A client reconnecting with `Last-Event-ID` receives only later events.
- Capacity limits on environments, vCPUs, memory, and disk count every environment that is not deleted, whether running or stopped; the disk limit also counts retained disks.

### Implementation

- `boxface-api` runs as an unprivileged `boxface` user and is the only process that opens SQLite.
- HTTP uses the Go standard library `net/http`; operation events are server-sent events.
- SQLite uses `modernc.org/sqlite` (pure Go) in WAL mode, with schema migrations embedded in the binary.
- The API calls the [Host Worker](./host-worker.md#interfaces--contracts) over its Unix socket and runs durable operations itself.

### Authentication

- `boxface-api bootstrap -owner NAME`, run on the host as the `boxface` user, creates the owner if needed and prints a one-time login code valid for 15 minutes. `POST /v1/login` exchanges it once for an API token.
- Codes and tokens are stored only as SHA-256 hashes; there is no default password. `DELETE /v1/devices/{id}` revokes one device; `boxface-api revoke -owner NAME` revokes all of an owner's tokens. Both delete the SSH keys those devices registered, so environments stop accepting them at their next start. Logins, revocations, and SSH tunnel opens are recorded with the client address in `auth_events`.
- Sign-in methods are pluggable identity providers on top of codes and tokens. A login code works on every listener and is the only method that needs no extra infrastructure, so a public-address host or a hosted service never depends on Tailscale. An *identity* is a `(provider, subject)` pair bound to one owner; `POST /v1/login` without a code signs in with the request's identity and issues an ordinary named token.
- Tailscale is the first provider, and it is optional. `tailscale serve` strips client-sent identity headers and sets `Tailscale-User-Login` for user-owned devices, so the API trusts that header only on the Tailscale socket. Tagged devices carry no login and must use a code. The owner binds a login with `boxface-api bootstrap -owner NAME -tailscale-login LOGIN` on the host, or with `POST /v1/identities {"provider": "tailscale"}`, which binds the login of the device making the request, so a login cannot be bound without signing in as it. Unbinding stops new identity logins; tokens already issued stay valid until revoked. Identity logins never count toward the login rate limit, since no secret is guessed.
- An OIDC provider for a hosted version fits the same model: the issuer and `sub` claim become the provider and subject.
- After 10 failed logins from one address in 10 minutes, `POST /v1/login` returns `429 rate_limited` (retryable, with `Retry-After`) for that address until the window ends.
- Listeners, any combination: `-listen` (plain HTTP, default `127.0.0.1:7443`, empty disables it); `-tls-listen` with `-tls-domain` (Let's Encrypt over TLS-ALPN-01, so port 443, cached in `-acme-dir`) or `-tls-cert` and `-tls-key`; `-tailscale-socket`, a Unix socket (mode 0600) for `tailscale serve`. Every listener has header and idle timeouts. `X-Forwarded-For` is trusted only on the Tailscale socket, whose proxy is `tailscaled`; it gives the client address for rate limiting and audit.
- Browser access uses a session cookie (`web_sessions`, hashed, 30 days) and `http.CrossOriginProtection` on every request; see [Web UI](./web-ui.md#security--privacy).
- OIDC is deferred.

### Data Model

Use SQLite transactions for local metadata.

| Record | Essential fields |
| --- | --- |
| Owner | ID, authentication records, permitted bindings, create defaults |
| API token | ID, owner, SHA-256 hash, client name, creation, last use, revocation |
| Auth event | Owner, action (`login`, `token.revoke`, `identity.bind`, `identity.unbind`, `environment.connect`, `secret.create`, `secret.update`, `secret.delete`, `secret.deliver`, `profile.create`, `profile.revise`), token ID, target (the environment for connects, the identity for identity logins and bindings), client address, time |
| Web session | ID, owner, SHA-256 hash, creation, expiry, end |
| Profile | ID, owner, name (unique per owner; the name is how environments refer to it) |
| Profile revision | ID, profile, number, normalized spec, build key, state (`pending`, `building`, `ready`, `failed`), error, build log tail, resolved dotfiles commit |
| Secret | ID, owner, name (unique per owner), ciphertext from the Go CDK keeper, creation, update; never the value |
| Secret binding | Secret, owner, consumer, variable; unique per owner, consumer, and variable |
| Identity | ID, owner, provider (`tailscale`), subject (the provider's user, such as `alice@github`), creation; each provider and subject is bound to at most one owner |
| SSH key | ID, owner, name, public key without comment or options, SHA-256 fingerprint unique per owner |
| Image alias | Name, image ID; repointing never changes existing environments |
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
