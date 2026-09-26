# Pelican Auto Proxy - tunnel client

Runs on a node host (the machine running Wings, or any LAN machine you want to forward to) and dials
out to the Pelican Auto Proxy VPS agent over WireGuard. It brings up a dedicated interface
(`autoproxy0`) and a small set of nftables/routing rules so players reach your services either with
their real source IP, or through a masqueraded connection for machines elsewhere on your LAN.

You normally never run this script by hand: the Pelican Auto Proxy plugin's Setup page generates the
exact install command (with your join code already filled in) for you to paste. This page is for
understanding what that command does, and for troubleshooting.

## Real mode vs. site mode

The join code the plugin gives you already carries a `mode`, chosen when the peer (node) was created:

- **Real mode**: the client runs on the same host as Wings. Players connect and the game server sees
  their actual IP address (bans, per-player limits, GeoIP all work normally). This is the headline
  feature and the common case.
- **Site mode**: the client runs on some other machine on your LAN (or the same machine, forwarding to
  a different host) and the VPS agent's rules point at a LAN IP behind this peer instead of at the peer
  itself. Traffic is masqueraded (shared IP), the same trade-off as any traditional reverse proxy.

Both modes use the same tunnel and almost the same firewall rules on this host. The one difference is
the masquerade rule: it is written only in site mode, and only for the LAN ranges that peer was given.
In real mode it is not written at all, so nothing on this host can rewrite a player's address. See
[`docs/dev/architecture.md`](../docs/dev/architecture.md), "Real-IP ruleset (client side)", for the
full technical rationale.

This host's tunnel interface is `autoproxy0`. The VPS's is `wg0` — different machines, different
names, and they never need to match.

## Install (systemd flavour, the default)

```
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-client.sh \
  | sudo bash -s -- <join-code>
```

This is generated for you by the plugin with the real join code already in place. It:

1. Refuses to run on anything except Debian 12/13 (tested live on 13; 12 is expected to work but not yet tested).
   Ubuntu is not supported in this release — support for it is tracked for later.
2. Installs `wireguard-tools`, `nftables`, `curl`, `iproute2` via apt (without pulling a kernel image:
   your distro kernel already has WireGuard built in or as a loadable module).
3. Decodes the join code and writes `/etc/autoproxy/client.json` and `/etc/wireguard/autoproxy0.conf`
   (both `0600`, root-only: the second one holds your tunnel private key).
4. Installs the `autoproxy-client` binary to `/usr/local/bin` and a systemd unit
   (`autoproxy-client.service`), then enables and starts it.

Add `--no-start` to install without starting (useful for staged rollouts), or `--yes` to skip the
confirmation prompt (already implied when there is no terminal attached, e.g. when piped from curl).

### --host-ip fallback

If your allocations are bound to a specific LAN IP instead of `0.0.0.0`, Docker/Wings never sees a
packet addressed to the tunnel IP, so real-mode forwarding silently does nothing. Use:

```
... install <join-code> --host-ip 192.0.2.50
```

This adds a `dnat` rule (priority `-101`, ahead of Docker's own DNAT at `-100`) that rewrites the
destination from the tunnel IP to your LAN IP for TCP and UDP, before Docker's rules ever see the
packet. Only use this if `autoproxy-client status` shows a handshake but connections still fail to
reach the game server.

## What it changes on the machine

- `/etc/autoproxy/client.json` - the decoded join code (mode, keepalive, LAN CIDRs, etc.), `0600`.
- `/etc/wireguard/autoproxy0.conf` - the WireGuard interface config, `0600`.
- `/usr/local/bin/autoproxy-client` and `/etc/systemd/system/autoproxy-client.service`.
- `/etc/sysctl.d/90-autoproxy.conf` - `net.ipv4.ip_forward=1`,
  `net.ipv4.conf.autoproxy0.rp_filter=2` (loose reverse-path filtering on this interface only; see
  Troubleshooting).
- A `autoproxy0` WireGuard interface, `Table = off` (it never touches your default route directly; a
  separate policy-routing rule does that - see below).
- One `ip rule` (`fwmark 0x2a lookup 200 priority 100`) and a default route in routing table 200 via
  `autoproxy0`, so only marked (proxied) traffic uses the tunnel.
- The `inet autoproxy_client` nftables table: marks new inbound tunnel connections and restores that
  mark on replies (both for forwarded and for locally-terminated services). In site mode it also gets a
  masquerade rule for traffic this host forwards on to the LAN ranges that peer covers. In real mode
  that rule is absent, which is what keeps the player's own address intact all the way to the game
  server.
- Two accept rules in Docker's `DOCKER-USER` chain, if that chain exists (see Troubleshooting).
- `/etc/autoproxy/remote-updates`, only if you run `remote-updates on|off`, and runtime state in
  `/run/autoproxy-client/` (the last update request acted on; cleared by a reboot).

Run `autoproxy-client status` any time to see exactly what's live. `autoproxy-client uninstall` lists
everything above before removing it, and asks for confirmation unless you pass `--yes`.

## Commands

```
autoproxy-client install <join-code> [--yes] [--no-start] [--host-ip <lan-ip>]
autoproxy-client up                     # idempotent: bring the tunnel + rules up
autoproxy-client run                    # up, then log health forever (what the service runs)
autoproxy-client status [--json]        # exit 0 if healthy, 1 otherwise
autoproxy-client down                   # tear down everything up/run created
autoproxy-client uninstall [--yes]      # remove files, unit, sysctl config, network state
autoproxy-client update [--version vX.Y.Z] [--force] [--no-restart]
                                        # install another official release, no join code needed
autoproxy-client remote-updates [on|off|status]
                                        # allow or refuse updates requested from the panel
autoproxy-client version
```

`update` downloads `autoproxy-client.tar.gz` and `SHA256SUMS` from this project's GitHub release (the latest, or the
one `--version` names), refuses on a checksum mismatch exactly as the installer does, replaces the script and the
unit, and restarts the service, which leaves the tunnel untouched. It refuses a downgrade unless `--force` is given,
and refuses in the Docker image (pull a new image instead). `AUTOPROXY_RELEASE_BASE` points it at a local mirror of a
release directory for tests; it still checks the checksum and says loudly that it is set. Never use it outside a
test.

## Version reports and remote updates

The script carries its release number (`AUTOPROXY_VERSION`, stamped by `scripts/package-client.sh` when a release is
built; a checkout of the source says `dev`). While the tunnel is up, `run` sends it every two minutes to the agent's
check-in route on the VPS's tunnel address, `https://10.66.66.1:<api port>/v1/tunnel/checkin`, from this machine's
own tunnel address. The API port comes from the join code (agents from 0.3.0 put it there); a config written by an
older client has none and 7443 is assumed, which `AUTOPROXY_API_PORT` in the service's environment overrides. There
is no token on this route: the agent knows the client by its tunnel source address, which WireGuard ties to this
peer's key, and the certificate is not checked (`curl -k`) because the client does not have it and the tunnel has
already authenticated both ends. An agent older than 0.3.0 answers 401; the client logs that once and asks again six
hours later or after a restart, so an old agent is not flooded with warnings.

The answer may name a release the panel asked this client to install. The client starts `autoproxy-client update
--version <that release>` in its own transient systemd unit (`autoproxy-client-update`, so the restart does not kill
it) only when remote updates are on here, the release is newer than this one, and it has not tried that request yet;
otherwise it reports why not. The next check-in carries the result: updating, updated, or failed with the reason.
See [../docs/security.md](../docs/security.md).

`status --json` is meant for the plugin and for your own monitoring; it reports the same fields as the
human-readable output as a single JSON object.

Restarting the service (`systemctl restart autoproxy-client`, or a crash) does **not** disconnect
players: `run` only tears down its own logging loop on `SIGTERM`, leaving the interface, routes and
firewall rules exactly as they were, and `up` is idempotent so the next start just re-syncs them.
That re-sync is skipped entirely when the live tunnel already matches the config — same key, same
peer, same endpoint, same keepalive — and the log says so. Re-applying a configuration WireGuard
already has rekeys the tunnel anyway, because the session secrets come from the private key, and the
tunnel then carries nothing until the next handshake: that is what used to make a restart cost
players around 15-30 seconds of dead traffic. If anything really has changed, the full sync runs and
the log tells you the tunnel is being rekeyed, so a new endpoint or keepalive still takes effect.

A restart keeps the tunnel's local UDP port, so it cannot help when the network between this machine
and the VPS drops that one flow. That case is handled separately: after 3 minutes without a handshake
(WireGuard's own limit for a session), `run` moves the tunnel to a new random local port and
re-resolves the VPS endpoint, then does it again every 3 minutes while the tunnel stays down. This
changes no key and no config, so it cannot disturb a working tunnel; the VPS follows the new port by
itself. After 10 minutes `run` still exits so the service manager restarts it.

A path can also fail without the handshake going stale: if the network drops nearly every packet
towards the VPS but lets the odd one through, handshakes keep renewing while the tunnel carries
nothing. The two-minute version report (0.3.0 agents and later) notices: when it times out twice in a
row, retried after one minute, `run` moves the tunnel to a new local port the same way and logs
`2 reports to the VPS in a row timed out although the handshake looks fresh: moved the tunnel ...`.
Only a timeout counts. A refused connection or an old agent's 401 means packets did come back, so the
path works and the port stays.

## Docker / Compose flavour

The normal way is the published image, `ghcr.io/finnwastakenwastaken/autoproxy-client` (`:latest`, or `:vX.Y.Z`
for one release; from 0.3.3 on, `linux/amd64`), with the Compose snippet the plugin's Setup page prints; see
[docs/install-client.md](../docs/install-client.md). `compose.yml` in this directory builds the image from source
instead:

```
cd client
cp .env.example .env   # fill in AUTOPROXY_JOIN_CODE
docker compose up -d
```

A source build reports its version as `dev`, and the plugin then cannot tell whether it is up to date. Set
`AUTOPROXY_VERSION=X.Y.Z` in `.env` (or pass `--build-arg AUTOPROXY_VERSION=X.Y.Z` to `docker build`) when you build
a release's source, and it reports that number, exactly as the published image does.

The container needs `network_mode: host` and `cap_add: NET_ADMIN` (already set in `compose.yml`): the
WireGuard interface and nft tables must live in the host's kernel network namespace, or a container
restart would disconnect every player. `sysctls:` is intentionally not set in the compose file - it has
no effect together with `network_mode: host`, since there is no separate network namespace for it to
apply to; the entrypoint sets `net.ipv4.ip_forward` and the interface's `rp_filter` on the host directly
the same way the systemd flavour does.

Unlike the systemd flavour, the container skips `install`: on first start, `run` reads
`AUTOPROXY_JOIN_CODE` from the environment and writes the config files itself. The image is Alpine and
does not include `python3` (keeps the image small), so it always uses the pure-bash join-code decoder;
the systemd flavour on Debian/Ubuntu uses `python3` when it's present (it usually is) and falls back to
the same bash decoder otherwise. Both paths decode to identical results - the test suite in
`client/tests/` checks this.

### Coolify

Add this repository's `client/` directory as a Docker Compose resource, base directory `client`, and
set `AUTOPROXY_JOIN_CODE` in the environment. Host networking and `NET_ADMIN` are handled by
`compose.yml`; nothing else to configure.

## Uninstall

```
sudo autoproxy-client uninstall --yes
```

Removes everything listed under "What it changes on the machine" above. It does **not** remove the apt
packages (`wireguard-tools`, `nftables`, `curl`, `iproute2`) - `apt-get remove` those yourself if you
want them gone too. It also does not force `net.ipv4.ip_forward`/`rp_filter` back to their previous
value; only the persistence file is removed, so the live kernel value stays as-is until reboot.

## Troubleshooting

**`autoproxy-client status` shows a handshake but no traffic reaches the server.**
Check whether your allocations are bound to a specific LAN IP rather than `0.0.0.0` (common with some
panel configurations). If so, re-run install with `--host-ip <your-lan-ip>` (see above).

**No handshake at all.**
Confirm outbound UDP to the VPS's WireGuard port isn't blocked by your ISP/router, and that the VPS is
actually listening (`autoproxy-agent` on the VPS side must be up). `autoproxy-client run` exits after
600 seconds without a handshake so systemd restarts it - repeated restarts in `journalctl -u
autoproxy-client` are themselves a symptom of this.

**Docker containers can't be reached even though the host can.**
`autoproxy-client up` inserts two accept rules into Docker's `DOCKER-USER` chain if that chain exists.
If Docker uses the newer nftables backend instead of iptables, that chain won't exist and you'll see a
warning in the log ("nftables backend, untested") - please report this with your Docker version so we
can add proper support; for now, forwarding to containers on such a host is unverified.

**`rp_filter` looks wrong / "martian" packets dropped.**
`autoproxy-client up` sets `net.ipv4.conf.autoproxy0.rp_filter=2` (loose mode), which is what real-mode
forwarding needs. A host-wide `net.ipv4.conf.all.rp_filter=1` does **not** override this: Linux takes
`max(all, interface)`, so the interface-specific `2` always wins. If `status` still shows something
other than `2` on `autoproxy0`, something else (cloud-init, another security tool) is resetting it after
we set it - that's worth investigating on the host itself, not a client bug.

**An orphaned `autoproxy_client` nft table survives a botched uninstall.**
`autoproxy-client down` (or `uninstall`) always deletes-then-recreates rather than appending, so
re-running `up` after a partial failure is safe. If you ever need to check by hand:
`nft list table inet autoproxy_client`.

## Not verified by this repository's own tests

Everything above is exercised in throwaway Docker containers on a development machine (never with real
network privileges on that machine itself - see the project `CLAUDE.md`). The following need a real
Wings host or VM and are tracked in [`docs/dev/testing.md`](../docs/dev/testing.md)'s live test plan
and in [`docs/dev/architecture.md`](../docs/dev/architecture.md)'s "only settled by testing" list:

- An actual WireGuard handshake against a live VPS agent.
- The `type route hook output` restore chain re-routing replies for locally-terminated (host-network)
  services in practice.
- Real interplay with a running Docker daemon (DOCKER-USER insertion against live container traffic,
  behaviour on the nftables Docker backend).
- Debian 12's exact nftables/WireGuard package versions (Debian 13 has a passed live run; Debian 12 does not yet).
  Ubuntu is out of scope for this release.
