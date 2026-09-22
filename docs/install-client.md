# Installing the node client

The node client runs on the machine that actually hosts the game server (real mode) or on a LAN gateway that
forwards to other machines (site mode, see [layouts.md](layouts.md)). It dials out to the VPS — the node host never
needs to be reachable from the internet itself.

You do not normally construct the install command by hand: the plugin's Setup page generates a **join
code** per node and shows the exact command to paste, in both flavours below. This page documents what that command
does and how to run it yourself if you are scripting an install.

## Is this node local or remote?

Answer this once per node, before you run anything. If the machine is a fresh Debian server that is not a Pelican
node yet, [docs/node-setup.md](node-setup.md) asks the same question and does the rest of the setup (Docker,
Wings, certificate) around it.

If the node's machine already has a public IP you control, it is a **remote** node and probably does not need the
proxy at all — players can connect to it directly. A node is not proxied until you mark it proxied on the plugin's
Setup page, so leaving one alone is the default rather than a setting you have to go and find. Mixed setups are
normal and supported: some nodes proxied, some not, on one VPS and one plugin install.

Reasons to proxy a remote node anyway:

- You want one public hostname or IP for every server, whichever node it happens to run on.
- You want the node's own IP to stay hidden. Players only ever see the VPS.
- The provider blocks the ports you need, or charges for them.

The install itself is identical either way. What differs is what you have to open at the provider.

## Local node

The node host has no usable public IP of its own: a home or office connection, or a machine behind a router that is
not yours to configure.

- The client runs on the Wings host, so real mode works and the game server sees each player's real IP.
- No inbound firewall rules on the node. The client dials out to the VPS; nothing has to reach the node from the
  internet.
- No router port forward. Avoiding that is the point of the tunnel.

## Remote node (rented server elsewhere)

A node on a rented server at another provider, with a public IP of its own.

- Same join command and the same binary as a local node. There is no separate remote flavour to install.
- Real mode still applies, because the client still runs on the Wings host. Players keep their real IP.
- The provider's security group needs exactly one outbound rule, and no inbound rules at all:

```
UDP out to <VPS IP>:51820
```

  (The install command downloads over HTTPS, so leave outbound HTTPS open while you run it.)

- Once traffic is proxied, the game ports on the node itself can be closed. Players connect to the VPS, and the VPS
  reaches the node through the tunnel.
- Latency: the tunnel adds the hop between the VPS and this node, so place the VPS close to the node host. See
  [What latency should I expect?](faq.md#what-latency-should-i-expect).

## Prerequisites

- Debian 12 or 13 (system-service flavour) — same OS gate as the VPS installer, refuses anything else with a clear
  message. Tested live on Debian 13; Debian 12 is expected to work but has not been tested yet. Ubuntu is not
  supported in this release (see [faq.md](faq.md#why-not-ubuntu-yet)).
- Root access.
- A join code from the plugin's Setup page. A join code does not expire and is not consumed by being used, so if
  the install fails partway through you can simply run it again with the same code. It carries that node's private
  key, so treat it like a password — see [security.md](security.md).
- Outbound UDP to the VPS's WireGuard port (51820 by default) and no other software already bound to the tunnel
  interface name (`autoproxy0`). That is the node host's interface; the VPS's tunnel interface is called `wg0`, and
  the two never need to match.

## Flavour 1: system service

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-client.sh | sudo bash -s -- <join code>
```

`<join code>` comes from the plugin — never type one from memory. This downloads and checksum-verifies the
`autoproxy-client` script and its systemd unit, then runs `autoproxy-client install <join code>`, which installs
`wireguard-tools`, `nftables`, `curl` and `iproute2`, decodes the join code, writes the tunnel and client config,
enables the service, and brings the tunnel up.

`install` takes three options after the join code: `--yes` to skip the "install on this machine?" confirmation
(already skipped when there is no terminal, which is the case when the command is piped from `curl`), `--no-start`
to install and enable the service without starting it, and `--host-ip <LAN address>` (see below).

## Flavour 2: Docker Compose

For hosts that already run everything through Docker. The plugin's Setup page shows the exact snippet with your join
code filled in; the shape is:

```yaml
services:
  autoproxy-client:
    image: ghcr.io/finnwastakenwastaken/autoproxy-client:latest
    network_mode: host
    cap_add:
      - NET_ADMIN
    restart: unless-stopped
    environment:
      AUTOPROXY_JOIN_CODE: <join code>
      # Only if this host's allocations are bound to a LAN address; see --host-ip below.
      # AUTOPROXY_HOST_IP: 192.0.2.50
```

The container never runs `install`: on first start it reads `AUTOPROXY_JOIN_CODE` (and `AUTOPROXY_HOST_IP`, if set)
from the environment and writes the same config files the system-service flavour does, straight onto the host.

`network_mode: host` and `cap_add: NET_ADMIN` are required, not optional extras: the tunnel interface and its
nftables rules have to live in the host's own network namespace so a container restart or image update does not
disconnect players — the tunnel and its connection tracking stay in the kernel, not in the container.

**Docker backend note:** the client's firewall rules are verified against Docker's `iptables` backend (the default
on Debian/Ubuntu). The `nftables` backend (`DOCKER_MODS`/`ip6tables`-free installs, or Docker configured explicitly
with `"iptables": false"` and an nftables-native setup) is **untested** — if you have deliberately switched Docker to
that backend, treat this as unverified and check `docker logs` and `nft list ruleset` after install rather than
assuming it works.

## What real mode changes on the machine

Real mode is the default when the node's Wings process runs on the same host as the client (see
[layouts.md](layouts.md), layouts 1–3).

| Item | Path / name | Purpose |
|---|---|---|
| Binary | `/usr/local/bin/autoproxy-client` | The client script itself. |
| File | `/etc/autoproxy/client.json` (mode 0600) | Decoded join-code fields: VPS endpoint, tunnel keys, assigned tunnel IP, mode. |
| File | `/etc/wireguard/autoproxy0.conf` (mode 0600) | Tunnel interface config, `Table = off` so the tunnel does not become the machine's default route. |
| Systemd unit | `autoproxy-client.service` | Runs `autoproxy-client run`, which brings everything up and then logs the handshake age every 30 seconds; a restart or `SIGTERM` leaves the tunnel itself up so in-progress player sessions survive a service restart. |
| nftables table | `inet autoproxy_client` | Marks inbound tunnel traffic, restores that mark on replies (both the forward path and, for locally-terminated services, the output path), and accepts forwarded traffic. In real mode it contains no masquerade rule, so the player's own source address survives all the way to the game server, container or not. |
| ip rule | `fwmark 0x2a lookup 200 priority 100`, plus a default route in table 200 | Routes marked reply traffic back out the tunnel instead of the machine's normal default route. |
| sysctl | `net.ipv4.conf.autoproxy0.rp_filter=2` (in `/etc/sysctl.d/90-autoproxy.conf`) | Reverse-path filtering set to "loose" for the tunnel interface — required because replies leave by a different route than requests arrive by; leaving this at strict (the Debian/Ubuntu default is usually 1 or inherited) silently drops every reply. Linux takes `max(all, interface)`, so a host-wide `all.rp_filter=1` does not override it; if `status` still reports anything but `2`, something else on the host is resetting it. |
| sysctl | `net.ipv4.ip_forward=1` (same file) | Needed even in real mode when the game server itself runs in a container publishing ports, so the host can route between the tunnel and the container network. |
| Docker (if present) | two rules in `DOCKER-USER` (table `ip filter`) | Accept traffic to and from the tunnel interface. Docker's own `FORWARD` chain policy is drop; without these two rules, forwarding looks completely dead — handshakes succeed but no game traffic crosses — with no error anywhere. |

### `--host-ip`

Pass `--host-ip <LAN address>` to `autoproxy-client install` (or, in the container flavour, set `AUTOPROXY_HOST_IP`)
when the node's Pelican allocations are bound to a specific LAN address rather than `0.0.0.0`. The join code itself
does not carry this — it is a property of the machine, so you give it to the installer on that machine.

Docker's own port publishing only matches traffic addressed to the address it was told to bind to; without `--host-ip`, real-mode DNAT to the
tunnel IP will not reach a service bound to a different address. With it, the client adds one extra chain,
`dnat_hostip` at prerouting priority `-101` — one ahead of Docker's own DNAT at `-100` — that rewrites the
destination from the tunnel IP to the address you gave it, for TCP and UDP, before Docker's rules ever see the
packet. Only reach for this if `autoproxy-client status` shows a healthy handshake but connections still never
arrive.

## What site mode changes instead

One rule on this machine, plus the VPS side. In site mode the client adds a `postrouting` masquerade rule, which is
what makes site mode work: traffic the tunnel forwards on to a LAN target is given this machine's address as its
source, the same way a home router masquerades outbound LAN traffic today. The hosts it forwards to have no route
back into the tunnel, so their replies have to come back through this machine — that is the shared-IP trade-off
site mode is documented to make.

The rule is written only for the LAN ranges that peer was given (`ip daddr { ... }`), and only in site mode. In
real mode no masquerade rule exists at all, so nothing here can replace a player's address — including when the
game server runs in a container and the packet crosses a bridge on the way to it. The rest of the difference is on
the VPS: which address its DNAT rules point at, and whether it masquerades before sending traffic down the tunnel.

## How to verify

```bash
sudo autoproxy-client status
```

Reports the WireGuard handshake age, whether the expected nftables table and rules are present, and exits non-zero
if anything is missing — safe to use in a monitoring check. On the plugin side, the Setup or Status page shows the
same node's handshake age within a minute of the client starting.

## Undo

```bash
sudo autoproxy-client uninstall
```

Lists what it is about to remove (the systemd unit, `/etc/autoproxy/client.json`, `/etc/wireguard/autoproxy0.conf`,
`/etc/sysctl.d/90-autoproxy.conf`, the binary, the `autoproxy0` interface with its `ip rule` and routing table, the
`inet autoproxy_client` table, the `DOCKER-USER` rules if it added them) and asks for confirmation unless `--yes` is
given. Without a terminal attached and without `--yes` it refuses rather than guessing. The apt packages it
installed are left in place, and the live `ip_forward`/`rp_filter` kernel values stay as they are until reboot —
only the file that made them persistent is removed. If you remove packages afterwards, remove `wireguard-tools`
only: on Debian, Docker depends on `nftables`, so removing that package stops Docker and every game server on the
host.

`autoproxy-client down` (without `uninstall`) brings the tunnel down without removing the installed files, if you
want to pause forwarding without undoing the install.
