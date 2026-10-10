# Credential Broker

**Status:** Storage and per-process delivery to agents implemented ([plan](../plans/2026-10-secret-store.md)); providers, file delivery, and other consumers proposed

---

## Overview

The credential broker resolves server-approved secret bindings at launch time and delivers only the selected values or capabilities to the correct environment and session. Secrets are stored by Boxface itself, encrypted with an open-source library, so a host needs no external secret service; 1Password (validated in [Gate C](../research/2026-10-gate-c.md)) and OpenBao are later providers.

## Context & Problem

**Background:** Agents and project tools need model and service credentials, but images, build logs, and repositories must never contain them. A guest is untrusted relative to host management services, so it cannot be allowed to name arbitrary vault references or expand its own access.

## Goals

- Keep broad service-account tokens on the server
- Resolve credentials at launch, never at image build
- Deliver per process where possible, with no plaintext on persistent storage
- Fail clearly and locally when resolution fails

## Technical Design

### Interfaces & Contracts

- A secret-provider interface with resolve, metadata, and failure reporting. Implement one provider first; keep the interface narrow.
- A binding maps a server-approved provider reference to an owner, consumer, delivery mode, and scope.
- The broker must not accept arbitrary vault references directly from untrusted guest requests. A guest cannot choose arbitrary destination environments or expand its own bindings.

### Storage

Decided by the owner on 2026-10-10: built in, using an open-source secret store rather than Boxface's own cryptography, with external providers later.

| Option | License | Verdict |
|---|---|---|
| **Go CDK `secrets` (`gocloud.dev/secrets`)** | Apache-2.0 | **Chosen.** Envelope encryption inside `boxface-api`, ciphertext in SQLite. The keeper is a URL: `base64key://` (a host key file readable only by `boxface`) for self-hosted installs, and AWS KMS, GCP KMS, Azure Key Vault, or Vault/OpenBao Transit for a hosted version, with no change to stored records beyond re-wrapping. No new service to run on a single host |
| OpenBao | MPL-2.0 | Later provider. A Linux Foundation fork of Vault: single binary, Raft or PostgreSQL storage, KMS auto-unseal plugins, and per-tenant namespace sealing (v2.6–2.7), which suits a multi-tenant hosted version; requiring it would add a service to run and unseal after every reboot |
| Infisical | MIT core | Not chosen: needs PostgreSQL and Redis, with 4–8 GB of memory recommended |
| 1Password service accounts | Proprietary | Later provider; validated in Gate C |
| SOPS, age | MPL-2.0, BSD | File encryption, not a store |

### Delivery

*Implemented:* `boxface-api` resolves an owner's bindings and pushes them to the worker after each start, after every change, and on reconcile; the worker holds them in memory and answers the guest launcher on the VM's own vsock socket; the launcher adds only the requesting agent's variables to that agent's environment. Nothing is written to disk on the host or in the guest, and changes reach the next launch without a restart.

- Environment delivery is per process where possible.
- Temporary file delivery uses a runtime directory with restrictive ownership and cleanup.
- Session launchers construct the child process environment; secrets are never passed on command lines or written to a shared tmux environment. See [Access and Sessions](./access-and-sessions.md#credential-delivery-to-sessions).

### Rotation and Revocation

- Deleting a delivered file does not remove copies a process already read.
- Rotation affects new launches unless the consumer supports refresh.
- Revocation cannot erase a credential already delivered to a running process; stopping/restarting or provider-side revocation may be necessary.

### Agent Authentication

API-key and interactive subscription login paths differ between agents. Validate at least one supported path per agent. Agent-managed OAuth state may need persistent user storage and must be treated as sensitive backup content. Do not promise a universal environment-variable authentication interface.

All four agents sign in with a subscription inside an environment and keep the login in a mode-600 file in the persistent home ([agent authentication](../research/2026-10-agent-auth.md)): Claude Code in `~/.claude/.credentials.json` (code pasted from the client), and Codex, OpenCode, and Pi with ChatGPT, whose OAuth redirect to `localhost:1455` reaches the guest because `connect` forwards that port. These logins are per environment and are carried by disk backups.

Claude Code supports both subscription paths ([Gate C](../research/2026-10-gate-c.md)): interactive login over SSH stores plaintext tokens in `~/.claude/.credentials.json` on the data disk, and a `claude setup-token` token stored with the secret provider can be delivered per session as `CLAUDE_CODE_OAUTH_TOKEN` without writing a credentials file.

### Data Model / State

- Credential binding: ID, provider reference, permitted consumer/scope, owner; no plaintext value.

### Runtime / Operations

- A failed lookup fails only the dependent launch with a clear session error; never fall back to another credential.
- Secret use is auditable without recording secret values.

### Security & Privacy

- Provision the 1Password bootstrap token as a protected host credential, outside configuration committed to Git and outside SQLite plaintext records.
- Delivered secrets remain readable by the receiving process and often other processes under the same user.
- Build jobs cannot access runtime secrets.

## References

- [Architecture](../ARCHITECTURE.md)
- [Configuration](./configuration.md)
- [1Password service accounts](https://developer.1password.com/docs/service-accounts/)
- [1Password runtime secret injection](https://developer.1password.com/docs/cli/secrets-scripts/)
