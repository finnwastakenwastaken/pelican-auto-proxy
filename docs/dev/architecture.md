# Architecture

Technical reference for contributors. For the plain-English version, start at the [README](../../README.md) and
the rest of `docs/`. For why decisions were made this way (and what was rejected), see [decisions.md](decisions.md).

## Components

```
players ──► VPS (autoproxy-agent, Go)
             nft: DNAT public port → target
                  target = a peer's tunnel IP  → NO masquerade  (real player IP; client on the Wings host)
                  target = LAN IP behind a peer → masquerade    (site mode)
             WireGuard tunnel interface, N peers, managed live by the agent
             HTTPS API, bearer token, self-signed cert (IP SAN) shipped in the VPS code, lockout
             ▲
Pelican plugin "autoproxy" ──HTTPS──► VPS API (from wherever the panel runs)
             │
             ▼ (join code)
node host: autoproxy-client, systemd or Docker, real mode or site mode
```

- **`agent/`** (Go, stdlib only, no cgo, no vendor directory): owns the VPS-side nftables tables, the WireGuard
  peer set, and the HTTPS API. One static binary, `autoproxy-agent`, subcommands `setup`, `show-code`, `uninstall`,
  `run`.
- **`client/`** (bash + `wg` + `nft`): one script, `autoproxy-client`, for both the system-service and Docker
  flavours. Subcommands `install`, `up`, `run`, `status`, `down`, `uninstall`.
- **`installers/`**: thin wrappers (`install-vps.sh`, `install-client.sh`) — OS gate, download the pinned release
  asset, verify its checksum, run the real setup command. No logic that isn't in the binary/script itself lives
  here on purpose, so "what does the installer do" and "what does the software do once installed" are the same
  question.
- **`plugin/autoproxy/`**: Pelican plugin (Filament pages: Setup, Forwards, Status; a dashboard banner; a
  once-a-minute scheduled command `autoproxy:sync`).

## API contract summary

All routes require `Authorization: Bearer <token>`, including unknown paths — there is no unauthenticated endpoint,
not even a health check. Body limit 1 MiB; unknown JSON fields are rejected rather than ignored, so a client typo
never silently produces an unintended forward.

| Method & path | Purpose |
|---|---|
| `GET /v1/status` | Version, uptime, applied rule/peer counts, freshest WireGuard handshake age, last error. Returns 200 even if `wg show` itself fails (with `wg.error` set) — the endpoint that reports the tunnel is missing must not fail because the tunnel is missing. |
| `GET /v1/rules` | The last successfully applied forwarding set (always an array, never `null`). |
| `PUT /v1/rules` | Full replace: the body is the complete desired state, anything absent is removed. Rejects (422, per-rule reasons) rather than partially applies on any validation failure; both sides of a port conflict are rejected together, never one picked over the other. |
| `GET /v1/peers` | Every peer's tunnel IP, mode, allowed CIDRs (site mode), handshake age, rx/tx. |
| `POST /v1/peers` | Creates a peer: generates its keypair and assigns a tunnel IP from the pool, applies it live, and returns a one-time join code containing the private key. The private key is never retrievable again after this response. |
| `DELETE /v1/peers/{id}` | Removes a peer live and its routes; any rule still targeting it is subsequently withheld, not silently redirected. |
| `POST /v1/peers/{id}/rotate` | Rotates a single peer's key without changing its tunnel IP or mode. |
| `POST /v1/token/rotate` | Issues a new API token, returned once; the old one stops working immediately. |

Validation shared between the plugin and the agent (the agent re-validates independently — the plugin's checks are
a UI convenience, not the security boundary): ports 1–65535; a port range end must be ≥ its start; a port range
cannot remap to a different target port, because nftables interval maps cannot express a per-port offset;
site-mode targets must be private IPv4 and inside that peer's allowed CIDRs; the reserved public ports (SSH, the
WireGuard port, the API port) can never be forwarded, including when a range would span one; overlapping public
port spans on the same protocol reject both rules unless they are identical in span and target; the same rule id
used twice rejects both; maximum 4096 rules.

## nftables design

Two VPS tables, written by two different components at two different times, which is the point: a bad rule set
cannot take SSH down with it.

- `inet autoproxy_base` — written once by `autoproxy-agent setup` into `/etc/nftables.conf`, owned by the
  `nftables` unit, and not touched again until the next setup run. An `input` chain with policy `drop` that accepts
  loopback, established/related, ICMP/ICMPv6, the detected sshd port, the WireGuard port and the API port, plus an
  `output` chain with policy `accept`. It has **no forward chain at all**, deliberately: nftables requires every
  base chain at a hook to accept, so a forward chain here as well would split ownership of forwarding across two
  files and two writers. The file ends in `include "/etc/autoproxy/rules.nft"`, and setup writes an empty
  placeholder there first, because a missing include target makes nft abort the *whole* file — leaving the box with
  no firewall at boot, not just a missing agent table. The file never runs `flush ruleset`; it declares and then
  deletes `inet autoproxy_base` by exact name so re-applying it cannot disturb the agent's table.
- `inet autoproxy_rules` — rendered by the agent on every apply and loaded with `nft -c -f` (check) then `nft -f`
  (apply); the new text only replaces `rules.nft` on disk once the apply succeeded, so a rejected ruleset leaves
  both the live table and the file untouched. It holds the DNAT maps (`tcp_addr`, `udp_addr`, `tcp_remap`,
  `udp_remap`), the `direct_targets` set, the `prerouting` DNAT chain, the `postrouting` masquerade chain, and the
  `forward` chain with policy `drop` and exactly two accepts — established/related, and
  `iifname <public> oifname <tunnel> ct status dnat`. Declaring the table, then deleting it, then redefining it in
  the same transaction (never `flush table`) is required: `flush` empties chains but leaves set/map elements in
  place, so re-applying a changed range fails with "File exists," and an empty rule set would leave every
  previously-open port silently still open.

Postrouting on the VPS: `oifname <tunnel> ip daddr != @direct_targets masquerade`, where `@direct_targets` is the
set of peer tunnel IPs currently in real mode — traffic to those is never masqueraded, which is what makes the
reply arrive at the client carrying the true player-facing address rather than the VPS's own.

### Real-IP ruleset (client side)

Verified design (sources: WireGuard, `nft`/`wg-quick` documentation, the nftables wiki, Docker's own firewall
documentation): a DNATed packet's reply must have its connection-tracking mark restored **only on the reply
direction** (`ct direction reply`), otherwise an already-DNATed inbound packet gets sent back out the tunnel
instead of to the game server. A second restore chain at `type route hook output` is needed for locally-terminated
services (a game server bound directly to the host's own network, not published through a container network) — a
forward-only restore chain never sees those.

```
sysctl: net.ipv4.ip_forward=1  net.ipv4.conf.<tunnel>.rp_filter=2
tunnel iface (client side: "autoproxy0"): Table=off, peer AllowedIPs 0.0.0.0/0;
  ip route replace <vps tunnel ip>/32 dev <tunnel>
ip rule add fwmark 0x2a lookup 200 priority 100; ip route replace default dev <tunnel> table 200
table inet autoproxy_client {
  chain mark_in     { type filter hook prerouting priority mangle; iifname "<tunnel>" ct state new ct mark set 0x2a }
  chain restore_fwd { type filter hook prerouting priority mangle; ct direction reply ct mark 0x2a meta mark set ct mark }
  chain restore_out { type route  hook output     priority mangle; ct direction reply ct mark 0x2a meta mark set ct mark }
  chain forward     { type filter hook forward priority filter; policy accept; iifname "<tunnel>" accept; ct state established,related accept }
  # site mode only (rendered only when this peer has lan_cidrs):
  chain postrouting { type nat hook postrouting priority srcnat; policy accept;
     iifname "<tunnel>" oifname != "<tunnel>" ip daddr != <tunnel subnet> ip daddr { <lan cidrs> } masquerade }
  # --host-ip only:
  chain dnat_hostip { type nat hook prerouting priority -101; policy accept;
     ip daddr <vps tunnel ip> meta l4proto { tcp, udp } dnat to <host ip> }
}
  (the postrouting masquerade chain exists in site mode only, and is scoped to the LAN ranges that peer was
   actually given. In real mode there is no postrouting chain at all.)
+ DOCKER-USER (table ip filter, iptables backend): iifname "<tunnel>" accept / oifname "<tunnel>" accept
  — the nftables Docker backend is untested; the client warns rather than silently doing nothing if that chain is absent.
```

The masquerade chain used to be rendered in both modes, on the assumption that real-mode traffic is delivered to a local address and
therefore never reaches postrouting, with bridges excluded by interface name (`oifname != "docker*" oifname !=
"br-*"`) as a belt-and-braces measure. Both halves of that were wrong on exactly the machine this project exists
for. A Wings host delivers the game port to a container, so the packet is *forwarded* across a bridge and does hit
postrouting; and Wings names its own bridge `pelican0`, which neither pattern matches. The game server saw the
bridge gateway (a 172.x address) instead of the player's address — the one thing real mode promises not to do.
Confirmed live: an echo server on a Wings host reported the bridge gateway before the fix and the real client
address after it. Matching on destination rather than on an interface name cannot be defeated by a bridge being
renamed, or by a different panel naming its bridge something else again.

Facts underpinning this design: NAT chains are only consulted for a connection's first packet, so a container
engine's own MASQUERADE rule never re-SNATs replies of an already-DNATed flow; a mangle-priority restore chain
(-150) runs before conntrack's own dstnat priority (-100) and before the routing decision; cryptokey routing on the
client accepts any player source once `AllowedIPs 0.0.0.0/0` is set on that peer, and `Table = off` only suppresses
automatic route installation, it doesn't affect which packets the interface accepts; the VPS only needs
`tunnel-IP/32` (plus LAN ranges for site mode) as that peer's allowed range, because replies arrive with the tunnel
IP as source after the un-DNAT step; a peer must never be given `0.0.0.0/0` on the VPS side; the encapsulated
packet's own mark never leaks into the outer UDP datagram; a container engine that routes `FORWARD` through its own
user-defined chain first makes an accept there final, so the two rules above are sufficient without touching the
base `FORWARD` chain; reverse-path filtering uses `max(all, interface)`, so the per-interface sysctl must be set to
loose (`2`) — leaving it at strict does not work even if the "all" value looks loose, and relying on `rp_filter=0`
loses the protection entirely rather than only where it's needed; a UDP connection-tracking mark is shared by both
directions of that flow.

**Only settled by testing against a real Wings host**, not by design review alone: the `type route` output-hook
restore for host-network UDP replies; which container
firewall backend is actually active; whether allocations are published on `0.0.0.0` or bound to a specific LAN
address (if bound, the container engine's own DNAT does not match a destination of the tunnel IP; the client's
`--host-ip` fallback DNATs tunnel IP → the LAN IP one priority ahead of the normal chain, unaffected by the marking
logic); path MTU over the tunnel.

### Restarting the client without dropping players

`autoproxy-client up` is idempotent and is what a service restart runs, so it has to cope with an interface that is
already live. It now decides whether to touch WireGuard at all. `tunnel_config_matches` compares the live interface
against `autoproxy0.conf` on four points — the private key, the single peer's public key, that peer's endpoint, and
the persistent keepalive. If all four match, `up` skips `wg syncconf` entirely and logs `tunnel configuration
already matches; leaving the live session untouched`. If anything differs, or the interface has no key at all, it
runs the full `wg syncconf` with the complete stripped config and logs `tunnel configuration changed; applying it
(this rekeys the tunnel)`.

It has to be a skip, not a sync-without-the-key. Handing WireGuard a private key rekeys the tunnel even when the
key is byte-for-byte identical, because the session secrets are derived from it; every live session is wiped and
the tunnel carries nothing until the next handshake. Nothing inbound can trigger that handshake — the client drops
what it can no longer decrypt — so recovery waits on the 25-second persistent keepalive. Measured live: with the
nftables table fully intact, `wg syncconf` alone froze the interface for about 13 seconds, rx and tx byte counters
not moving by a single byte, while the handshake timestamp stayed unchanged.

Removing the `PrivateKey` line from the config fed to `syncconf` looks like the obvious fix and is worse than the
bug. WireGuard does not read a missing key as "leave the key alone"; it reads it as "this interface has no key" and
clears it. The interface then reports public key `(none)`, can never handshake again, and the node stays dark until
the client is re-run with a config it considers changed.

Verified live on both paths: a `systemctl restart autoproxy-client` during a continuous once-per-second UDP
exchange lost 0 of 15 packets and left the handshake timestamp unchanged, with the "already matches" line in the
journal; the restart where the key genuinely differed logged "changed; applying" and rekeyed as intended.

### Interface names

They differ on purpose, and mixing them up is the most common documentation bug in this repository:

| Machine | Interface | Written by | Peers in the config file? |
|---|---|---|---|
| VPS | `wg0` (`/etc/wireguard/wg0.conf`, `wg-quick@wg0`) | `autoproxy-agent setup`, once | No — the agent adds them live from `peers.json` |
| Node host | `autoproxy0` (`/etc/wireguard/autoproxy0.conf`) | `autoproxy-client install` | One: the VPS |

The agent's default is overridable with `AUTOPROXY_WG_IFACE`; the client's is a `readonly` constant.

### Two deliberate deviations on the VPS

- `wg0.conf` has no `PostUp`/`PreUp` hooks. `wg-quick` treats a failing hook as fatal and deletes the interface it
  has just created, so one sysctl a container or locked-down provider kernel refuses would take the tunnel down.
- `rp_filter` is not changed on the VPS at all. Every address a peer may send from is in that peer's `AllowedIPs`
  and is therefore routed back down `wg0`, so the strict reverse-path check already passes. The client is the side
  that needs `rp_filter=2`, on its own interface only.
- `/etc/nftables.conf` is replaced without `--yes` when it still matches the checksum dpkg recorded for the
  `nftables` package's own conffile — an untouched distro default. Anything else, including an unparsable dpkg
  answer, refuses without `--yes`. Rationale and the user-facing wording: [../install-vps.md](../install-vps.md).

## Peer management

Peers are added and removed live with `wg set <tunnel> peer <pub> allowed-ips ...`. WireGuard itself never touches
routes, so the agent separately issues `ip route replace <peer tunnel IP>/32 dev <tunnel> scope link table 201`
(plus LAN ranges for site mode) for each peer. The static interface config (`wg-quick@<tunnel>`) is kept for the
interface's own address and listen port only; the agent never regenerates that file from peer state (two writers
editing the same file is a race waiting to happen) and never calls `wg syncconf` per change. On start, the agent
re-applies every peer and route from `peers.json` — the same "state file is truth, reapply on boot" pattern the
rule set already uses, extended to peers.

### Peer routes live in a dedicated table, never in main

A peer route is never `ip route replace <cidr> dev <tunnel>` straight into the main table. A site peer's
`lan_cidrs` can legitimately cover the address WireGuard is actually dialing for that peer's own transport (its
live public endpoint) — the agent cannot validate against that address at peer-creation time, because WireGuard
resolves it from the client's own config, not from anything the peer-create request carries, and a roaming
client's endpoint can change afterwards regardless. If that route lived in the main table it would beat the
interface route to the peer's real endpoint, so the VPS's own encrypted handshake and keepalive packets would be
routed back into the tunnel they are trying to open — a loop that never completes a handshake and looks, from the
plugin's Setup page, exactly like an unreachable client: "waiting for the first handshake," forever, with nothing
in any log to say why. See [decisions.md](decisions.md) for the live reproduction and what was rejected, and
[../troubleshooting.md](../troubleshooting.md) for the symptom from the operator's side.

The fix is the same construction `wg-quick` uses for `AllowedIPs 0.0.0.0/0`:

```
wg set wg0 fwmark 0x2b
ip route replace <peer cidr> dev wg0 scope link table 201     # every peer route, not just LAN ranges
ip rule add not fwmark 0x2b lookup 201 priority 90
```

The fwmark tags the interface's own transport socket; the rule sends everything that is *not* that marked traffic
through table 201 first, while the interface's own marked packets fall through to the main table and reach the
peer's real endpoint. `agent/internal/wg.DefaultFWMark` (`0x2b`), `DefaultTable` (`201`) and `DefaultRulePriority`
(`90`) are the single source of truth, deliberately distinct from the node client's own `0x2a` / `200` / `100`
(`client/autoproxy-client`) — a different machine solving the mirror-image problem, marking the tunnel's own
traffic to steer it *out* of the peer table rather than marking game traffic to steer it *in*.

All three of the following apply it, because each covers a window the others do not:

- `autoproxy-agent setup` sets the mark and installs the rule live, immediately after `wg-quick@wg0` starts, so
  the operator watching setup output sees it and a refusing kernel says so there.
- `WG0Conf` writes a static `FwMark = 0x2b` line into `/etc/wireguard/wg0.conf` itself, so the mark is true from
  the instant `wg-quick` brings the interface up at boot — before the agent has run at all. A plain `[Interface]`
  key, not a `PostUp` hook: see "Two deliberate deviations on the VPS" above for why hooks are avoided here.
- `peers.Manager.Restore` (daemon start) and `applyLocked` (every peer create, delete-and-recreate, or rotate)
  re-ensure both. `wg set ... fwmark` is declarative and `ip rule show` is a cheap check, so this is unconditional
  rather than only running once — it is also what migrates an existing install: `AddRoute` first tries (best
  effort, result ignored) to remove any route a pre-fix agent left for the same CIDR in the main table, so an
  in-place upgrade does not end up with the same CIDR routed in two tables at once.

`autoproxy-agent uninstall` removes the rule explicitly. It has to: the rule names a table and a fwmark, not the
`wg0` device, so deleting the interface (which does drop every route that referenced it, in whatever table it was
in) does not take the rule with it.

## Security design

- **Transport:** TLS 1.3 minimum, `ReadHeaderTimeout` and `IdleTimeout` set short enough to bound a slow-client
  attack, a fixed request body cap.
- **Certificate:** self-signed ECDSA P-256, 10-year validity, SHA-256 signature, subject alternative name(s) set to
  the VPS's actual IP (and a DNS name if one is given at setup). The plugin stores this certificate as its own
  trust anchor (OpenSSL accepts a self-signed certificate as its own root) rather than relying on a public CA,
  because there usually isn't a stable hostname to get one for at setup time. An IP changing invalidates the
  certificate on purpose — see the "certificate error after VPS IP change" entry in
  [troubleshooting.md](../troubleshooting.md).
- **Authentication:** a 32-byte token, compared in constant time over its SHA-256 digest (so neither the token nor
  its length leaks through timing), on every route without exception.
- **Abuse handling:** a per-source-IP failure counter; five failures trigger a lockout (429, with a `Retry-After`
  header) for roughly 15 minutes; the counter resets on a successful request and is purged after a longer idle
  window so it doesn't grow unbounded; each lockout is logged once, not once per rejected request while locked out.
- **Scope of a leaked credential:** covered in plain language in [security.md](../security.md); the short version
  is that the API token can change forwarding rules and peers but cannot itself join the tunnel, and a join code
  can join the tunnel as one peer but cannot change any rule.

## Not this version

IPv6, multiple VPSes or failover between them, a public CA for the API when a stable domain exists, per-rule
allow-lists or rate limits, an automated migration path from a prior single-deployment prototype, platforms outside
Debian 12 and 13. Ubuntu 22.04/24.04 support is a tracked follow-up, not ruled out.
