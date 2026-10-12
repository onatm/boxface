# Configuration Model

**Status:** Proposed — decisions confirmed by the owner on 2026-10-12; blocks the 0.1.0 release

**Roadmap:** [Usable configuration and operations](../ROADMAP.md#usable-configuration-and-operations)

**Design:** [Web UI](../designs/web-ui.md), [Boxface API](../designs/api.md), [CLI](../designs/cli.md), [Access and Sessions](../designs/access-and-sessions.md), [Personal Configuration](../designs/personal-configuration.md), [Guest](../designs/guest.md)

---

## Goal

An owner can tell what each configuration page is for, where to change something, and which settings a new environment gets. Every page and API resource belongs to one of four groups with one name each, revoking a device removes all of its access, and dotfiles are rendered at profile build time instead of executing in the guest. The API and CLI names are settled before 0.1.0 makes them a public contract.

## Current State

- The web UI has eight flat pages: Setup, Profiles, Images, Defaults, SSH keys, Credentials, Clients, and Environments.
- `main` is both an image alias and a profile name; `owner_defaults` points at both, and the Setup checklist asks for each.
- Defaults (`/v1/defaults`, `owner_defaults`) holds image, profile, vCPUs, memory, and disk: a template with no name. The Defaults page also shows host limits.
- `boxfacectl login` creates an API token (`api_tokens`, named after the device's hostname, with `last_used_at`) and then registers the device key in `ssh_keys`, which has no link to the token. Clients lists tokens and SSH keys lists keys; revoking a client leaves its SSH key accepted by environments at their next start.
- Disk usage and the build-cache control are on the Images page; capacity limits are on Defaults.
- A profile's dotfiles repository is cloned in the builder and copied raw onto the profile drive. On the first boot of each data disk, `boxface-personalise` runs `chezmoi apply` in the guest, once; later dotfile revisions never reach the home. Every profile must include `chezmoi` ([`spec.go`](../../internal/profiles/spec.go)).

## Decisions

| Decision | Choice | Reason |
|---|---|---|
| Grouping | Overview; **Environments** (Environments, Template, Credentials); **Baseline** (Images, Profiles); **Access** (Devices); **Host** | Each group answers one question: what runs, what it is built from, who gets in, and what the server has left |
| Profile | Keep the name "Profile" | Owner decision |
| Images | Keep the name; the UI says "base image" where a reader could confuse it with a profile | An image is the system layer, a profile the user layer; together they are the baseline, as in the architecture |
| Template | Defaults becomes the `default` template: base image, profile, vCPUs, memory, disk. Only `default` exists for now; `boxfacectl create NAME` uses it | One recipe with a name, so the next step (named templates) needs no rename. Owner decision: default only |
| One `main` | The template is the only "default" concept. The image alias `main` and the profile name `main` stay as the names the `default` template points at, and the UI shows them as the template's choices instead of as separate setup steps | Removes the two-`main` confusion without breaking existing aliases |
| Devices | Clients and SSH keys merge into Devices. `ssh_keys` gains `token_id` (nullable). Keys added with a device token are linked to it; keys pasted in the web UI are not. Revoking a device revokes its token and deletes its linked keys | One row per device, and revocation removes all of a device's access |
| Tailscale | The bound Tailscale login moves to the Devices page, under "Sign-in" | It is a way to get in, like a device |
| Host | Disk usage, build cache, capacity limits, and version on one Host page | Server state, not configuration |
| Overview | The Setup checklist until it is complete, then an environment summary and host health | The first page stays useful after setup |
| Dotfiles | chezmoi runs in the builder VM, not the guest ([option B](#dotfiles-in-the-builder)) | Executable input stays in the isolated builder, the result is recorded, and chezmoi leaves the image and profile requirements |
| Release | Every rename lands before 0.1.0 is tagged | API paths and CLI flags become a contract at the first release |

### API

| Today | After |
|---|---|
| `GET`/`PUT /v1/defaults` | `GET /v1/templates`, `GET`/`PUT /v1/templates/default` |
| `GET /v1/tokens`, `DELETE /v1/tokens/{id}` | `GET /v1/devices`, `DELETE /v1/devices/{id}` (revokes the token and deletes its linked keys) |
| `GET`/`POST /v1/ssh-keys`, `DELETE /v1/ssh-keys/{id}` | Unchanged; each key reports `device_id` when linked |
| `POST /v1/login-codes`, `/v1/identities` | Unchanged |
| `GET /v1/storage`, `GET /v1/capabilities` | Unchanged; shown together on Host |

The old paths are removed rather than aliased: nothing outside this repository uses them before 0.1.0.

### Migration

- `owner_defaults` rows become the `default` template.
- `ssh_keys.token_id` is backfilled where an unrevoked token and a key of the same owner share a name (the device hostname both were registered under). Unmatched keys stay unlinked and are shown as keys added by hand.

## Dotfiles in the Builder

The profile build renders the dotfiles once, in the builder VM, into a plain file tree on the profile drive. The guest copies that tree; it never runs chezmoi.

| Part | Change |
|---|---|
| Builder | After cloning, run chezmoi (pinned with the builder's Nix) to render the target state into `boxface/home/` on the profile drive, plus a manifest of each file's path, mode, and digest. Scripts (`run_` files and hooks) are excluded. A template that calls a password manager or other credential source fails the build with a message pointing to Credentials |
| Profile spec | `chezmoi` leaves the required packages (`git` and `tmux` stay); a profile can still include it for interactive use |
| Guest | A small seeder replaces `boxface-personalise`: on the first boot of a data disk it copies `boxface/home/` into the home, skipping files that already exist, and records the profile revision and manifest under `~/.local/state/boxface/` |
| Image contract | Unchanged, except that the image no longer needs a profile with chezmoi |
| Updates | Existing environments keep their profile revision; updates are explicit, as for images. Because each revision's rendered tree and manifest are kept, a later "update profile" action can merge three ways (the previous rendered file, the new rendered file, and the user's current file): files the user did not change are updated and changed files are reported as conflicts. The update action itself belongs to [Data Lifecycle](../designs/data-lifecycle.md) and is out of scope here |

Trade-offs accepted: templates see the builder's hostname and OS, not the environment's; chezmoi externals download at build time and are recorded only by the build log; chezmoi scripts do not run at all.

## Scope

**In scope:** the API and migration changes above, Devices with linked keys and revocation, the web UI regrouped into Overview, Environments, Baseline, Access, and Host, rendering dotfiles in the builder and seeding them in the guest, and updates to the README and the affected design docs.

**Out of scope:** named templates beyond `default`, `boxfacectl` template or device commands, environment actions in the web UI, applying a new profile revision to an existing environment, and private dotfiles repositories.

## Milestones

| Step | Work | Verify |
|---|---|---|
| M0. Spike | Run `chezmoi archive` or `chezmoi apply --destination` against a chezmoi repository with templates, a `run_` script, an external, and a password-manager template, and against a plain repository whose files start with `.` | Recorded in `docs/research/`: what each produces, how scripts are excluded, how a credential template fails, and what a plain repository yields |
| M1. Templates | Migration; `GET /v1/templates`, `GET`/`PUT /v1/templates/default`; `create` reads the `default` template; `/v1/defaults` removed | Tests: an existing `owner_defaults` row becomes `default`; `create` without flags uses it; flags still override it |
| M2. Devices | Migration with `token_id` and backfill; keys added with a device token are linked; `GET`/`DELETE /v1/devices`; `/v1/tokens` removed | Tests: login links the device key; revoking a device revokes the token and deletes its keys; a hand-added key survives every revocation; another owner's device cannot be revoked; the backfill links only same-owner, same-name pairs |
| M3. Web UI | Grouped navigation; Overview, Template, Devices, and Host pages; Images and Profiles under Baseline; the checklist asks for the template's image and profile instead of two `main`s | Every page reachable from the navigation at phone width; no page shows host state except Host and Overview; the Web UI design doc's page list matches |
| M4. Dotfiles | Builder renders and records the tree; seeder in the base image; `chezmoi` removed from required packages; `boxface-personalise` removed from `images/guest/` | Integration test on the host: a chezmoi repository with a template seeds rendered files; a `run_` script does not run; a credential template fails the build with its message; a second boot changes nothing; a user-edited file survives a restart |
| M5. Docs | README, Web UI, API, CLI, Access and Sessions, Personal Configuration, Guest, and Architecture updated to the new names | No doc names Defaults, Clients, or `/v1/defaults` outside plans and research |

## Open Questions

- What a plain dotfiles repository (files named `.bashrc`, not `dot_bashrc`) should do: copy it as it is, or fail the build with a message. The M0 spike answers what chezmoi does with it; the owner decides after.
