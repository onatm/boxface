# Web UI

**Status:** Draft — proposed design

---

## Overview

A minimal web interface for managing recipes, profiles, and environments. It uses the same validation and API as the CLI. Terminal access remains CLI-first; the web UI provides connection instructions rather than a browser terminal.

## Goals

- Routine configuration and lifecycle management without manual edits to host internals
- Same revisions and operations as CLI requests
- Clear separation of stop, delete, and purge

## Technical Design

### Scope

Server status, recipe/profile revisions, environment creation, environment detail, operation/build progress, start/stop/delete, backup status, and connection instructions.

### Behavior Rules

- Editing a recipe creates a new revision. Existing environments show that an update is available; they do not change silently.
- The UI separates stop, delete-with-data-retained, and purge.
- It never displays resolved credential values.

### Security & Privacy

Browser access uses an authenticated secure session and CSRF protection. See [Boxface API](./api.md#authentication).

## References

- [Boxface API](./api.md)
- [boxfacectl](./cli.md)
