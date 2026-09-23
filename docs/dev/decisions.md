# Decisions

Newest first. Record why, and what was rejected. No infrastructure values from any real deployment belong here.

## 2026-09-23: Client versions and remote updates (0.3.0)

- Updating a client needed the join code, which the panel shows once. Now: the client carries its real version
  (stamped by `scripts/package-client.sh`), reports it to the agent over the tunnel, the panel shows it with the
  exact update command, `autoproxy-client update` needs no join code, the installer without a join code updates in
  place, and an opt-in per-client button lets the client update itself.
- **Check-in on the existing API port, through the tunnel, identified by tunnel source address.** Rejected: a new
  tunnel-only port. The base firewall only accepts SSH, the WireGuard port and the API port, it is written once by
  setup, and nothing in the agent's own table can accept past its `policy drop`. A new port would have forced every
  existing install to re-run setup (and rewrite `/etc/nftables.conf`), the one thing the table-201 fix showed can be
  avoided. The API port is already accepted on every interface, `wg0` included. Rejected: a second listener bound to
  the tunnel address on the same port (the wildcard listener already holds it; Linux refuses the bind) and
  `SO_BINDTODEVICE` on a second listener (same reason). Rejected: a token for clients (every node would hold a
  secret that the tunnel key already is).
- **`tunnel_guard` in the agent's table** so "addressed to the tunnel address" means "came through the tunnel" even
  for a neighbour on the VPS's own network segment. A drop can live in the agent's table; an accept could not. It
  costs one rule and turns a TCP-handshake-and-routing argument into an explicit filter. Proven on the test setup
  both ways (dropped with it, 401 without it).
- **The check-in body is decoded leniently**, unlike every token route. A check-in only describes its own peer and
  cannot create a forward; strictness there would turn "a newer client added a field" into "versions silently stop
  being reported" until the agent is updated. Every field is still validated, the body capped at 4 KiB.
- **The client decides, the agent only relays a number.** The panel and the agent can name a release `X.Y.Z`; the
  URL, the checksum source and the refusal rules (no downgrade, no reinstall, node switch off, Docker) live in the
  client. One attempt per request id: a failure is not retried every two minutes (GitHub is not hammered and a bad
  asset is not re-downloaded forever); pressing Update again mints a new id.
- **Opt-in twice, off by default in the panel.** Per client in the panel (default off), and a node-side switch the
  API token cannot override (`remote-updates off`). Rejected: node side off by default too. Every node needs one
  hand-run step to reach 0.3.0 anyway, but asking owners to also flip a switch on each node would make the button
  useless for the common case of one admin who owns everything; the node switch exists for the case where panel and
  node have different owners.
- **An old agent is asked once, then every six hours**, and the backoff is in memory: persisting it across restarts
  would leave a client silent for hours after its agent was updated, with no way to hurry it.
- **Hand-run `update` does not report its outcome to the panel**; only a panel request does. A refusal the admin just
  read in the terminal must not also turn the panel red.
- Measured live on the test setup: installer update mode and `autoproxy-client update` during a 5 Hz UDP echo
  through the VPS lost 0 of 200 and 0 of 225 datagrams, handshake timestamp and local port unchanged; a panel-
  requested self-update ran while 800 TCP connects through the VPS to that node all succeeded.

## 2026-09-21: First release ships Debian-only; Ubuntu becomes a follow-up

- Project decision: supported platforms for the first release are Debian 12 and Debian 13. Debian 13 has a passed
  live E2E run on the test setup; Debian 12 is expected to work (same OS gate logic, same package names) but has not
  had a live run yet — docs say exactly that rather than implying both are proven.
- Ubuntu 22.04/24.04 support was implemented and even live-tested against earlier in development, but is dropped
  from what this release promises. Rejected: shipping Ubuntu as "probably fine" alongside the two Debian releases
  that were actually proven — an untested promise is exactly what `docs/dev/testing.md` and the hard rules warn
  against. Cut instead of leaving it half-verified.
- The OS gate (`agent/internal/setup/host.go`'s `supported` map, and the matching case statements in
  `installers/install-vps.sh`, `installers/install-client.sh` and `client/autoproxy-client`) now lists Debian only,
  but Ubuntu gets its own message ("Ubuntu is not supported in this release... tracked for a later release")
  instead of falling into the generic unsupported-OS refusal, so a Ubuntu user is told this is temporary, not that
  the project rejected their platform. Adding Ubuntu back is one list entry per gate, by design.
- The "about five minutes" quick-start claim is a measured timing from the test setup's own runs (see
  `docs/dev/testing.md`'s live test plan, step 6), not a stranger's first attempt; `docs/quickstart.md` states that
  basis once rather than implying a guarantee.
- Release-checklist screenshots stay unfulfilled (no change) — that gate is expected to keep failing until someone
  actually takes them.

## 2026-09-21: Peer routes move out of the main table, onto a fwmark and a dedicated rule

- Live proof (documentation-range values standing in for the test setup's real ones): a site peer with
  `lan_cidrs ["198.51.100.75/32"]` whose client endpoint was `198.51.100.75:42625` never completed a handshake.
  `wg show` on the VPS showed bytes received from the peer and bytes counted as "sent," the client showed bytes
  sent and 0 received, and neither side ever got a `latest handshake`. `ip route get 198.51.100.75` on the VPS
  resolved `dev wg0` — the agent's own `ip route replace 198.51.100.75/32 dev wg0` (added because that address was
  also in the peer's `lan_cidrs`) beat the interface route to the peer's real endpoint
  (`198.51.100.0/24 dev <public-iface>`). The VPS's own encrypted handshake and keepalive packets to that endpoint
  were therefore routed back into the tunnel they were trying to open — a loop, not a reachability problem, and one
  that produces no error anywhere: the plugin's Setup page just sits on "waiting for the first handshake" with
  nothing to tell the admin why.
- Cause: `autoproxy-agent` adds one `ip route replace <cidr> dev wg0` per peer `AllowedIP` into the **main** routing
  table (`agent/internal/wg.Manager.AddRoute`, called from `agent/internal/peers.Manager.applyLocked`). A site
  peer's `AllowedIPs` include its `lan_cidrs`, and nothing stops one of those from covering the address WireGuard
  is dialing for that peer's own transport, because the agent has no way to know that address at peer-creation
  time — it is not part of the peer-create request, WireGuard resolves the endpoint itself from the config the
  client was given, and a roaming client's endpoint can change afterwards without telling the agent at all.
- Fixed by giving the VPS the same construction `wg-quick` uses for `AllowedIPs 0.0.0.0/0`: peer routes go into a
  dedicated routing table instead of main (`ip route replace <cidr> dev wg0 scope link table 201`), the interface's
  own transport socket is tagged with a fwmark (`wg set wg0 fwmark 0x2b`), and an `ip rule add not fwmark 0x2b
  lookup 201 priority 90` sends everything that is *not* the interface's own marked traffic through the dedicated
  table first, while the interface's own marked packets fall through to the main table and reach the peer's real
  endpoint. Verified by hand on the test VPS: applying exactly this (`wg set wg0 fwmark 0x2b`; `ip route replace
  <cidr> dev wg0 scope link table 201`; `ip rule add not fwmark 0x2b lookup 201 priority 90`) completed the
  handshake within about 5 seconds, with `ip route get <endpoint>` and `ip route get <endpoint> mark 0x2b` both
  resolving to the VPS's normal public interface afterwards, and all other traffic to the peer's CIDR still
  resolving `dev wg0` via table 201.
- Numbers chosen deliberately, distinct from the node client's own fwmark `0x2a` / table `200` / priority `100`
  (`client/autoproxy-client`): the VPS is a different machine solving the mirror-image problem (marking the
  tunnel's own transport traffic to steer it OUT of the peer table, where the client marks game traffic to steer it
  INTO the tunnel). Reusing the client's numbers would have been harmless in practice — different machines, no
  shared namespace — but picking different ones makes a box that somehow runs both roles fail loudly on a
  collision instead of colliding silently. `agent/internal/wg.DefaultFWMark` / `DefaultTable` /
  `DefaultRulePriority` are the single source of truth; `setup`, `uninstall` and the daemon's own start-up path all
  reference them rather than repeating the literals.
- Applied unconditionally and repeatedly, not just once at setup: `agent/internal/setup.Run` sets the fwmark and
  installs the rule live right after `wg-quick@wg0` starts (so the operator watching setup output sees it, and a
  kernel that refuses `ip rule` says so there); `WG0Conf` also writes a static `FwMark` line into
  `/etc/wireguard/wg0.conf` itself, so the mark is true from the instant `wg-quick` brings the interface up on
  boot, before the agent has had a chance to run at all; and `peers.Manager.Restore` and `applyLocked` re-ensure
  both on every daemon start and on every single peer application (`wg set ... fwmark` is declarative and `ip rule
  show` is a cheap check, so paying for it repeatedly costs nothing). Three layers because each covers a window the
  others don't: setup covers "operator is watching right now," `wg0.conf` covers "the box rebooted before the agent
  ever ran," and the daemon path covers "an existing install is upgraded in place." `AddRoute` also migrates away
  any route a pre-fix agent left in the main table for the same CIDR, best effort and silently — an upgrade that
  never restarted the agent leaves nothing to migrate, and a route left there by something unrelated is not this
  call's business to diagnose.
- Rejected: validating `lan_cidrs` against the address the client will use as its own endpoint. That address is not
  known at peer-creation time (WireGuard resolves it from the client's own config, not from anything the agent
  sees when a peer is created) and can change later if the client roams, so there is nothing stable to validate
  against; a check that only catches the case where the endpoint happens to match what was true at creation time
  would give a false sense of safety while leaving the live bug's own reproduction case uncaught. The fix therefore
  has to make the routing itself loop-safe regardless of what `lan_cidrs` contains, not try to reject the
  dangerous inputs at the door. (A narrower, different check — refusing a `lan_cidrs` range that contains the
  VPS's *own* known-and-stable public address, which is a real misconfiguration in its own right and unrelated to
  the roaming-endpoint problem above — is a separate guard in `peers.Manager.checkCIDRsLocked`.)
- Rejected: reusing the client's fwmark/table/priority on the VPS. Harmless today since the two never share a
  network namespace, but picking distinct values costs nothing and turns a hypothetical mixed-role mistake into an
  immediate, loud failure instead of two unrelated routing decisions silently sharing state.
- Rejected: relying solely on the daemon's start-up path to install the fwmark and rule. It would leave a real gap
  between "the interface comes up at boot" and "the agent gets around to starting," during which any peer routes
  still in the live kernel state from before a reboot (there are none immediately after a reboot, but a `wg-quick`
  restart without a full reboot is a case worth covering too) could still race the interface's own unmarked
  handshake traffic. Writing `FwMark` into `wg0.conf` itself closes that gap without adding a `PostUp` hook (which
  the VPS already avoids on purpose — see "Two deliberate deviations on the VPS" in
  [architecture.md](architecture.md) — a plain `[Interface]` key carries none of that risk).

## 2026-09-21: `up` leaves an already-matching tunnel alone
- A `systemctl restart autoproxy-client` dropped traffic for roughly 15-30 seconds, so it did disconnect players -
  the opposite of what the client is documented to do. Isolated live: `wg syncconf` on its own, with the nftables
  table untouched, froze the interface for about 13 seconds (rx/tx counters unmoved, handshake timestamp unchanged).
- Cause: `up` re-synced the interface on every start. Handing the kernel a private key rekeys the tunnel even when
  it is byte-for-byte the same key, because session secrets are derived from it. Nothing inbound can trigger the new
  handshake - the client drops what it can no longer decrypt - so recovery waits on the 25-second keepalive.
- `up` now compares the live interface against the config on four points (private key, the peer's public key, its
  endpoint, persistent keepalive) and skips `wg syncconf` entirely when all four match, logging that it is leaving
  the live session untouched. Anything different, or an interface with no key, gets the full sync and a log line
  saying this rekeys the tunnel. Verified live on both paths: a full service restart during a once-per-second UDP
  exchange lost 0 of 15 packets with the handshake timestamp unchanged, and the restart where the key genuinely
  differed logged the rekey and applied it.
- Rejected: feeding `syncconf` the config with the `PrivateKey` line removed. This is worth recording precisely
  because it looks correct and was tried. WireGuard reads a missing key as "this interface has no key" and clears
  it: the interface reported public key `(none)`, could never handshake again, and the node went dark until the
  client was re-run with a config it considered changed. The cure for the rekey is to skip the sync, never to sync
  without the key.
- Rejected: skipping `syncconf` altogether whenever the interface already exists. It would fix the restart, but a
  changed peer endpoint or keepalive would then never be applied, and the failure would be silent.

## 2026-09-21: The client masquerade is site-mode only, and matches on destination
- First live end-to-end test: in real mode the game server saw the bridge gateway (a 172.x address), not the
  player's address. The masquerade chain was rendered in both modes on the assumption that real-mode traffic is
  delivered to a local address and never reaches postrouting. On a Wings host the game server is in a container, so
  the packet is forwarded across a bridge and does reach postrouting.
- The chain is now rendered only in site mode, and scoped to that peer's own `lan_cidrs` by destination, not by
  interface name. In real mode there is no postrouting chain at all. Verified live: the echo server reported the
  bridge gateway before the change and the real client address after it.
- Rejected: adding `pelican*` to the interface-name exclusion list. It fixes this panel on this day only — any
  bridge rename, any other panel, and the player's IP is silently destroyed again, with nothing in any log to say
  so. A destination match cannot be defeated by a rename.

## 2026-09-21: A headless `autoproxy:setup` command alongside the Setup page
- Unattended installs, scripted rebuilds and headless panels need a way in that does not involve opening the admin
  UI. The command calls the same service classes the Setup page calls, so the two cannot drift.
- The Setup page stays the documented normal path; the command is the alternative, not the replacement.
- `store-code` prefers `--code-file` over `--code`: an argument is visible in `ps` and in shell history.

## 2026-09-21: Public fork "Pelican Auto Proxy"
- Name final before first release (plugin id `autoproxy`); renaming later breaks updates.
- Panel talks to the VPS over a token-protected HTTPS API with a self-signed certificate trusted as its own CA (PEM shipped in
  the setup code). Rejected: tunnel-only relay (needs a client on the panel host), and pin-only without hostname/IP checking.
- One tunnel client per node host; the VPS manages peers live. Real player IPs when the client runs on the Wings host.
  Site mode (masquerade) for other machines on a LAN. Rejected: shared IP only.
- Plugin generates every command with the user's values. Rejected: docs only.
- Platforms: Debian 12/13, Ubuntu 22.04/24.04. Rejected: best-effort others (untested promises).
- MIT, fresh history for the public repo, hub listing.

## Carried over from the prototype (2026-09-20)
- Kernel forwarding (nftables DNAT over WireGuard), never per-port userspace proxies: memory independent of port count.
- Reconcile every minute is the source of truth; model observers are unreliable (bulk delete and server deletion bypass events).
- Alias keyword marks an allocation public; only allocations with an assigned server are forwarded.
- Rules are pushed as a full set; conflicts withhold both sides; the agent validates independently (reserved ports, RFC1918 targets).
- Tunnel interfaces live in the host network namespace so a client restart does not disconnect players.
- Traps learned live: Docker's FORWARD policy is drop (DOCKER-USER accepts required); the VPS peer needs routes to the targets;
  an orphaned Docker nft table with a drop policy silently kills forwarding; a UDP echo target on the gateway host itself answers
  from the wrong address (test against a single-homed host); never `flush ruleset`, only per-table flushes.
