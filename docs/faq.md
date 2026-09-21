# FAQ

## Why not playit.gg, frp, or a Cloudflare tunnel?

Those all work, and if one already does what you need, there's no reason to switch. Auto Proxy exists for a
different trade-off:

- **playit.gg** and similar hosted tunnel services are easiest to set up, but you don't control the relay, and free
  tiers throttle or cap bandwidth. Auto Proxy's VPS is yours — the only ongoing cost is what you already pay the
  VPS provider.
- **frp** (and Auto Proxy's own predecessor) does the same kind of relay, but as a userspace proxy per forwarded
  port. That's fine for a few ports; a Pelican panel exposing many allocations across many servers scales more
  cheaply in the kernel, where memory is independent of how many ports are open.
- **Cloudflare Tunnel** is excellent for HTTP(S) traffic but is not built for arbitrary UDP game protocols, and it
  puts Cloudflare in the path of the connection. Auto Proxy forwards raw TCP/UDP to a VPS you control end to end.

Real player IPs (when the client runs on the Wings host) is the feature none of the above give you for game
servers specifically — most tunnel relays present a shared IP to the game server no matter where the client runs.

## Why not Ubuntu yet?

This release supports Debian 12 and 13 only — tested live on Debian 13, and Debian 12 is expected to work but has
not been tested yet. Ubuntu was in scope during development, and the OS gate in the agent and the installers is
built so adding it back is a small, contained change; it just was not tested before this release shipped, and an
untested platform is not something this project promises on. Ubuntu support is tracked for a later release, not
ruled out.

## Do I need a client on the panel machine?

No. The panel talks to the VPS over HTTPS and never carries game traffic, so it never runs a tunnel client. The
client goes on the machines that host the game servers. If the panel and a node share one machine, the client you
install there is for the node, not the panel.

## My node is a rented server with a public IP, do I need this?

Probably not — players can connect to it directly already, and a node stays unproxied until you mark it proxied.
Proxy it anyway if you want one public address for every server whichever node it runs on, if you want the node's
own IP hidden from players, or if the provider blocks or charges for the ports you need. Details, including the one
firewall rule it needs: [is this node local or remote?](install-client.md#is-this-node-local-or-remote).

## Does this support IPv6?

Not in this version. The VPS's public side and the tunnel are IPv4 only; targets must be private IPv4 addresses.
IPv6 is on the list for a later version (see the project's "not this version" notes in
[dev/architecture.md](dev/architecture.md)) but nothing here should be assumed to work over IPv6 today.

## Can I use several VPSes?

Not yet — one VPS per plugin install. If you need forwarding from more than one public location (geographic
failover, or more bandwidth than one VPS's uplink gives you), that's not supported today; it's recorded as a later
possibility, not ruled out permanently.

## What latency should I expect?

Player traffic makes one extra hop: player → VPS → WireGuard tunnel → your node host. In practice this adds
roughly the network latency between the VPS and your node host (often single-digit milliseconds if they're in the
same region), on top of whatever latency the player already has to the VPS. Choosing a VPS location close to your
node host (not necessarily close to your players) keeps that extra hop small. There is no additional relay-side
processing delay worth measuring — forwarding happens in the kernel, not in a userspace proxy loop.

## What does the VPS cost?

Whatever the smallest tier from any provider costs — the agent itself uses well under 20 MB of memory even with a
thousand forwarded ports, and does no meaningful CPU work beyond applying rule changes. The cost driver is entirely
the provider's own pricing and any bandwidth cap they apply, not anything Auto Proxy needs. This project doesn't
recommend a specific provider.

## Can I use my existing WireGuard setup?

Not directly. Auto Proxy manages its own dedicated WireGuard interface on each machine — `wg0` on the VPS, and
`autoproxy0` on every node host — and expects to own that interface's configuration, peers and routes entirely. It
isn't designed to share an interface with a VPN you already run for other purposes. Running both is fine: they're
separate interfaces, separate keys, separate subnets, and don't interact.

## What about DDoS protection?

None is built in. The VPS is exposed on the internet on whatever ports you forward, the same as any port-forwarded
home server would be, and inherits whatever DDoS mitigation (if any) your VPS provider offers at the network edge.
If you need dedicated DDoS protection, that's a provider or CDN concern layered in front of the VPS, independent of
this project — the agent's own hardening protects its control-plane API, not the forwarded game traffic itself,
and it is one specific thing rather than a general rate limiter: five wrong tokens from one address lock that
address out for fifteen minutes. Forwarded ports themselves are not rate limited or filtered at all.

