# Installing the VPS agent

The VPS agent is the only part of Pelican Auto Proxy that needs a public IP address. It receives forwarding rules
from the plugin over HTTPS and applies them as nftables DNAT rules, and it terminates the WireGuard tunnel that every
node host dials into.

## Prerequisites

- A VPS with a public IPv4 address you control (most providers' cheapest tier is enough — the agent uses under
  20 MB of memory even with a thousand forwarded ports).
- Debian 12 or 13. The installer checks `/etc/os-release` and refuses anything else with a clear message, rather
  than half-installing on an unsupported system. Tested live on Debian 13; Debian 12 is expected to work but has not
  been tested yet. Ubuntu is not supported in this release (see [faq.md](faq.md#why-not-ubuntu-yet)).
- Root access (either logged in as root, or a user that can `sudo`).
- A kernel with WireGuard support — every current Debian kernel has this built in.
- Nothing else already listening on the ports you plan to use (SSH, and by default UDP 51820 and TCP 7443).

## Install

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-vps.sh | sudo bash
```

A minimal Debian image may have no `curl`. If the command above says `curl: command not found`, run
`sudo apt-get install -y curl ca-certificates` once and repeat it.

This downloads the pinned release's `autoproxy-agent` binary, verifies its checksum against `SHA256SUMS`, and runs
`autoproxy-agent setup`. Setup is idempotent — re-running it never regenerates an existing token, certificate or
WireGuard key, but it does refresh derived config (the systemd units, the base nftables table) so a changed flag
takes effect.

At the end it prints a boxed **VPS code** once, with a warning that it will not be shown again on this run, and
writes the same information to a root-only file.

### Flags

Pass flags after the script, or run `autoproxy-agent setup` directly with the same flags once the binary is on the
machine:

| Flag | Default | Meaning |
|---|---|---|
| `--api-port` | `7443` | TCP port the HTTPS API listens on. Change this only if something else on the VPS already uses it. |
| `--wg-port` | `51820` | UDP port the WireGuard tunnel listens on. |
| `--wg-subnet` | `10.66.66.0/24` | Address range for the tunnel. The VPS takes the first usable address; node hosts are assigned from the rest. |
| `--public-ip` | auto-detect | This VPS's public IPv4. Detected from the default route; pass it yourself if detection picks the wrong address. It goes into the API certificate and into every join code. |
| `--dns-name` | none | An extra DNS name to put in the API certificate, if you have a hostname for the VPS. |
| `--dry-run` | off | Print every change that would be made and touch nothing. |
| `--yes`, `-y` | off | Replace an `/etc/nftables.conf` that you have edited yourself. Not needed for the untouched default Debian/Ubuntu ships — see "your existing firewall" below. |

`--api-port` and `--wg-port` may not be the same port, and `--wg-subnet` must be a `/24` or larger with no host
bits set; setup refuses with a message rather than half-installing.

### Environment variables

Two, both read by the installer or by setup rather than passed as flags:

| Variable | Effect |
|---|---|
| `AUTOPROXY_VERSION` | Install this release tag instead of resolving the latest one. `AUTOPROXY_VERSION=v1.2.3 bash install-vps.sh` pins the download, which is how you control exactly when an update lands. |
| `AUTOPROXY_SSH_PORT` | Tell setup which port sshd listens on, when it could not ask sshd itself. Setup warns loudly when that happens and assumes 22; if sshd is elsewhere, abort and re-run with this set, or the base firewall will lock you out. |

SSH is never touched: the installer does not edit `sshd_config` and does not add or remove keys. It asks `sshd -T`
which port sshd actually listens on, then does two things with the answer — allows that port in the base firewall,
and reserves it, so a later forwarding rule cannot accidentally take it over. If sshd cannot be asked, setup says
so loudly and assumes 22; that is the moment to abort and re-run with `AUTOPROXY_SSH_PORT` set.

## What it changes on the machine

| Item | Path / name | Purpose |
|---|---|---|
| Binary | `/usr/local/bin/autoproxy-agent` | The agent itself, copied here so the systemd unit always resolves. |
| File | `/etc/autoproxy/agent.env` (mode 0600) | Token, listen address, public interface, reserved ports, WireGuard subnet/port, public IP. Read by the unit as its `EnvironmentFile`. |
| File | `/etc/autoproxy/tls/agent.crt` and `/etc/autoproxy/tls/agent.key` (key mode 0600) | Self-signed certificate and key the API serves; the certificate (not the key) is embedded in the VPS code. |
| File | `/etc/autoproxy/vps-code` (mode 0600) | The VPS code itself, so `autoproxy-agent show-code` can re-print it. It contains the API token. |
| File | `/etc/autoproxy/rules.nft` | The nftables file the agent owns and rewrites on every sync. Created empty at setup. |
| File | `/etc/wireguard/wg0.conf` (mode 0600) | The tunnel interface's static config (address, private key, listen port). It deliberately holds **no peers**: the agent adds those live. |
| File | `/etc/wireguard/autoproxy-server.key` / `.pub` | The VPS's WireGuard identity, generated once and never regenerated. |
| File | `/etc/nftables.conf` | The base firewall (see below). The first time setup replaces a file that is not already ours, it keeps the old one at `/etc/nftables.conf.pre-autoproxy`. |
| Directory | `/var/lib/autoproxy/` | `peers.json` (every node's public key, tunnel IP, mode) and `rules.json` (the last forwarding set applied), so a reboot restores both without waiting for the panel. |
| Systemd unit | `autoproxy-agent.service` | Runs `autoproxy-agent run`; `After=wg-quick@wg0.service`. |
| Systemd unit | `wg-quick@wg0.service` | Brings the tunnel interface up at boot. Enabled by setup. |
| nftables table | `inet autoproxy_base` | The base firewall from `/etc/nftables.conf`: an input chain with policy `drop` that allows loopback, established/related, ICMP, your sshd port, the WireGuard port and the API port. It has **no** forward chain on purpose — the agent's own table owns forwarding. |
| nftables table | `inet autoproxy_rules` | The DNAT maps the plugin's rules populate, the masquerade rule and the forward chain (policy `drop`); empty until the first sync. |
| sysctl | `net.ipv4.ip_forward=1` (in `/etc/sysctl.d/99-autoproxy.conf`) | Lets the kernel route tunnel traffic to forwarded targets. |

### Your existing firewall, and two things setup deliberately does

**It replaces `/etc/nftables.conf`.** Debian and Ubuntu both ship a skeleton `/etc/nftables.conf` in the `nftables`
package, and every fresh VPS has it. Setup asks dpkg for the checksum of the file that package installed: if yours
still matches it byte for byte — nobody has edited it — setup replaces it **without asking for `--yes`**, because
otherwise the one-command install would stop on every fresh VPS. If the file differs from the packaged default in
any way, it is treated as your firewall: setup refuses to write it, tells you to re-run with `--yes`, and carries
on with the rest of the install — your file is left exactly as it was, and the agent's own table still works, but
inbound traffic will not reach it unless your firewall already allows the API port and the game ports. When setup
*does* replace a file that was not already ours, it first keeps the old one at `/etc/nftables.conf.pre-autoproxy`
(it never overwrites an existing copy), and `autoproxy-agent uninstall` puts that back. If dpkg cannot be asked, or
its answer cannot be parsed, setup assumes the file is yours and refuses — the worst case is being asked for a
flag, never a firewall replaced silently.

**It does not change `rp_filter` on the VPS.** Reverse-path filtering stays exactly as your provider's image left
it. The VPS does not need it loosened: every address a peer may send from is listed in that peer's `AllowedIPs` and
is therefore routed back down `wg0`, so the strict reverse-path check passes on its own. (The node client *does*
set `rp_filter=2`, on its own tunnel interface only — that is a different machine and a different problem, see
[install-client.md](install-client.md).) Setup also adds no `PostUp`/`PreUp` hooks to `wg0.conf`: `wg-quick` treats
a failing hook as fatal and deletes the interface it just created, so one sysctl a container or a locked-down
provider kernel happens to refuse would take the whole tunnel down.

Beyond `/etc/nftables.conf`, nothing here touches a firewall you already had. If a prior provider firewall template
left other tables in place, they are left alone — see the "orphaned firewall tables" entry in
[troubleshooting.md](troubleshooting.md) if forwarding looks like it should work but doesn't. One exception, and it
is announced in the output: if Docker is **not** installed, setup deletes orphaned `ip nat` and `ip filter` tables
by exact name, because a removed Docker leaves a `FORWARD` chain with policy `drop` behind that silently swallows
every forwarded packet. With Docker installed, those tables are its own and are left alone.

## How to verify

```bash
sudo autoproxy-agent show-code       # confirms the agent is installed and re-prints the VPS code
systemctl status autoproxy-agent wg-quick@wg0
sudo wg show wg0                     # interface present, listening on the configured port, 0 peers on a fresh install
```

`autoproxy-agent show-code` also serves as the fastest health check: if it prints the code, the agent's own state and
config parsed cleanly.

## Re-printing the VPS code

If you lose the code before pasting it into the plugin, it is not gone — nothing needs to be regenerated:

```bash
sudo autoproxy-agent show-code
```

This reads the existing token, certificate and WireGuard public key from disk and reprints the same code. It does
not rotate anything, so a code you already pasted into the plugin stays valid.

## Undo

```bash
sudo autoproxy-agent uninstall
```

Prints exactly what it is about to remove — the `autoproxy-agent` and `wg-quick@wg0` units, the `inet
autoproxy_rules` and `inet autoproxy_base` tables, the `wg0` interface, `/etc/autoproxy`, `/var/lib/autoproxy`,
`/etc/wireguard/wg0.conf` and the server keys, `/etc/sysctl.d/99-autoproxy.conf`, the unit file and the binary — and
asks for confirmation before doing it. Pass `--yes` (or `-y`) to skip the prompt in a script.

It also puts `/etc/nftables.conf` back: restored from `/etc/nftables.conf.pre-autoproxy` if that copy exists, and
otherwise deleted if the file is one we wrote — which leaves the machine with **no firewall at all**, so install
your own. The `wireguard-tools` and `nftables` packages stay installed. SSH is never touched.
