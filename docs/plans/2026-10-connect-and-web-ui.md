# Install, Configure, Connect

**Status:** Done — C0–C7 complete and acceptance A1–A8 passed on the droplet on 2026-10-10 ([results](../research/2026-10-connect.md#acceptance-c7)); one open question below

**Roadmap:** [Daily terminal workflow](../ROADMAP.md#daily-terminal-workflow), with the configuration part of the [Web UI](../designs/web-ui.md) pulled forward from [Usable configuration and operations](../ROADMAP.md#usable-configuration-and-operations)

**Design:** [Access and Sessions](../designs/access-and-sessions.md), [boxfacectl](../designs/cli.md), [Web UI](../designs/web-ui.md), [Boxface API](../designs/api.md), [Host Worker](../designs/host-worker.md)

---

## Goal

The owner goes to a machine, installs the server, and finishes setup in a web UI: SSH keys, the `main` image, default sizes, and client logins. After that, the client needs three commands:

```sh
boxfacectl login https://boxface.example.ts.net
boxfacectl create travel
boxfacectl connect travel
```

The API is reachable in one of two ways, and the CLI works the same through each:

| Exposure | TLS | When |
|---|---|---|
| Public address | Built into `boxface-api` (ACME or supplied certificate) | The machine has a public address enabled |
| Tailscale | `tailscale serve`, with an optional passwordless login via Tailscale identity | The public address is disabled; the owner's usual setup |

An SSH tunnel to the loopback API, as today, keeps working for development and recovery.

## Current State

- `create` requires `--image <uuid>`; images have no names and there are no server defaults.
- Owner SSH keys come from `/etc/boxface/authorized_keys` on the host, and `install.sh` refuses to run without that file. *(Replaced in C3.)*
- Connecting means finding the tap device on the host, deriving the guest address, and running `ssh -J root@host dev@172.20.x.y`. The address can change after each stop because the `/30` is released.
- The guest host key is generated inside the guest on first boot, so the client cannot pin it. *(Replaced in C4.)*
- The guest profile has no tmux.
- The API serves plain HTTP on `127.0.0.1:7443`. Tokens cannot be listed or revoked individually; `revoke` removes all of an owner's tokens.
- Login codes carry 160 bits and tokens 256 bits of entropy, both stored as SHA-256 hashes, so they are suitable for internet exposure.
- **Security finding:** the forward chain accepts all guest traffic that is not going to another guest (`iifname "bfx*" accept`) and masquerades it. On a host running Tailscale, a guest can reach every tailnet peer *as the host's tailnet identity*, and every LAN address. S1 passed on a droplet without Tailscale, so it did not cover this case.

## Options Considered

### Reaching the guest

| Option | How | Verdict |
|---|---|---|
| **A. API tunnel** | `boxfacectl` is an SSH `ProxyCommand`. It opens an authenticated HTTP Upgrade to the API, which dials only the owned, running guest's port 22 | **Recommended.** It is the only option that works the same with a public address and with Tailscale. Boxface authorises every connection, guest addresses stay internal, and later previews can ride the same SSH connection (`-L`) |
| B. Host subnet route | Host advertises `172.20.0.0/16` to the tailnet | Tailscale only. Guest addresses change on restart, and every guest port is exposed without Boxface authorisation |
| C. Tailscale in each guest | Each guest joins the tailnet | Tailscale only. Puts a tailnet credential and membership in an untrusted guest, needs TUN in the guest kernel, and needs an auth key per environment |
| D. Jump through host SSH | `ssh -J gateway@host` | Exposes host SSH to the internet and needs host accounts outside Boxface |

### Exposing the API over Tailscale

| Option | Verdict |
|---|---|
| **`tailscale serve` → API Unix socket** | **Recommended.** No new code or dependency in the API. Tailscale provides HTTPS at `https://<host>.<tailnet>.ts.net` and strips, then sets, `Tailscale-User-Login` for user-owned devices. Serving to a Unix socket means only root can forge identity headers. Caveats: HTTP Upgrade works only over HTTP/1.1 ([tailscale#20882](https://github.com/tailscale/tailscale/issues/20882)), there are reports of long-lived upgraded connections dropping ([tailscale#18827](https://github.com/tailscale/tailscale/issues/18827)), and `unix:` targets need a recent Tailscale. Validated in C0 |
| `tsnet` embedded in `boxface-api` | Fallback if C0 fails. Gives a direct `WhoIs` with no proxy, but adds the `tailscale.com` module tree to the server binary, a second tailnet node, and an auth key on first start |

### TLS for internet exposure

| Option | Verdict |
|---|---|
| **Built in: `golang.org/x/crypto/acme/autocert`, or `-tls-cert`/`-tls-key`** | **Recommended.** Install-and-go with one domain name and port 443 (TLS-ALPN-01 needs no port 80). A small, well-maintained dependency |
| Reverse proxy (Caddy) in front | Works with the loopback listener unchanged, but adds a second service to install and configure. The documented alternative |

## Scope

**In scope:**
- TLS and public-address hardening.
- First-run setup in the web UI.
- SSH keys, default image, and default sizes managed through the API.
- Named, revocable client tokens.
- Image aliases.
- Guest egress restricted away from private and tailnet ranges.
- A pinned per-environment SSH host key and an API tunnel to guest SSH.
- `boxfacectl login URL`, `create NAME` with no required flags, and `connect NAME`.
- tmux in the guest profile.
- Optional Tailscale identity login.

**Out of scope:**
- Any new CLI command beyond `connect`: port forwarding, `ssh-config`, and a web-login command are deferred.
- Session records and the `sessions` API; tmux `new-session -A` is enough for now.
- Credential delivery to sessions.
- A browser terminal.
- Image builds through the API; images still come from the experiment scripts.
- Recipes and profiles.
- Multiple owners.
- Updating SSH keys inside existing environments.

## User Journey

1. On the host: install Firecracker, build an image with the experiment scripts, and run `deploy/install.sh`. Then either run `tailscale serve` or enable the TLS listener. `install.sh` no longer needs an authorized keys file. It prints a one-time setup URL with a code.
2. In the browser: sign in with that code. Add an SSH public key, import the staged image and mark it `main`, set default sizes, and create a CLI login code.
3. On the tablet: `boxfacectl login https://boxface.example.ts.net`. It prompts for the code; over Tailscale with identity enabled, no code is needed.
4. `boxfacectl create travel`, then `boxfacectl connect travel`.

## Decisions

Confirmed by the owner on 2026-10-10. Environments created before this plan have no recorded host key and are recreated.

| Decision | Proposal | Reason |
|---|---|---|
| Listeners | `-listen` stays loopback HTTP by default. `-tls-listen :443` with `-tls-domain NAME` (ACME, cache under `/var/lib/boxface/api/acme`) or `-tls-cert`/`-tls-key` for a public address. `-tailscale-socket PATH` adds a Unix listener for `tailscale serve`. Any combination is allowed | One binary covers both exposures; nothing changes unless configured |
| Internet hardening | Body limits, header and idle timeouts on every listener (none on upgraded tunnels); per-IP rate limit on `/v1/login` and web login; tokens named per client and revocable individually; audit of logins and tunnel opens; `/health` stays minimal | Login codes and tokens are already strong; the gaps are brute-force noise, lost devices, and traceability |
| Port binding | The unit gains `AmbientCapabilities=CAP_NET_BIND_SERVICE` only when `-tls-listen` uses a port below 1024 | The API stays unprivileged |
| Data path | API tunnel: `GET /v1/environments/{id}/ssh` with `Upgrade: boxface-ssh`, returning `101`. The API resolves the guest address from the worker and dials only `<guest>:22` of an owned environment that is `running` | Works through both exposures; a single authorised gateway that cannot become an arbitrary tunnel ([Access and Sessions](../designs/access-and-sessions.md#terminal-transport)) |
| CLI surface | `login URL` (positional; `--server` still accepted), `create NAME` (flags optional, defaults from the server), and `connect NAME` (no required flags; `--session S` optional). `tunnel` is a hidden helper used as the SSH `ProxyCommand`. Existing `list`, `start`, `stop`, and `delete` are unchanged | The owner's daily path is three commands |
| Connect to a stopped environment | `connect` starts it, shows progress, then connects | One command to resume work. This changes the current rule that "starting is explicit" ([feature](../features/remote-development.md#user-facing-behavior), [boxfacectl](../designs/cli.md#behavior-rules)); starting never changes the image, so no silent upgrade happens |
| Previews | Deferred. When added, they ride the same tunnel as `ssh -L` bound to client loopback | Keeps the CLI to three commands now; the gateway still dials only port 22 |
| SSH keys | Owner keys are stored by the API (`ssh_keys` table, public keys only) and recorded with each create operation, so a resumed create seeds the same keys; the worker seeds them from `DiskSpec` instead of reading `/etc/boxface/authorized_keys`. `login` creates a device key beside the CLI config and registers it; the web UI lists, adds, and removes keys | No key management step in the CLI, and `connect` knows which key to use. Keys apply to environments created afterwards; updating existing ones needs a boot-time metadata channel and is deferred |
| Guest host key | The worker generates an ed25519 host key with `ssh-keygen` at `CreateDisk`, seeds it into `/boxface/ssh/` on the disk (which `boxface-data-init` already reuses), and keeps the public half at `disks/<id>.host_key.pub` | The client pins the key before the first connection, and no image change is needed. Environments created before this change must be recreated |
| Known hosts | `connect` writes the pinned key to `<config dir>/known_hosts` before running `ssh` with `StrictHostKeyChecking yes` | Pinned identity with no prompt and no edits to the user's own `known_hosts` |
| Server defaults | Per-owner defaults (`image`, `vcpus`, `memory_mib`, `disk_gib`) set in the web UI, falling back to `main`, 2, 2048, 20. Create fields become optional; the API fills them from defaults and records concrete values. The idempotency key still matches the request as sent. A missing image or alias fails with `not_found` naming it | Implements `create` with no file ([Configuration](../designs/configuration.md#saved-definitions-and-overrides), [feature](../features/remote-development.md#user-facing-behavior)) |
| Image aliases | `image_aliases (name, image_id, updated_at)`; the image field accepts an ID or alias, resolved once at create | Repointing `main` never changes existing environments ([Configuration](../designs/configuration.md#interpretation)) |
| First-run setup | `install.sh` runs `boxface-api bootstrap` and prints `<url>/login?code=…`. Bootstrap codes work for web and CLI login | One path from install to a configured server, with no default password |
| Web UI | Served by `boxface-api` at `/` from embedded static HTML, JS, and CSS, with no build step; it calls the `/v1` JSON API with `fetch` and `EventSource` | Same API as the CLI ([Web UI](../designs/web-ui.md)); no new toolchain |
| Web auth | On the Tailscale socket, a Tailscale login bound to the owner signs in directly. Otherwise a one-time code from `boxface-api bootstrap` is exchanged for an HttpOnly `SameSite=Strict` session cookie (`Secure` over HTTPS), stored as a hash like tokens. Cookie and identity-authenticated requests are wrapped in Go's `http.CrossOriginProtection` | Works with both exposures and adds no CLI command; ambient credentials get CSRF protection |
| Tailscale login | `boxface-api bootstrap -owner NAME -tailscale-login a@b`, or set in the web UI. A request on the Tailscale socket with a bound login is the owner; `boxfacectl login --server https://host.ts.net` then needs no code | Identity headers are trusted only on the socket only root can reach |
| Guest egress | The forward chain drops guest traffic to `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, `169.254.0.0/16`, and `fd7a:115c:a1e0::/48` before the internet accept | Closes the tailnet and LAN path; guest DNS is public (`1.1.1.1`). Guests do not need LAN access |
| Sessions | `connect NAME [--session S]` runs `tmux new-session -A -s S` (default `main`); tmux is added to the guest profile | Reattachable sessions without a session API |

## Web UI Scope

The web UI is for configuration and preparation; `boxfacectl` is for provisioning and use.

| Page | Contents |
|---|---|
| Setup | Shown until an SSH key, a `main` image, and defaults exist; a checklist linking to the pages below |
| Images | Staged imports to register; registered images with digest and date; assign or move the `main` alias |
| Defaults | Default image and sizes, with host capacity limits shown alongside |
| SSH keys | Add (paste a public key) and remove; fingerprints shown |
| Clients | Issue a CLI login code with the exact `boxfacectl login` command; list named tokens with last use; revoke one; bind a Tailscale login |
| Environments | Read-only list with state, image, and whether `main` has moved since creation; a copyable `boxfacectl connect` command. Start, stop, and delete stay in the CLI for now |

## Milestones

| Step | Work | Verify |
|---|---|---|
| C0. Spike *(done)* | `experiments/connect-spike/`, on the droplet with its public address firewalled off: `tailscale serve` to a Unix socket; identity headers and a forged one; SSE through it; a hand-written HTTP/1.1 Upgrade tunnel carrying SSH; 2-hour idle SSH sessions with and without `ServerAliveInterval 15`; a network switch with tmux reattach. Record the Tailscale version. The public-address path has no proxy in between and is covered in C7 | Results in `docs/research/2026-10-connect.md`. If upgrades drop through `tailscale serve`, switch to `tsnet` before C4 |
| C1. Guest egress | Private and tailnet drop rules in `firewallRuleset`; extend S1 to probe a LAN address and a tailnet peer | Ruleset unit test; S1 shows only the internet reachable, with Tailscale running on the host |
| C2. Listeners and hardening *(done)* | TLS listener (ACME and supplied certificate), Tailscale socket listener, timeouts, login rate limit, named tokens with per-token revocation, login audit. Identity headers are not used yet; C5b trusts them only on the Tailscale socket, which C2 already marks | Tests: plain, TLS (HTTP/2), and socket listeners serve, and only the socket is marked; incomplete TLS configuration refused; `X-Forwarded-For` ignored except on the socket; after ten failures from an address the next login returns `429`, even with a valid code; tokens listed by name with the current one marked; a revoked token is refused while others keep working; logins and revocations audited |
| C3. Configuration model *(done)* | Migration for `ssh_keys`, `owner_defaults`, and `image_aliases`; routes for each; create fills defaults and resolves aliases; keys recorded with the create operation and passed in `DiskSpec`; `login URL` registers a device key; `create NAME` and `image import --alias`; `install.sh` drops the authorized keys requirement. The setup URL moves to C6 with the web UI | Unit and API tests: create with no fields uses defaults and records concrete values; a retry after defaults change returns the same operation; repointing `main` leaves existing environments unchanged; a missing image names it; a create with no keys is rejected; a resumed create seeds the keys recorded at submit; option-prefixed and multi-line keys are rejected |
| C4. Gateway *(done)* | Worker: host key seeding, `GET /environments/{id}/endpoint`, `GET /disks/{id}/host-key`. API: `GET /v1/environments/{id}/connection` (user, host key) and the `ssh` upgrade route; `environment.connect` audit | API tests: another owner's, a stopped, or a deleted environment is refused before dialing; only port 22 is dialed. Integration test: SSH to the worker's endpoint with the pinned key and strict checking; SSH through the API tunnel end to end is checked with `connect` in C5 and A2 |
| C5. Client *(done)* | `login URL`; `create NAME` with all flags optional; `connect NAME [--session S]`, which starts a stopped environment first; hidden `tunnel` (HTTP/1.1 forced). Add `tmux` to the profile and rebuild the image | `connect` to a stopped environment starts it and connects; Ctrl-C while starting leaves the start running; Ctrl-C and network loss inside the session leave tmux running |
| C5b. Tailscale identity *(done)* | Generic `identities (provider, subject, owner)` table; `POST /v1/login` without a code signs in with the request's identity on the Tailscale socket; `GET`/`POST`/`DELETE /v1/identities`, where `POST` binds the requesting device's own login; `bootstrap -tailscale-login`; `login URL` tries the identity first and falls back to the code prompt. Codes remain the method for public-address hosts, and an OIDC provider for a hosted version fits the same table | Tests: the header is ignored off the socket; an unbound or missing login falls back to codes without counting toward the rate limit; a login cannot be bound to two owners or bound from another listener; unbinding stops identity logins |
| C6. Web UI *(done)* | The pages above, `web_sessions` with cookie sessions, cross-origin protection, and the setup URL printed by `install.sh` | Works through both exposures; a cross-origin POST is rejected; no secret values are shown |
| C7. Acceptance *(done)* | Scenarios below on the droplet. The ThinkCentre repeat belongs to the roadmap's final stage | Results recorded in `docs/research/2026-10-connect.md` |

## Acceptance Scenarios

| ID | Scenario | Pass |
|---|---|---|
| A1 | Fresh host: install, open the setup URL, add a key, mark an image `main`, set defaults, issue a CLI code | Setup checklist completes without editing host files beyond staging the image |
| A2 | Fresh client: `login URL`, `create travel`, `connect travel`, with no other flags | Lands in tmux session `main` with no host lookups or host-key prompts |
| A3 | Close the client terminal while a command runs, then `connect travel` again | Same session; the command is still running |
| A4 | Stop `travel`, then `connect travel` | Starts it (its address may change) and connects with the same pinned host key |
| A5 | Tunnel request for another owner's environment, a stopped one, or a deleted one, or with a revoked token | Refused before any connection to a guest |
| A6 | From a guest on a Tailscale host: reach a tailnet peer, a LAN address, the host, the internet | Only the internet is reachable |
| A7 | A2 and A3 over public-address TLS and over `tailscale serve` | Both work; only the Tailscale socket honours identity headers |
| A8 | Web UI: a cross-origin POST; a forged `Tailscale-User-Login` on the TLS listener | Both rejected |

## Measurements

- Install-to-first-connect time for a fresh host.
- Connect-to-prompt time for each exposure, against the provisional 3-second target in [Access and Sessions](../designs/access-and-sessions.md#runtime--operations).
- Tunnel survival across idle periods and network changes.

## Verification

- All scenarios pass, or each failure leads to a recorded design decision.
- `task build test lint` passes.
- The design docs record these decisions: [Access and Sessions](../designs/access-and-sessions.md), [boxfacectl](../designs/cli.md), [Web UI](../designs/web-ui.md), [Boxface API](../designs/api.md), and [Host Worker](../designs/host-worker.md).
- [Architecture](../ARCHITECTURE.md#decision-status) moves Access from proposed to decided.
- The README install section is rewritten around the setup URL.

## Open Questions

- **Keys for devices that log in after an environment exists.** Each `boxfacectl login` registers a new device key, but an environment accepts only the keys registered when it was created, so a second device cannot connect to existing environments ([C7 finding](../research/2026-10-connect.md#finding-a-new-device-cannot-connect-to-existing-environments)). Options:
  1. The worker rewrites `authorized_keys` on the stopped data disk at each start, from the owner's current keys recorded with the start operation. Simple and offline, but a running environment picks up a new key only after a restart.
  2. The guest fetches its authorized keys at boot and on demand over vsock from the worker, with `AuthorizedKeysCommand`. Changes apply at once, at the cost of a new guest-to-worker channel.
  3. SSH certificates: the API signs each device key with a per-owner CA that environments trust (`TrustedUserCAKeys`). Devices and revocation never touch the guest, matching the Access and Sessions refinement; certificate issuance and CA key custody are new work.
  
  Whichever is chosen, `connect` should report an authentication failure as a key problem, not as a lost connection.
