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

- [ ] Decide whether personal file updates need a built-in three-way merge or initially an explicit manual conflict workflow.

## References

- [Guest](./guest.md)
- [Configuration](./configuration.md)
- [chezmoi](https://www.chezmoi.io/what-does-chezmoi-do/)
- [chezmoi templating](https://www.chezmoi.io/user-guide/templating/)
