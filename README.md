# Pelican Auto Proxy

If your Pelican Panel and its game servers run on a home or office connection, players cannot reach them directly:
there is no public IP, or your router is not yours to configure. Pelican Auto Proxy rents that public face out to a
small VPS. Inside the panel you mark an allocation public, and within a minute players can connect through the VPS —
no port forwarding on your router, no per-port proxy process, and (when the tunnel runs on the machine hosting the
game server) players show up with their real IP instead of the VPS's.

Forwarding happens in the kernel (nftables DNAT over a WireGuard tunnel), so one VPS can carry hundreds of forwarded
ports without running hundreds of processes.

**Status: in development, not yet released.** Nothing here is installable yet; this documentation describes the
5-minute install strangers will follow once a release exists.

## How it works

```
                    ┌──────────────────────────┐
   players  ───────►│   VPS (autoproxy-agent)  │
                     │   nftables DNAT + WireGuard
                     └────────────┬─────────────┘
                                  │ WireGuard tunnel (UDP 51820)
                    ┌─────────────┴─────────────┐
                    │   your node host           │
                    │   autoproxy-client          │
                    │   Pelican Wings + game server│
                    └─────────────────────────────┘
                                  ▲
                                  │ HTTPS (token + pinned cert)
                    ┌─────────────┴─────────────┐
                    │   Pelican Panel (anywhere)  │
                    │   Auto Proxy plugin          │
                    └─────────────────────────────┘
```

The VPS never sees your panel's database or your node's files — only the forwarding rules the plugin sends it and the
game traffic it relays. The plugin talks to the VPS directly over HTTPS, from wherever the panel happens to run.

## Requirements

- A small VPS with a public IPv4 address, root access, and a kernel that supports WireGuard (any current Debian
  kernel does).
- Debian 12 or 13 on both the VPS and every node host that runs the tunnel client. Tested live on Debian 13; Debian 12
  is expected to work but has not been tested yet. Ubuntu is not supported in this release; it is tracked for a
  later one.
- A Pelican Panel install where you are a root admin, able to import a plugin zip.
- About five minutes and three copy-paste commands.

## Quick start

The plugin generates every command below with your own values — these are examples of the shape, not values to
type by hand. Full walkthrough with a "how to verify" step after each one: [docs/quickstart.md](docs/quickstart.md).

**1. Install the agent on your VPS.** SSH in as root and run:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-vps.sh | sudo bash
```

It prints a **VPS code** — a block of text, not a secret you type from memory. Copy it.

**2. Paste the VPS code into the plugin.** Import the plugin zip (Admin → Plugins → Import), install it, then open
**Admin → Auto Proxy → Setup** and paste the code into step 1.

_(Screenshot: Setup step 1, VPS connected, showing the agent version. Added at first release.)_

**3. Add each node.** For every node you want reachable, step 2 of the same page shows a join command built for that
node — either a one-line install or a Docker Compose snippet. Run it on that node's host as root:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-client.sh | sudo bash -s -- <join code>
```

_(Screenshot: Setup step 2, one node connected and one waiting for its first handshake. Added at first release.)_

Within a minute the plugin's Status page shows a handshake for that node. Give an allocation the alias `proxy` or
`public` and, once a server is assigned to it, it is reachable from the internet within a minute too.

## Supported layouts

Every layout uses the same VPS and the same plugin; only where the tunnel client runs, and in what mode, changes.
Full diagrams and "what the game server sees" for each: [docs/layouts.md](docs/layouts.md).

- **Panel and node on one machine** — the most common home setup. One tunnel client, real player IPs.
- **Panel on one machine, nodes on the same LAN** — one tunnel client per node host, each its own peer, real player IPs.
- **Remote nodes on other networks** — each remote node runs its own tunnel client and connects independently.
- **Site mode, for services that are not Pelican** — a client on one LAN machine forwards to other hosts on that LAN;
  those hosts share one IP, the way a traditional reverse proxy works.

The panel machine itself never runs a tunnel client. Wherever the panel lives, all it needs is outbound HTTPS to the
VPS's API.

## What players see

A game server sees the address the tunnel delivers packets from. When the tunnel client runs on the same host as the
game server (real mode, the default for a node running Wings), players show up with their **real IP** — bans, geo
rules and per-player connection limits all work normally. When traffic is forwarded across a LAN to another machine
(site mode), every player looks like they are connecting from that one forwarding host, the same limitation any
shared-IP proxy has.

## Security summary

- The VPS only relays traffic and applies rules the plugin sends it over an authenticated, encrypted API; it does
  not hold your panel's database, files or player data.
- The API is protected by a token and a certificate pinned by the plugin at setup, with a lockout after repeated
  failed attempts. Restricting the API port to your panel's IP is one command away — see
  [docs/security.md](docs/security.md).
- Only allocations you deliberately mark public (alias `proxy` or `public`, with a server assigned) are ever
  forwarded; everything else stays closed. Full threat model: [docs/security.md](docs/security.md).

## Documentation

- [Quick start](docs/quickstart.md) — the steps above, in full, with verification after each one.
- [Supported layouts](docs/layouts.md) — four ways to arrange panel, nodes and the VPS.
- [Installing the VPS agent](docs/install-vps.md)
- [Installing the node client](docs/install-client.md)
- [Using the plugin](docs/plugin.md)
- [Security](docs/security.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Updating](docs/updating.md)
- [Uninstalling](docs/uninstall.md)
- [FAQ](docs/faq.md)
- [Running the node client under Coolify](docs/coolify.md)
- [Migrating from frp or playit.gg](docs/migrating-from-frp-or-playit.md)
- [Architecture (for contributors)](docs/dev/architecture.md)
- [Testing (for contributors)](docs/dev/testing.md)
- [Cutting a release (for contributors)](docs/dev/release.md)

## License

[MIT](LICENSE).
