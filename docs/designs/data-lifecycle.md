# Data Lifecycle

**Status:** Draft — proposed design

---

## Overview

Each environment has a dedicated persistent data disk, separate from its read-only image. This doc covers how that data survives stop/start and image replacement, and how offline backup, restore, upgrade, delete, and purge work.

## Context & Problem

**Background:** The image is replaceable; the data disk holds repositories, homes, dependencies, agent state, and rootless Docker data. Reverting an image does not revert application data, and database or Docker format migrations may limit rollback.

## Goals

- Files and agent state survive clean stop/start and image replacement
- Consistent offline backups with recorded compatibility
- Restores that never silently reuse identity or login state
- Data deletion only through an explicit decision

## Technical Design

### Persistent Disk

- One ext4 data image per environment initially (proposed; see [Architecture](../ARCHITECTURE.md#decision-status)).
- Each environment records its image digest and data-format compatibility metadata.
- Mount layout is defined in [Guest](./guest.md#filesystem-layout).

### Backup

The MVP supports consistent **offline** data-disk backups. Stop the guest, flush/close the disk, copy or snapshot the backing storage through a supported mechanism, and record checksums and image compatibility. Local snapshots are not off-host backups.

### Restore

Restore creates a new disk and environment record. Renew machine/SSH identity for a clone and do not automatically activate restored agent login state without the owner's awareness. A recovery restore intended to replace the same environment must have an explicit identity policy.

### Upgrade

1. Resolve new image.
2. Verify compatibility.
3. Stop.
4. Back up.
5. Switch image pointer.
6. Start.
7. Validate.

On failure, retain diagnostics and offer image rollback. If the new guest modified persistent formats, require restoring the paired backup for a full rollback; do not claim pointer rollback alone is safe.

### Delete and Purge

- Delete removes VM resources and preserves the data disk unless an explicit purge is requested. The retained disk remains discoverable and consumes quota until purged.
- Purge removes the correct environment's data and leaves unrelated disks untouched.

### Data Model / State

- Data disk: ID, path, allocated capacity, retention state, attached environment.
- Backup: ID, source environment/image, checksum, data version, storage location.

### Runtime / Operations

- Backup, restore, and upgrade run as durable [Operations](./operations.md).
- Record backup duration and disk growth as MVP measurements.

### Security & Privacy

Backups of persistent user data can include login state. Document sensitive backup/restore behaviour; make no claim that user-data backups are secret-free.

## Open Questions

- [ ] Off-host backup approach; local backups do not protect against host-disk loss.

## References

- [Architecture](../ARCHITECTURE.md)
- [Guest](./guest.md)
- [Host Worker](./host-worker.md)
