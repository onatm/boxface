# Access and Sessions

**Status:** Terminal transport implemented and accepted ([connect plan](../plans/2026-10-connect-and-web-ui.md), [results](../research/2026-10-connect.md)); session records, credential delivery, and port forwarding proposed

---

## Overview

Clients control environments through the API and reach the guest terminal and application previews through SSH via a host gateway. Named tmux sessions inside the guest keep agents and shells running across client disconnects.

## Context & Problem

**Background:** A tablet client sleeps, changes networks, and drops connections. Work must continue remotely and be reattachable, and previews must reach the tablet without exposing guest services publicly. The gateway must not become a path to host management services.

## Goals

- Reattachable named terminals that survive client disconnects
- Authenticated SSH data path with pinned host and guest identity
- Loopback-bound previews by default
- No secrets in shared terminal environments or command lines

## Technical Design

### Terminal Transport

- Use the API to authorise access, then OpenSSH for the data path through the host gateway. *Implemented:* `boxfacectl connect` runs the user's `ssh` with `boxfacectl tunnel` as `ProxyCommand`; the tunnel is `GET /v1/environments/{id}/ssh` with `Upgrade: boxface-ssh` over HTTP/1.1, so it works the same over TLS on a public address and through `tailscale serve`. Every token check happens per tunnel, so a revoked token is refused on the next connection.
- Initial implementation can register a user's public key and manage narrowly scoped guest access. Credential expiry and revocation must be reflected on subsequent connections. Short-lived SSH certificates are an optional refinement, not required to build the first terminal path. *Implemented:* `login` registers a device key, and every start gives the VM the owner's current keys on a read-only drive, so adding or revoking a key takes effect at the next start ([decision](../plans/2026-10-connect-and-web-ui.md#open-questions)).
- The gateway resolves environment UUIDs to worker-owned guest endpoints. It must not become an arbitrary tunnel to host management services. *Implemented:* the API dials only port 22 of the worker-reported address of an owned, `running` environment, and records each tunnel as `environment.connect`.
- Pin host identity and maintain per-environment guest identity across restarts. Validate guest host identity; never disable host-key checking globally. *Implemented:* the worker generates the guest host key at disk creation and keeps its public half; `connect` pins it in a `known_hosts` file of its own under the environment ID and uses strict checking.
- Browser terminal streaming is not part of the first MVP.

### Sessions

A session records environment, name, kind, process state, and terminal attachment information. The native agent CLI runs in the guest as an ordinary process.

- tmux supplies persistent interactive terminals. systemd supervision can support managed background processes.
- A disconnected terminal can reattach while the guest remains running. *Implemented:* `connect NAME [--session S]` runs `tmux new-session -A -s S` (default `main`); session records are not built yet.
- After reboot, conversation recovery depends on the agent's own saved state; Boxface must not automatically replay a potentially destructive task.

### Session State

A session is `starting`, `running`, `exited`, or `unknown`. Client attachment is a separate property: losing an attachment does not mean the session exited. Host or guest reboot terminates processes; saved transcripts do not imply live session recovery.

### Credential Delivery to Sessions

Do not inject model secrets into a shared tmux server environment. A session launcher requests its approved bindings and constructs the child process environment without command-line secret values or plaintext intermediate files on persistent storage. See [Credential Broker](./credential-broker.md).

### Port Forwarding

- `forward travel 3000:3000` binds client loopback by default and forwards to the guest application port through authenticated SSH.
- Explicitly distinguish guest ports from ports inside Docker containers.
- Do not automatically expose guest services publicly. Public preview URLs are deferred.

### Runtime / Operations

Initial engineering target: session attachment within 3 seconds on a healthy local/private network, excluding authentication interaction. This is a provisional target, not a guarantee. Measured on the droplet from Europe to New York: 1.5 s over both exposures for a running environment, 8.5 s when `connect` starts a stopped one; SSH through `tailscale serve` survived 2-hour idle periods and a network switch.

### Security & Privacy

- Network reachability through the private network does not replace Boxface authentication.
- Terminal content is kept out of central logs by default.

## References

- [Architecture](../ARCHITECTURE.md)
- [Guest](./guest.md)
- [boxfacectl](./cli.md)
- [Credential Broker](./credential-broker.md)
