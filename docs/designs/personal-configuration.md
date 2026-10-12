# Personal Configuration

**Status:** Draft — proposed design

---

## Overview

Dotfiles in chezmoi format are rendered once, when a profile revision is built, and seeded as plain files into a new environment's home; later profile updates must not overwrite the user's modifications or agent state.

## Context & Problem

**Background:** Home Manager owns packages and chezmoi owns personal files; both must never manage the same files. Agents write authentication and conversation state into the home, and users edit their own configuration. A reboot or profile update must not destroy either.

## Goals

- Seed a new home from a pinned dotfiles commit
- Update only files the user has not changed
- Never overwrite agent-managed state

## Technical Design

### Seeding

The profile builder renders the dotfiles repository with `chezmoi archive --exclude=scripts,encrypted` inside the builder VM, as UID 1000 `dev` with `HOME=/home/dev` and hostname `boxface`, so templates see what the guest would. Rendering runs the dotfiles' templates and `modify_` scripts, which is why it happens only in the isolated builder, never in the API, on the host for profiles, or in the guest ([render spike](../research/2026-10-dotfiles-render.md)). `run_` scripts never run; encrypted files are skipped and listed in the build log; a template that reads a password manager fails the build with a pointer to Credentials.

The result goes on the profile drive as `boxface/home/` (readable by every user) and `boxface/home.manifest`, one line per entry: kind (`dir`, `file`, `create`, `modify`, `external`, `symlink`), mode, SHA-256 digest or link target, and path. A repository without chezmoi naming is copied as it is: its top-level dot entries except `.git`, `.github`, `.gitignore`, `.gitmodules`, and `.gitattributes`. `images/image.sh` renders `images/dotfiles/` the same way on the host, with chezmoi from the toolset's pinned nixpkgs.

On the first boot of a data disk, `boxface-personalise`, a user service, follows the manifest: it creates missing directories with their recorded modes, copies each file with its mode, replaces links Home Manager generated into `/nix`, and skips any file the user already has. It records the revision, what it seeded and skipped, and a copy of the manifest under `~/.local/state/boxface/`. The guest needs no chezmoi. Reboot is not permission to overwrite the home.

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
- chezmoi templates, `modify_` scripts, and externals execute or fetch during rendering, in the builder VM only; `run_` scripts and hooks never run.

## Open Questions

- [ ] Decide whether personal file updates need a built-in three-way merge or initially an explicit manual conflict workflow. Each revision's manifest records digests, so a three-way merge (previous rendered file, new rendered file, the user's file) needs no chezmoi in the guest; `create_` files are never updated. `chezmoi status` reports a user-edited managed file as `MM` ([Gate C](../research/2026-10-gate-c.md)); applying a newer revision is untested.

## References

- [Guest](./guest.md)
- [Configuration](./configuration.md)
- [chezmoi](https://www.chezmoi.io/what-does-chezmoi-do/)
- [chezmoi templating](https://www.chezmoi.io/user-guide/templating/)
