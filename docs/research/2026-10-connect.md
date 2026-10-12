# Install, Configure, Connect: Tailscale Serve Spike

**Date:** 2026-10-10

**Result:** Pass (C0 spike and C5 client); `tailscale serve` to a Unix socket carries the API, its event streams, and the SSH upgrade tunnel, so `tsnet` is not needed

**Scripts:** `experiments/connect-spike/` (`host.sh` on the host, `client.sh` on a tailnet client)

---

## Summary

A spike server behind `tailscale serve --https=8443 unix:/run/boxface-spike/api.sock` passed every C0 check.
- Tailscale identity headers arrive, and a forged one is replaced.
- Server-sent events are delivered one at a time, not buffered.
- A hand-written HTTP/1.1 `Upgrade: boxface-ssh` tunnel carries SSH, with the spike's `tunnel` mode as `ProxyCommand`.
- SSH sessions through the tunnel stayed open for 2 hours idle, with and without client keepalives.
- After a network switch, reconnecting found the tmux session and its process still running.

The main concern was reports of upgraded connections dropping behind `tailscale serve` ([tailscale#18827](https://github.com/tailscale/tailscale/issues/18827)). That did not happen here, so C4 builds the gateway on the Unix socket listener from C2.

## Environment

| Item | Value |
|---|---|
| Host | DigitalOcean Basic droplet `ubuntu-s-2vcpu-4gb-nyc1`, Ubuntu 24.04.5 LTS, Tailscale 1.104.1 |
| Client | macOS, Tailscale 1.102.4, in Europe; the host is in New York |
| Path | Client → `https://<host>.<tailnet>.ts.net:8443` → `tailscale serve` → Unix socket (mode 0600, root) → spike server → host `sshd` on `127.0.0.1:22` |
| Device | User-owned (not tagged), so identity headers are set |

The spike was to run with the host's public address firewalled off. It was left open, but every check used the tailnet URL, which is reachable only through Tailscale. The public-address path is covered in C7.

`tailscale serve` had to be enabled for the tailnet once in the admin console. The first `host.sh start` printed the link to do so.

## Results

| Check | Result |
|---|---|
| Identity headers | Pass: `Tailscale-User-Login` arrived as `onatm@github` |
| Forged identity | Pass: a client-sent `Tailscale-User-Login: forged@example.com` was replaced with the real login |
| Server-sent events | Pass: six events arrived 1.00 s apart. The client spoke HTTP/2 to `tailscale serve` and the backend saw HTTP/1.1 |
| SSH through the upgrade tunnel | Pass: connect and run a command in 2.1 s, across the Atlantic |
| 1 MiB round trip | Pass: bytes identical |
| Idle 2 hours, no keepalive (`ServerAliveInterval 0`) | Pass: survived 7200 s; the server log shows the tunnel closed by the client at 2h0m1s, when the command finished |
| Idle 2 hours, `ServerAliveInterval 15` | Pass: survived 7200 s; closed at 2h0m1s by the client |
| Network switch with tmux reattach | Pass: after switching networks the session dropped (server log: a 2m10s tunnel closed); reconnecting found the same tmux session with the process still running |

## Consequences

- The gateway uses `tailscale serve` to the API's Unix socket; `tsnet` stays a fallback only.
- The client forces HTTP/1.1 through ALPN for the tunnel, since upgrades through `tailscale serve` work only on HTTP/1.1 ([tailscale#20882](https://github.com/tailscale/tailscale/issues/20882)). Other API calls may use HTTP/2.
- `connect` (C5) should still set `ServerAliveInterval 15`. Idle sessions survived without it, but keepalives detect a dead network sooner after a switch.
- Identity headers can be trusted on the Unix socket. They are replaced by `tailscale serve`, and only root can connect to the socket.

## Client Connect (C5)

**Date:** 2026-10-10. **Result:** Pass, after one fix.

Same droplet and client. The current `boxface-api` and `boxface-worker` were installed with `-tailscale-socket /var/lib/boxface/api/tailscale.sock`, published with `tailscale serve --bg unix:...` on port 443. The Gate C image was rebuilt with `tmux` 3.6a in the profile; the profile and image build took about 3 minutes with a warm Nix store. Every command used the macOS build of `boxfacectl` against `https://<host>.<tailnet>.ts.net`.

| Check | Result |
|---|---|
| `login URL --code` | Pass: logged in; device key generated and registered as the Mac's host name |
| `image import tmux --alias main` | Pass: 38 s, mostly hashing the 6.7 GiB root filesystem |
| `create work` with no flags | Pass: running in 7.0 s with the defaults (2 vCPUs, 2048 MiB, 20 GiB) |
| `connect work` to a stopped environment | Pass: started it (`create_network`, `start_vm`, `wait_ready`), then attached to tmux session `main` with strict host-key checking against the pinned key |
| Reconnect to a running environment | Pass: 3.2 s from command to tmux |
| Client killed inside the session | Pass: `sleep 100000` kept running in session `main`; the next `connect` reattached to the same pane |
| Ctrl-C while `connect` starts the environment | Pass: exit 130 with the resume command printed; the start finished on the server |
| Network switch inside the session | Pass: after switching Wi-Fi to a phone hotspot, `connect` again found `top` still running |
| Audit | Pass: each tunnel recorded as `environment.connect` with the environment ID and the client's tailnet address, taken from `X-Forwarded-For` on the socket |

**Fix found by this run:** on macOS the config directory is `~/Library/Application Support/boxface`. ssh splits `UserKnownHostsFile` on whitespace, so the pinned key was never read and strict checking refused the first connection. `connect` now quotes the path, and the CLI test uses a config path containing a space.

## Acceptance (C7)

**Date:** 2026-10-10. **Result:** A1–A8 pass on the droplet, with one design gap found: a device key registered after an environment is created is not accepted by that environment.

### Setup

The droplet's Boxface state was wiped (services and VMs stopped, `/var/lib/boxface`, the service override, and the `tailscale serve` config removed); Firecracker, Tailscale, and the built image in `/var/tmp` stayed. `deploy/install.sh` from commit `f5bf337` installed both services, then the API was exposed both ways at once:

```ini
ExecStart=/usr/local/bin/boxface-api serve -tailscale-socket /var/lib/boxface/api/tailscale.sock -tls-listen 143.244.155.78:443 -tls-domain 143-244-155-78.sslip.io
AmbientCapabilities=CAP_NET_BIND_SERVICE
```

`-tls-listen` names the public address, because `tailscaled` already listens on port 443 of the tailnet address for `tailscale serve`. Let's Encrypt issued a certificate for `143-244-155-78.sslip.io` (issuer `YE1`) through TLS-ALPN-01 on the first request, which took 3.2 s. Within minutes, internet scanners were connecting to port 443 without a server name and were refused by `autocert`.

Clients were the macOS build of `boxfacectl` with fresh config directories whose paths contain a space, and headless Chromium for the web UI.

### Results

| ID | Result |
|---|---|
| A1 | Pass. `install.sh` printed the setup URL with a code. Over public HTTPS, the web UI registered the staged image (35.7 s, mostly hashing) and made it `main`, added an SSH key, saved defaults (disk 10 GiB), and issued a CLI login code; the only host step was staging the image. The first browser attempt used the setup code and then failed in the test script (a selector clicked the Setup page link instead of the Register button), so the run continued with a code from `boxface-api bootstrap`, the documented fallback |
| A2 | Pass over public TLS: `login URL --code`, `create travel` (running in 7.5 s), and `connect travel` landed in tmux session `main` as `dev`, with no host lookups or host-key prompts |
| A3 | Pass over public TLS: the client was killed while `sleep 100000` ran; `connect travel` reattached to the same pane with the process still running (pid 1346) |
| A4 | Pass: after `stop travel`, `connect travel` started it (`create_network`, `start_vm`, `wait_ready`) and reached the prompt in 8.5 s; the pinned key in `known_hosts` was unchanged and strict checking passed |
| A5 | Pass: tunnel requests for another owner's environment (`not_found`), with a revoked token (`unauthorized`), for a deleted environment (`environment_deleted`), and for a stopped one (`environment_not_running`) were refused; the count of `environment.connect` audit records stayed at 4, so no tunnel opened |
| A6 | Pass, with Tailscale running on the host: from the guest, the client Mac's tailnet address, MagicDNS (`100.100.100.100`), the host's tailnet address (where `tailscale serve` listens), the VPC gateway and the host's VPC address, the cloud metadata service (`169.254.169.254`), the host through the guest gateway, and the host's public address were all dropped; `1.1.1.1:443` and `https://deb.debian.org` were reachable |
| A7 | Pass. Over `tailscale serve`: the Tailscale login was bound from the web UI's Clients page, after which a new browser signed in with no code and `boxfacectl login https://<host>.ts.net` needed no code; A2 and A3 passed again (session `main`, `sleep 200000` survived a killed client). On the TLS listener, a forged `Tailscale-User-Login` was ignored by `POST /v1/login`, `POST /web/login` (both `401`), and `GET /web/session` |
| A8 | Pass on the TLS listener with a valid session cookie: cross-site and foreign-`Origin` `POST`s, and a cross-site sign-out, returned `403`; the same `POST` from the same origin returned `201`. No CORS headers are sent, so a cross-site page cannot read `GET` responses |

### Measurements

| Measurement | Value |
|---|---|
| Install to first connect, fresh host | 7 min 14 s as run, including about 5 minutes lost to the A1 test-script timeout; about 2 min 15 s without it, of which 36 s is image hashing |
| Connect to prompt, running environment, public TLS | 1.50–1.57 s (5 runs) |
| Connect to prompt, running environment, `tailscale serve` | 1.53–1.55 s (5 runs) |
| Connect to a stopped environment, to prompt | 8.5 s |
| Create with defaults | 7.5 s |
| First TLS request with ACME issuance | 3.2 s |

Both exposures meet the provisional 3-second attachment target. Tunnel survival across idle periods and a network change is in the C0 and C5 sections above (through `tailscale serve`); idle survival over public TLS was not measured.

### Finding: a new device cannot connect to existing environments

A second client config logged in over Tailscale with no code, generated and registered its own device key, and then `connect travel` failed with `Permission denied (publickey)`: `travel` was seeded with the keys registered when it was created. This followed the scope at the time (updating SSH keys inside existing environments was deferred), but every additional device, such as a tablet next to a laptop, hits it on every environment created before it logged in. The CLI also reports it as "connection lost or refused", which does not point at keys. Copying the first device's key into the second config made the rest of A7 pass.

**Resolution (same day):** keys now reach the VM on a read-only drive at every start. On the droplet, with the image rebuilt: a second device that logged in over Tailscale ran `connect multi` against a running environment and got "multi does not accept this device's key yet" with the restart command; after `stop multi`, its `connect` started `multi` and landed in tmux `main`, and the first device still connected. The worker integration test shows a key passed at start accepted, a key left out at start refused, and a key registered while the VM ran refused until its next start. `travel`, created on the previous image, still connected with its create-time keys.
