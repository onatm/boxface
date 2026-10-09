# Operations

**Status:** Draft — proposed design

---

## Overview

Every long-running mutation in Boxface (build, create, start, stop, delete, backup, restore, upgrade) is a durable operation. Operations make mutations idempotent, resumable after restarts, and observable without tying their lifetime to a client connection.

## Context & Problem

**Background:** Builds and VM lifecycle changes touch external resources and can be interrupted by API, worker, or host restarts. Clients on a tablet disconnect often. Without durable intent, retries duplicate disks and interruptions orphan processes.

## Goals

- Persist intent before performing external actions
- Make retries return the same operation and target
- Resume or clean up after any restart without touching unowned resources
- Report progress and actionable errors to every client

## Technical Design

### Lifecycle

An operation has its own `queued`, `running`, `succeeded`, `failed`, or `cancelled` state, independent of the target environment's state. Operation failure must not erase the environment's actual resources. Return structured error codes, retryability, and remediation hints.

- `step_failed`: a worker step returned an error; the message names the step.
- `internal_error`: the operation could not run, for example a database error; it is failed rather than retried in a loop. If even the failure cannot be recorded, the environment stays held until the API restarts.
- A shutdown interrupts an operation without failing it; on restart it resumes from its last completed step.

### Interfaces & Contracts

- Long-running mutations return `202 Accepted`, operation ID, and target ID.
- Support an idempotency key scoped by owner and operation type; reusing a key with different parameters is an error.
- Serialize conflicting lifecycle mutations per environment.
- Clients follow resumable event streams (`GET /v1/operations/{id}/events`). Disconnection does not cancel a build or create.

### Data Model / State

- Operation: UUID, type, target, idempotency key, steps, progress, error.
- Each operation is a sequence of idempotent steps with durable completion records.

### Recovery and Resource Ownership

Use temporary paths and atomic publication for images and disks.

| Failure | Required behaviour |
| --- | --- |
| Image build fails | Mark operation failed; preserve previous ready artifacts; remove owned temporary resources |
| VM launch fails after disk creation | Retain identifiable data disk; remove transient network/process resources |
| API restart | Worker/guest continue; clients resume operations by ID |
| Worker restart | Discover owned VM processes; reconnect or report a recoverable error |
| Host reboot | No sessions claimed live; recover disks and mark environments stopped |
| Guest service unavailable | Distinguish VMM running from guest ready; timeout with diagnostics |
| Disk full | Stop allocation/build safely; retain data; report capacity error |
| Secret lookup fails | Fail only the dependent launch; never fall back to another credential |
| Delete interrupted | Resume cleanup without touching unowned resources |

Image garbage collection protects artifacts referenced by environments, backups, and active operations. Retained data disks remain visible in inventory and consume quota until explicitly purged.

### Runtime / Operations

- Expose build logs, operation progress, VM health, resource consumption, and actionable errors.
- Keep terminal content and model prompts out of central logs by default.

## References

- [Architecture](../ARCHITECTURE.md)
- [Boxface API](./api.md)
- [Host Worker](./host-worker.md)
