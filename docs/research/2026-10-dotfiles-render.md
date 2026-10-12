# Rendering Dotfiles at Build Time

**Date:** 2026-10-12

**Result:** Pass, with three changes to the design: rendering executes code, so it belongs in the builder only; the builder must render as `dev` with the guest's hostname; plain repositories need their own handling

---

## Summary

`chezmoi archive` renders a chezmoi repository into a tar of the target state without touching a home directory or writing chezmoi state, and two renders of the same input produce identical content. It keeps file modes, private directories, executable bits, and symlinks, honours `.chezmoiignore` and `.chezmoiroot`, and fetches externals. `--exclude=scripts` keeps `run_` scripts out of the result, but rendering still executes code: template functions such as `output` and every `modify_` script run during `archive`, with or without that flag. A template that reads a password manager fails the render with a clear error, and an encrypted file fails it unless excluded. A plain dotfiles repository (files named `.bashrc`) renders to almost nothing: chezmoi ignores every source name that starts with a dot, so only `README.md` remains, and the command still succeeds.

## Environment

| Item | Value |
|---|---|
| chezmoi | 2.70.3, from the builder's pinned nixpkgs (`7c8764b7`) |
| Runner | `nixos/nix:2.24.10` container on an arm64 Mac, as root unless noted |
| Fixtures | A chezmoi repository with a plain file, a template using `.chezmoidata.yaml` and `.chezmoi.*`, `private_`, `executable_`, `create_`, `symlink_`, and `modify_` entries, an `output` template, `run_once_before_` and `run_onchange_after_` scripts, a `.chezmoiexternal.toml` file, and `.chezmoiignore`; separate repositories with a `onepasswordRead` template, an `encrypted_` file, an `env` template, and `.chezmoiroot`; and a plain repository with `.bashrc`, `.gitconfig`, `.config/nvim/init.lua`, and `README.md` |

## Results

| ID | Check | Result |
|---|---|---|
| T1 | `archive`, default options | Exit 0. `run_` scripts are not executed but are included as files at the archive root (`install.sh`, `x.sh`, mode 0700), so they would be copied into the home |
| T2 | `archive --exclude=scripts` | Exit 0. Scripts gone; `.gitconfig` rendered from `.chezmoidata.yaml`; `.ssh` is 0700, `.local/bin/hello` executable, `.linked` a symlink; the external was downloaded. The `output` template and the `modify_` script **ran** (marker file written, `.modified` holds the script's output) |
| T3 | `archive --exclude=scripts,externals` | Exit 0. External gone; `output` and `modify_` still ran |
| T4 | `apply --destination DIR --exclude=scripts` | Same files as T2, and it writes chezmoi state under `$HOME`. `archive` needs neither |
| T5 | Plain repository, `archive` | Exit 0 with one entry, `README.md`. Every dot-named file was ignored |
| T6 | Plain repository, `managed` | `README.md` only |
| T7 | `onepasswordRead` template | Exit 1: `error calling onepasswordRead: op signin --raw: exec: "op": executable file not found in $PATH` |
| T8 | `encrypted_` file without a key | Exit 1: `encryption not configured`. With `--exclude=encrypted`, exit 0 and the file is left out |
| T9 | `env "HOME"` template | Renders the renderer's environment |
| T11 | `managed --exclude=scripts,externals --path-style=source-relative` | Lists source names, including `create_settings.json` and `modify_dot_modified`, so `create_` and `modify_` entries can be identified for the manifest; `archive` itself writes `create_` files as ordinary files |
| T12 | `.chezmoiroot` naming `home` | Renders `home/dot_rooted` as `.rooted`; the repository's `README.md` outside the root is not included |
| T13 | Render as UID 1000 `dev` with `HOME=/home/dev` | `.chezmoi.username` = `dev`, `.chezmoi.homeDir` = `/home/dev`; `.chezmoi.hostname` is the renderer's hostname. As root (T2) the username was `root` |
| T14 | Two renders of the same input | Identical content digests; only archive timestamps differ |

## Findings

1. **Rendering is code execution.** Template functions (`output`, and any other that runs a command) and `modify_` scripts execute during `archive`. Excluding scripts removes only `run_` scripts. Rendering is therefore safe only where the dotfiles' code may run: in the builder VM, which has no credentials and is already isolated for Nix builds. It must never move back into the API or the host.
2. **Render as the guest user.** Run as root, templates see `root` and the builder's home; dotfiles that branch on the username or home render wrongly. The builder should run chezmoi as UID 1000 `dev` with `HOME=/home/dev`, and with the guest image's hostname (`boxface`; the builder's own is `boxface-builder`).
3. **Credential sources fail cleanly.** Password-manager templates fail with chezmoi's error. The builder should add one line naming Credentials as the way to give environments secrets.
4. **Encrypted files need a choice.** Failing on `encrypted_` files blocks any repository that keeps some; `--exclude=encrypted` drops them quietly. The builder should exclude them and list the excluded paths in the build log.
5. **Plain repositories are silently empty.** A repository without chezmoi naming yields only its non-dot files, so a `README.md` would land in the home and nothing else. Boxface must detect this (the repository has dot-named files outside `.git` and `.chezmoi*`, and chezmoi manages none of them) rather than report success.
6. **`create_` and `modify_` need recording.** `archive` loses the distinction. `managed --path-style=source-relative` recovers it, so the manifest can mark `create_` files (seed only, never update) and `modify_` files (rendered once at build time from an empty file, which is not what the user expects for a merge) for the later update action.
7. **Externals are fetched at build time** and recorded only in the log. They are allowed, since the builder has network access for Nix, and `--exclude=externals` remains available if a profile should not fetch them.

## Design Changes

- Replace "scripts never run" with: `run_` scripts are excluded, and templates and `modify_` scripts run in the builder only.
- Render with `chezmoi archive --exclude=scripts,encrypted` as `dev`, `HOME=/home/dev`, hostname `boxface`; build the manifest from `managed --path-style=source-relative`.
- Plain repositories: detect them as in finding 5 and either copy them as they are or fail with a message; the owner decided to copy them as they are.

## Host Run

**Date:** 2026-10-12, on the droplet, services at `3d4b5bd` with the fixes below; base and builder images from `images/profile-images.sh` (kernel and mkosi checkout reused from Gate A and Gate C).

| Check | Result |
|---|---|
| Base image contract check | Failed first on a correct image: enable links are absolute (`/etc/systemd/system/…`), and `[ -e ]` followed them on the build host. Fixed to test the link itself; the base image then passed |
| Plain repository (`mathiasbynens/dotfiles`) | Profile built; 25 top-level dotfiles copied; an environment seeded them, including `.vim/` contents, and left out `README.md` and `bootstrap.sh` |
| chezmoi repository (`twpayne/dotfiles`, commit `b74c1055`) | Failed first: `map has no entry for key "ephemeral"`, because its data comes from `.chezmoi.toml.tmpl`, which only `chezmoi init` evaluates. Fixed by running `init --promptDefaults` into a config file of the template's format, on a `dev`-owned copy of the source (init creates a `.git` there). Then: 15 files rendered; an environment started in 7 s and seeded them, with `user.name` from the computed config |
| Config template formats | `.chezmoi.yaml.tmpl` and `.chezmoi.toml.tmpl` both render with prompt defaults |
| Guest | No chezmoi in the environment; `boxface-personalise` and `boxface-home-files` active; the rendered tree on `/dev/vdd` is read-only |
| Device revocation | Revoking the test device removed its SSH key and its token returned 401 |
