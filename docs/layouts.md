# Supported layouts

One VPS and one plugin install support all four of these at once — you are not choosing a layout for the whole
setup, you are choosing a mode per node (or per non-Pelican service) on the Setup page. This page shows each layout
on its own for clarity.

In every diagram, the VPS is the only machine with a public IP.

## 1. Panel and node on one machine

The most common home setup: Pelican Panel, Wings, and the game servers all run on the same box.

```
 internet ──► VPS (autoproxy-agent) ──WireGuard──► home machine
                                                     ├─ Pelican Panel + plugin
                                                     ├─ Wings
                                                     ├─ game server(s)
                                                     └─ autoproxy-client (real mode)
```

- **Where the client runs:** on the one machine, alongside Wings.
- **Mode:** real (the client and the game server share a host).
- **What the game server sees:** the player's real IP.

## 2. Panel on one machine, nodes on the same LAN

The panel lives on one box; one or more separate node hosts on the same LAN run Wings.

```
                              ┌─ node host A: Wings + autoproxy-client (real mode)
 internet ──► VPS ──WireGuard─┼─ node host B: Wings + autoproxy-client (real mode)
                              └─ panel host: Pelican Panel + plugin (no tunnel needed)
```

- **Where the client runs:** on each node host, not on the panel host — the panel only needs outbound HTTPS to the
  VPS's API.
- **Mode:** real, one peer per node host.
- **What the game server sees:** the player's real IP, per node.

## 3. Remote nodes on other networks

Node hosts on different networks entirely (different homes, different providers) each connect independently.

```
 internet ──► VPS ──WireGuard──► node host A (network 1): Wings + autoproxy-client
          └──────────WireGuard──► node host B (network 2): Wings + autoproxy-client
```

- **Where the client runs:** on each remote node host; each dials out to the same VPS as its own peer.
- **Mode:** real, per node host.
- **What the game server sees:** the player's real IP. Nodes do not need to see each other at all — only the VPS.

A rented server with a public IP of its own may not need the proxy: players can already reach it. Proxy it when you
want one public address for every server, want its IP hidden, or the provider blocks the ports you need. What to
open at the provider, and what changes if you do:
[is this node local or remote?](install-client.md#is-this-node-local-or-remote).

## 4. Site mode, for services that are not Pelican

One LAN machine runs the tunnel client and forwards to other hosts on that LAN — including things the plugin never
manages, like a second panel, a web app, or a game server not tracked as a Pelican allocation. Use the plugin's
**Forwards** page, or Setup's "forwarded from another host on this LAN" mode, for this.

```
 internet ──► VPS ──WireGuard──► LAN gateway: autoproxy-client (site mode)
                                    ├─ masquerades to ─► LAN host X (Wings + game server)
                                    └─ masquerades to ─► LAN host Y (some other service)
```

- **Where the client runs:** on one LAN machine acting as a gateway, not on the target hosts themselves.
- **Mode:** site (the client masquerades outbound, the way a traditional reverse proxy or router does).
- **What the game server sees:** the LAN gateway's address for every player — everyone shares that one IP. This is
  the same trade-off any shared-IP forwarding setup has; per-player bans and limits do not work here. Pick layout 1,
  2 or 3 instead whenever the client can run directly on the game server's own host.

### What a LAN range may not contain

The LAN ranges you give a site client describe what that client is allowed to reach. Two of them are refused when
you create the client, with the reason on screen:

- a range that overlaps the **tunnel subnet** (`10.66.66.0/24` by default) — those addresses belong to the tunnel
  itself and are handed out by the VPS;
- a range that contains the **VPS's own address**, the one every client dials. The VPS has to reach that address
  directly; routing it into the tunnel would cut the VPS off from its own network.

Keep ranges as narrow as the machines you actually forward to. `192.168.1.50/32` for one host is better than
`192.168.1.0/24` for the whole LAN — it is less to get wrong, and it keeps the tunnel client from being allowed to
claim traffic for addresses nobody meant to expose.

### When the forward target is the client's own machine

A manual forward can point at the address of the machine the site client itself runs on. That works, but the
address the service sees is different again: the packet is delivered locally on that machine instead of being
routed on to another host, so the client's masquerade rule never fires. The only rewrite left is the VPS's own,
and the service therefore sees **the VPS's address inside the tunnel** (the first address in the tunnel subnet,
e.g. `10.66.66.1`) rather than the LAN address of the machine it is running on.

It is still a shared address — every player looks the same — so the trade-off is unchanged. Do not be alarmed by
a tunnel-range address turning up in your logs here; it is not a misconfiguration. It is also the one case where
the LAN range you give the client is the client's own address, which is allowed: the VPS keeps peer routes in a
routing table of their own precisely so a range like that cannot cut the VPS off from reaching that client. If you want that machine's
players to keep their real IPs, give it its own node in real mode (layout 1, 2 or 3) instead of forwarding to it
in site mode.
