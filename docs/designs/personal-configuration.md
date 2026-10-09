# Personal Configuration

**Status:** Draft — proposed design

---

## Overview

chezmoi seeds a new environment's home from a pinned profile revision and applies later profile updates without overwriting the user's modifications or agent state.

## Context & Problem

**Background:** Home Manager owns packages and chezmoi owns personal files; both must never manage the same files. Agents write authentication and conversation state into the home, and users edit their own configuration. A reboot or profile update must not destroy either.

## Goals

- Seed a new home from a pinned dotfiles commit
- Update only files the user has not changed
- Never overwrite agent-managed state

## Technical Design

### Seeding

Use chezmoi to seed a new home from a pinned profile revision. Record the applied revision and managed file manifest. Reboot is not permission to overwrite the home.

Gate C validated seeding from a read-only, image-embedded source applied once per data disk by a user service, with the revision and managed files recorded under `~/.local/state/boxface/` ([Gate C](../research/2026-10-gate-c.md)). Agents write their own files beside managed ones, as Claude Code does in `~/.claude`, so dotfiles manage individual files, never agent-written ones.

### Profile Updates

For MVP profile updates:

1. Compare the previous managed content, current file, and new generated content.
2. Automatically update only unchanged managed files.
3. Preserve modified files and report conflicts.
4. Require explicit resolution before overwriting a user's modifications.

### Exclusions and Hooks

- An agent's conversation history, mutable login state, and self-managed databases are excluded from the dotfile manifest.
- Restrict bootstrap hooks to approved build/initialisation actions; no package installation hook should bypass the declared profile build.

### Security & Privacy

- Personal preferences may be writable. Security-sensitive platform configuration stays outside the user-owned home and cannot depend on chezmoi enforcement.
- chezmoi hooks are executable input requiring server-side authorisation.

## Open Questions

- [ ] Decide whether personal file updates need a built-in three-way merge or initially an explicit manual conflict workflow. `chezmoi status` reports a user-edited managed file as `MM` ([Gate C](../research/2026-10-gate-c.md)); applying a newer revision is untested.

## References

- [Guest](./guest.md)
- [Configuration](./configuration.md)
- [chezmoi](https://www.chezmoi.io/what-does-chezmoi-do/)
- [chezmoi templating](https://www.chezmoi.io/user-guide/templating/)
