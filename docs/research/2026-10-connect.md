# Install, Configure, Connect: Tailscale Serve Spike

**Date:** 2026-10-10

**Result:** Pass; `tailscale serve` to a Unix socket carries the API, its event streams, and the SSH upgrade tunnel, so `tsnet` is not needed

**Plan:** [Install, Configure, Connect](../plans/2026-10-connect-and-web-ui.md#milestones), step C0

**Scripts:** `experiments/connect-spike/` (`host.sh` on the host, `client.sh` on a tailnet client)

---

## Summary

A spike server behind `tailscale serve --https=8443 unix:/run/boxface-spike/api.sock` passed every C0 check.
- Tailscale identity headers arrive, and a forged one is replaced.
- Server-sent events are delivered one at a time, not buffered.
- A hand-written HTTP/1.1 `Upgrade: boxface-ssh` tunnel carries SSH, with the spike's `tunnel` mode as `ProxyCommand`.
- SSH sessions through the tunnel stayed open for 2 hours idle, with and without client keepalives.
- After a network switch, reconnecting found the tmux session and its process still running.

The plan's main concern was reports of upgraded connections dropping behind `tailscale serve` ([tailscale#18827](https://github.com/tailscale/tailscale/issues/18827)). That did not happen here, so C4 builds the gateway on the Unix socket listener from C2.

## Environment

| Item | Value |
|---|---|
| Host | DigitalOcean Basic droplet `ubuntu-s-2vcpu-4gb-nyc1`, Ubuntu 24.04.5 LTS, Tailscale 1.104.1 |
| Client | macOS, Tailscale 1.102.4, in Europe; the host is in New York |
| Path | Client → `https://<host>.<tailnet>.ts.net:8443` → `tailscale serve` → Unix socket (mode 0600, root) → spike server → host `sshd` on `127.0.0.1:22` |
| Device | User-owned (not tagged), so identity headers are set |

The plan asked for the host's public address to be firewalled off. It was left open, but every check used the tailnet URL, which is reachable only through Tailscale. The public-address path is covered in C7.

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

## Consequences for the Plan

- The gateway uses `tailscale serve` to the API's Unix socket; `tsnet` stays a fallback only.
- The client forces HTTP/1.1 through ALPN for the tunnel, since upgrades through `tailscale serve` work only on HTTP/1.1 ([tailscale#20882](https://github.com/tailscale/tailscale/issues/20882)). Other API calls may use HTTP/2.
- `connect` (C5) should still set `ServerAliveInterval 15`. Idle sessions survived without it, but keepalives detect a dead network sooner after a switch.
- Identity headers can be trusted on the Unix socket. They are replaced by `tailscale serve`, and only root can connect to the socket.
