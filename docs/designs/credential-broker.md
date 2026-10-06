# Credential Broker

**Status:** Draft — proposed design

---

## Overview

The credential broker resolves server-approved secret bindings at launch time and delivers only the selected values or capabilities to the correct environment and session. The first external adapter uses server-side 1Password CLI/service-account access.

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

### Delivery

- Environment delivery is per process where possible.
- Temporary file delivery uses a runtime directory with restrictive ownership and cleanup.
- Session launchers construct the child process environment; secrets are never passed on command lines or written to a shared tmux environment. See [Access and Sessions](./access-and-sessions.md#credential-delivery-to-sessions).

### Rotation and Revocation

- Deleting a delivered file does not remove copies a process already read.
- Rotation affects new launches unless the consumer supports refresh.
- Revocation cannot erase a credential already delivered to a running process; stopping/restarting or provider-side revocation may be necessary.

### Agent Authentication

API-key and interactive subscription login paths differ between agents. Validate at least one supported path per agent. Agent-managed OAuth state may need persistent user storage and must be treated as sensitive backup content. Do not promise a universal environment-variable authentication interface.

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
