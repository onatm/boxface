# Web UI

**Status:** Draft — configuration pages implemented; recipes and environment actions proposed

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

### Implemented Pages

`boxface-api` serves the UI at `/` from static HTML, CSS, and JavaScript embedded in the binary, with no build step. It calls the `/v1` API with `fetch`, like the CLI. The navigation groups the pages; old addresses (`#setup`, `#defaults`, `#clients`, `#keys`) open their replacements.

| Group | Page | Contents |
| --- | --- | --- |
| | Overview | Until setup is complete, a checklist: a base image for the template's image, the template's profile built, the template saved, and a device logged in with its SSH key. Afterwards, the `boxfacectl` commands, an environment count by state, and free disk space |
| Environments | Environments | Read-only list with state, whether `main` has moved since creation, each environment's profile revision and whether a newer one is ready, and the `connect` command |
| | Template | The `default` template: base image, profile, vCPUs, memory, and disk that `boxfacectl create NAME` uses |
| | Credentials | Save credentials from presets or a custom binding; replace or delete; values never shown |
| Baseline | Images | Register staged imports; move `main`; remove unused images. Registering the first image also points `main` at it |
| | Profiles | List with each profile's latest revision and state, and which one the template uses; an editor with a Packages tab of nixpkgs names and a Home Manager module tab (packages, generated files such as `programs.git`, and session variables reach the environment; activation scripts do not), an unfree switch, and a public dotfiles repository; "Save and build" adds a revision, and the revision history shows each build's state, error, and log tail, refreshing while one runs; "Use in template" sets the template's profile |
| Access | Devices | Issue a CLI login code with the exact `boxfacectl login` command; logged-in devices with their SSH key fingerprints, revoked together; other SSH keys added by hand; bind or unbind a Tailscale login |
| | Host | Disk usage and the build-cache control, capacity limits, and the server version |

### Security & Privacy

- Sign-in is a one-time code (from `install.sh`, `boxface-api bootstrap`, or the Devices page), exchanged by `POST /web/login` for a session cookie, or a bound Tailscale login on the Tailscale socket, which signs in automatically. `/login?code=…` signs in and removes the code from the address bar.
- Sessions are stored as SHA-256 hashes in `web_sessions`, last 30 days, and end on sign-out. The cookie is `HttpOnly`, `SameSite=Strict`, and `Secure` over HTTPS or Tailscale. Session secrets are not API tokens and API tokens are not accepted as cookies.
- Every API request goes through Go's `http.CrossOriginProtection`, which rejects cross-origin state-changing requests from browsers; the CLI sends no browser headers and is unaffected.
- Pages are served with a `default-src 'self'` content security policy, `frame-ancestors 'none'`, `no-referrer`, and `no-store`. The UI builds the DOM without HTML strings, so API data is never parsed as markup.
- It never displays secret values other than a login code the owner just created.

## References

- [Boxface API](./api.md)
- [boxfacectl](./cli.md)
