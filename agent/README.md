# autoproxy-agent

The VPS side of **Pelican Auto Proxy**. It owns one nftables table, manages the
WireGuard peers your node hosts connect through, and exposes a small HTTPS API
that the Pelican plugin drives.

Written in Go with the standard library only. No database, no daemon it depends
on, two state files.

## What it does

Players connect to your VPS. The kernel forwards their packets down a WireGuard
tunnel to the machine that actually runs the game server, and the reply goes
back the same way. There is no userspace proxy per port, so memory does not grow
with the number of forwarded ports.

Two ways a forward can point:

| Mode | Target | Masquerade | The game server sees |
|---|---|---|---|
| **real-IP** | a peer's own tunnel address | no | the player's real IP address |
| **site** | an address on a peer's LAN | yes | the tunnel address |

Real-IP mode is the headline: IP bans and per-player rate limits work. It needs
the client running on the host that publishes the game ports. Site mode is for
anything else on that LAN — another machine, a service the client does not run
on — and those targets share one source address because they have no route back
into the tunnel.

## Install

```
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-vps.sh | sudo bash
```

The installer checks the OS, downloads the binary, verifies its SHA-256 against
the published `SHA256SUMS`, and runs `autoproxy-agent setup`. Supported:
**Debian 12, Debian 13, Ubuntu 22.04, Ubuntu 24.04**. Anything else is refused
with a message saying so.

Setup ends by printing a **VPS code**. Paste that into the plugin's Setup page.

## Subcommands

```
autoproxy-agent setup [flags]   provision this VPS and print the VPS code
autoproxy-agent show-code       re-print the saved VPS code (root only)
autoproxy-agent uninstall [-y]  remove everything setup created
autoproxy-agent run             run the daemon (the default with no subcommand)
autoproxy-agent -version        print the version
```

### `setup`

| Flag | Default | Meaning |
|---|---|---|
| `--dry-run` | off | print every change that would be made and touch nothing |
| `--yes`, `-y` | off | allow replacing an `/etc/nftables.conf` that is not ours |
| `--api-port N` | `7443` | HTTPS control API port |
| `--wg-port N` | `51820` | WireGuard listen port |
| `--wg-subnet CIDR` | `10.66.66.0/24` | tunnel subnet; `.1` is the VPS, `.2`–`.254` is the peer pool |
| `--public-ip ADDR` | auto-detect | this VPS's public IPv4, used for the certificate SAN and in join codes |
| `--dns-name NAME` | none | extra DNS name in the API certificate, if you have a hostname |

What it changes on the machine:

- installs `wireguard-tools` and `nftables`
- `net.ipv4.ip_forward=1`, persisted in `/etc/sysctl.d/99-autoproxy.conf`
- a WireGuard keypair and `/etc/wireguard/wg0.conf` **with no peers in it**, carrying
  `FwMark = 0x2b` (see "Peer routes" below)
- an `ip rule` at priority `90` (`not fwmark 0x2b lookup 201`), so peer routes stay
  out of the main routing table
- `/etc/nftables.conf`: a base firewall with `policy drop` on input, allowing
  loopback, established, ICMP, your sshd port, the WireGuard port and the API
  port, ending in `include "/etc/autoproxy/rules.nft"`
- `/etc/autoproxy/` (agent.env, rules.nft, tls/agent.crt, tls/agent.key, vps-code) and `/var/lib/autoproxy/`
- the binary itself at `/usr/local/bin/autoproxy-agent`, if it is not already there
- the `autoproxy-agent` systemd unit, and `wg-quick@wg0` enabled
- deletes orphaned `ip nat` / `ip filter` tables **only if Docker is not installed**

It never touches sshd, and it never runs `flush ruleset`.

**Peer routes.** WireGuard never adds routes itself, so the agent adds one per
peer `AllowedIP` — but into routing table `201`, never into the main table. A
site client's LAN range can legitimately cover an address the VPS has to reach
directly, including that client's own WireGuard endpoint; a route like that in
the main table makes the VPS send its own encrypted handshakes into the tunnel
it is trying to open, and the tunnel then never comes up, with no error
anywhere. The interface's own packets carry fwmark `0x2b`, and the `not fwmark
0x2b lookup 201` rule lets exactly those skip table `201` and reach the peer's
real endpoint. This is the same construction `wg-quick` uses for
`AllowedIPs 0.0.0.0/0`.

The mark is written into `wg0.conf` (so it is true from the moment `wg-quick`
brings the interface up after a reboot) and re-applied by the agent on every
start (so an install upgraded in place gets it without re-running setup). The
agent also removes, best effort, any peer route a pre-fix version left in the
main table. `autoproxy-agent uninstall` deletes the rule explicitly — it names
a mark and a table rather than the `wg0` device, so removing the interface does
not take it with it.

To see it:

```bash
sudo wg show wg0 fwmark      # 0x2b
ip rule show | grep 201      # 90: not from all fwmark 0x2b lookup 201
ip route show table 201      # one entry per peer AllowedIP
```

Re-running is safe: the WireGuard key, the bearer token and the certificate are
generated once and kept. Everything derived from them is refreshed, so a new
sshd port or a new interface takes effect.

**Your existing firewall.** Debian and Ubuntu ship a default `/etc/nftables.conf`
with the `nftables` package; setup recognises that untouched default (by the
checksum dpkg recorded for that conffile) and replaces it **without asking for
`--yes`**, keeping a copy at `/etc/nftables.conf.pre-autoproxy`. Otherwise the
one-command install would stop on every fresh VPS, which is every VPS this gets
installed on. A file you have actually edited is refused unless you pass
`--yes`, and so is one where dpkg cannot be asked or its answer cannot be
parsed: the worst case is an operator who has to pass a flag, never an operator
whose firewall we replaced silently. Only the write is skipped — the generated
ruleset is still syntax-checked, and the agent's own table is unaffected.

**`rp_filter` is deliberately left alone on the VPS.** No sysctl of ours touches
it, and `wg0.conf` carries no `PostUp` hooks that could. The VPS does not need a
loosened reverse path: every address a peer may send from is in that peer's
`AllowedIPs` and is therefore routed back down `wg0`, so the strict check
passes. (The client sets `rp_filter=2` on its own `autoproxy0` interface — a
different machine, a different problem.) A failing `wg-quick` hook would also
delete the interface it had just created, which is the other reason there are
none.

### `uninstall`

Lists everything it is about to remove, then removes it: the unit, the binary,
`/etc/autoproxy`, `/var/lib/autoproxy`, both nftables tables (by exact name),
and `wg0`. It restores `/etc/nftables.conf.pre-autoproxy` if there is one. The
`wireguard-tools` and `nftables` packages are left installed.

## The two codes

Both are **base64url without padding** wrapping a JSON object, so they survive
being pasted into a web form or a shell command line.

### VPS code — printed by `setup`, pasted into the plugin

```json
{
  "v": 1,
  "endpoint_ip": "203.0.113.10",
  "api_port": 7443,
  "api_ca_pem": "<base64 of the PEM certificate>",
  "api_spki_sha256": "<base64 SHA-256 of the SubjectPublicKeyInfo>",
  "token": "<64 hex characters>",
  "wg_pubkey": "<WireGuard public key>",
  "wg_port": 51820,
  "tunnel_subnet": "10.66.66.0/24",
  "vps_tunnel_ip": "10.66.66.1",
  "version": "v1.0.0"
}
```

`api_ca_pem` is the certificate itself: the plugin trusts it as its own
certificate authority (Guzzle `'verify' => <path to that PEM>`), which gives
both encryption and `iPAddress` SAN matching without anybody owning a domain.
`api_spki_sha256` is there for clients that prefer public-key pinning — it is
the value curl expects after `sha256//` in `CURLOPT_PINNEDPUBLICKEY`.

The code contains the bearer token. It is printed once with a warning and saved
to `/etc/autoproxy/vps-code` (mode 0600). `autoproxy-agent show-code` reprints
it; only root can read it.

### Join code — returned by `POST /v1/peers`, pasted into the client installer

```json
{
  "v": 1,
  "endpoint": "203.0.113.10:51820",
  "vps_wg_pubkey": "<VPS WireGuard public key>",
  "client_privkey": "<the client's private key, returned exactly once>",
  "client_address": "10.66.66.2/32",
  "tunnel_subnet": "10.66.66.0/24",
  "vps_tunnel_ip": "10.66.66.1",
  "mode": "real",
  "lan_cidrs": ["10.0.0.0/24"],
  "keepalive": 25
}
```

`client_privkey` is generated on the VPS, handed over once, and **never
stored**. If a join code is lost, `POST /v1/peers/{id}/rotate` mints a new
keypair and a new code; the old key stops working immediately.

`lan_cidrs` is present only in site mode. `keepalive` is 25 seconds because the
client always dials out and nothing else holds the home router's NAT mapping
open.

## API

HTTPS on `0.0.0.0:7443` by default, TLS 1.3 minimum, `Authorization: Bearer
<token>` on every route. Bodies are capped at 1 MiB. Errors are
`{"error": "..."}`; rejected rule sets are `{"rejected":[{"id","reason"}]}`.

The examples below use the documentation address `203.0.113.10`, the PEM the
plugin extracted from the VPS code, and `$AUTOPROXY_TOKEN` for the bearer token.

```bash
API=https://203.0.113.10:7443
curl() { command curl -sS --cacert ./agent.pem -H "Authorization: Bearer $AUTOPROXY_TOKEN" "$@"; }
```

| Route | Purpose |
|---|---|
| `GET /v1/status` | version, uptime, tunnel state, peer summary, applied counts, last error |
| `GET /v1/peers` | every peer with live handshake age and counters |
| `POST /v1/peers` | create a peer → **201** with the join code (shown once) |
| `DELETE /v1/peers/{id}` | remove a peer, its routes and its forwards → **204** |
| `POST /v1/peers/{id}/rotate` | new keypair and a new join code |
| `GET /v1/rules` | the rule set currently applied |
| `PUT /v1/rules` | replace the whole rule set |
| `POST /v1/token/rotate` | new bearer token, returned once and persisted |

### `GET /v1/status`

```bash
curl $API/v1/status
```

```json
{
  "version": "v1.0.0",
  "uptime_s": 3601,
  "wg": { "iface": "wg0", "peers": 2, "handshake_age_s": 41, "rx": 9214, "tx": 18422 },
  "peers": { "total": 2, "healthy": 2 },
  "applied": { "tcp": 3, "udp": 4, "rules": 5 },
  "applied_at": "2026-09-21T07:21:31Z",
  "last_error": ""
}
```

`healthy` counts peers whose last handshake is under 180 seconds old.
`last_error` is how you find out that something applied only partly — for
example that a stored rule was dropped on restart because its peer is gone.

### `POST /v1/peers`

```bash
# A Wings host: real player IPs.
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"wings-1","mode":"real"}' $API/v1/peers

# A LAN machine forwarding on to other hosts: shared IP.
curl -X POST -H 'Content-Type: application/json' \
  -d '{"name":"lan-box","mode":"site","lan_cidrs":["10.0.0.0/24"]}' $API/v1/peers
```

**201**:

```json
{
  "peer": {
    "id": "02d7a63c5f3b",
    "name": "wings-1",
    "public_key": "<WireGuard public key>",
    "tunnel_ip": "10.66.66.2",
    "mode": "real",
    "lan_cidrs": [],
    "handshake_age_s": null,
    "rx": 0, "tx": 0,
    "created_at": "2026-09-21T07:20:58Z"
  },
  "join_code": "<base64url>",
  "warning": "This join code contains the client's private key and is shown exactly once. ..."
}
```

`lan_cidrs` must be RFC1918, written with no host bits, must not overlap the
tunnel subnet, and must not overlap another peer's ranges — two peers claiming
one range gives the kernel two answers for the same address. They are required
in site mode and refused in real-IP mode. Failures are **422** with a reason.

### `PUT /v1/rules`

The whole desired set, every time. Validation is all-or-nothing: if any rule is
rejected, nothing is applied.

```bash
curl -X PUT -H 'Content-Type: application/json' $API/v1/rules -d '{
  "rules": [
    {"id":"alloc-1","proto":"both","public_port":9445,
     "target_peer":"02d7a63c5f3b","note":"real player IPs"},
    {"id":"alloc-2","proto":"udp","public_port":27015,
     "target_ip":"10.0.0.10","via_peer":"0c3c1200cd26","note":"site mode"},
    {"id":"alloc-3","proto":"both","public_port":9400,"public_port_end":9420,
     "target_peer":"02d7a63c5f3b"}
  ]
}'
```

| Field | Meaning |
|---|---|
| `id` | yours; used to report which rule was rejected |
| `proto` | `tcp`, `udp` or `both` |
| `public_port`, `public_port_end` | a single port or an inclusive range |
| `target_peer` | real-IP mode: DNAT to that peer's tunnel address, no masquerade |
| `target_ip` + `via_peer` | site mode: an address inside that peer's `lan_cidrs`, masqueraded |
| `target_port` | remap; not allowed on a range |
| `note` | cosmetic |

Set exactly one of `target_peer` or `target_ip`. A rule is rejected when its
port span overlaps another rule on the same protocol, when it hits a reserved
port (sshd, the WireGuard port, the API port), when a range asks to be remapped,
when a site target sits outside its peer's ranges, or when it names a peer that
does not exist.

**200**: `{"applied":{"tcp":4,"udp":4,"rules":3},"applied_at":"..."}`.
**422**: `{"rejected":[{"id":"alloc-2","reason":"target_ip 10.0.0.10 is outside the ranges peer \"0c3c1200cd26\" covers (192.168.1.0/24)"}]}`.

Pushing `{"rules":[]}` closes every forwarded port. That is the intended way to
turn everything off.

### Authentication and lockout

Wrong tokens return **401**. Five failures from one address lock that address
out for fifteen minutes: **429** with `Retry-After: 900`, and the correct token
is refused too while the lockout stands. The check happens before the token is
hashed, so a flood costs nothing. A success clears the counter. Lockouts are
logged once per address; the table is purged after 30 minutes idle and capped at
10 000 addresses.

`POST /v1/token/rotate` returns a new 32-byte hex token once and rewrites the
`AUTOPROXY_TOKEN` line in `agent.env`, leaving the rest of the file alone. If it
cannot write the file it does **not** switch the live token — otherwise a
restart would silently fall back to the old one.

**Interface names.** The VPS's tunnel interface is **`wg0`**
(`/etc/wireguard/wg0.conf`, `wg-quick@wg0`). The node hosts' is **`autoproxy0`**,
written by `autoproxy-client`. They are different names on different machines
and never need to match; `AUTOPROXY_WG_IFACE` overrides this side of it.

## Configuration

`/etc/autoproxy/agent.env` (mode 0600) is the systemd `EnvironmentFile`. Every
value has a matching flag; the token is env-only on purpose, because a flag
would put it in `/proc/<pid>/cmdline` and every `ps` listing.

| Variable | Default | Meaning |
|---|---|---|
| `AUTOPROXY_TOKEN` | — | bearer token; the agent refuses to start without it |
| `AUTOPROXY_LISTEN` | `0.0.0.0:7443` | API listen address |
| `AUTOPROXY_PUBLIC_IFACE` | `eth0` | the internet-facing interface, matched in the DNAT rules |
| `AUTOPROXY_PUBLIC_IP` | — | this VPS's public IPv4, handed to clients in join codes |
| `AUTOPROXY_RESERVED_PORTS` | `22,51820,7443` | public ports that may never be forwarded |
| `AUTOPROXY_STATE_DIR` | `/var/lib/autoproxy` | holds `rules.json` and `peers.json` |
| `AUTOPROXY_RULES_FILE` | `/etc/autoproxy/rules.nft` | the file the agent owns and rewrites |
| `AUTOPROXY_ENV_FILE` | `/etc/autoproxy/agent.env` | rewritten by token rotate |
| `AUTOPROXY_TLS_CERT` / `_KEY` | `/etc/autoproxy/tls/agent.crt`, `.key` | TLS material |
| `AUTOPROXY_WG_IFACE` | `wg0` | tunnel interface |
| `AUTOPROXY_WG_SUBNET` | `10.66.66.0/24` | tunnel subnet and peer pool |
| `AUTOPROXY_WG_PORT` | `51820` | WireGuard port, used in join codes |
| `AUTOPROXY_NFT_BIN` / `_WG_BIN` / `_IP_BIN` | `nft`, `wg`, `ip` | binary paths |

## How state survives a restart

`wg0.conf` deliberately contains **no peers** and no `PostUp` hooks. Peers are
added live with `wg set` and re-applied from `/var/lib/autoproxy/peers.json` on
every start; routes are added with `ip route replace`, because wg never touches
the routing table itself. Two writers on `wg0.conf` is how peers silently
disappear after a reboot, and a failing `PostUp` makes wg-quick delete the
interface it has just created.

Rules work the same way: `rules.json` is re-applied on start. Rules whose peer
has been deleted in the meantime are dropped rather than failing the whole set —
one stale entry must not close every working game port — and `last_error` says
so.

Stopping the agent leaves the nftables rules and the WireGuard peers in place.
Players keep playing across an agent restart.

## Generated nftables

One table, `inet autoproxy_rules`, rewritten as a single transaction
(declare, delete, redefine) so it is never half-applied. `flush table` is not
enough: it empties chains but leaves set and map elements behind, which means a
closed port that is silently still open.

```
	set direct_targets {
		type ipv4_addr
		elements = { 10.66.66.2, 10.66.66.3 }
	}

	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		oifname "wg0" ip daddr != @direct_targets masquerade
	}
```

`direct_targets` holds the tunnel address of **every** real-IP peer, whether or
not a rule points at it yet. Traffic to one of those is not masqueraded; that
single `!=` is what preserves the player's address.

The forward chain has `policy drop`, with exactly two accepts: established
traffic, and new sessions the prerouting chain just DNATed.

## Development

Go is not needed on your machine; everything runs in containers.

```bash
# unit tests and vet
docker run --rm -v "$PWD/agent:/src" -w /src golang:1.25-alpine go test ./...

# golden nftables files, checked by real nft
scripts/vps-test/run.sh        # also runs the whole end-to-end gate

# build a static linux/amd64 binary into agent/bin/
scripts/build-agent.sh
```

`scripts/vps-test/run.sh` builds a throwaway Debian 13 image, then runs the
agent inside it with `--cap-add NET_ADMIN --network none`: real nftables, real
WireGuard, no route to anything. It installs, creates peers, pushes both kinds
of rule, checks the live ruleset, exercises the lockout and the token rotate,
restarts to prove state is re-applied, and uninstalls.

Golden files live in `agent/internal/nft/testdata` and are regenerated with
`go test ./internal/nft -update`. Every one of them is then checked with
`nft -c -f` against real nftables.

## Memory

Measured, not estimated: **15.6 MB RSS** with 1000 rules applied (2000 nftables
map elements), on Debian 13 with nftables 1.1.3. The kernel holds the maps, so
this is flat in the number of forwarded ports.

## Licence

MIT. See `LICENSE` at the repository root.
