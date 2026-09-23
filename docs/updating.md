# Updating

Each of the three parts updates independently — you do not need to update the VPS, every node, and the plugin at
the same time, though checking the [changelog](../CHANGELOG.md) for breaking notes before mixing old and new
versions for long is worth the minute it takes.

## Order: VPS agent, then plugin, then clients

When a release touches all three (0.3.0 does), update in this order:

1. **VPS agent** first. A newer agent keeps serving older clients and an older plugin exactly as before.
2. **Plugin** next. It reads what the new agent offers; against an agent older than 0.3.0 it still works, and simply
   says the agent has to be updated before client versions can be shown.
3. **Tunnel clients** last. A 0.3.0 client against an older agent keeps forwarding normally; it only cannot report its
   version (it tries once, writes one line to its log, and asks again six hours later or when it is restarted).

Nothing here needs the node's join code, and no step disconnects players.

![The Plugins page with Pelican Auto Proxy installed](img/plugins.png)

## VPS agent

**Upgrading from 0.2.x to 0.3.0 needs only the new binary and a restart.** Everything 0.3.0 adds on the VPS lives in
the agent's own nftables table, which it rewrites whenever it starts, so `/etc/nftables.conf` does not change and
setup does not have to run again. On the VPS as root:

```bash
cd /tmp
curl -fsSLO https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/autoproxy-agent_linux_amd64
curl -fsSLO https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS          # must print: autoproxy-agent_linux_amd64: OK
cp -p /usr/local/bin/autoproxy-agent /usr/local/bin/autoproxy-agent.previous
install -m 0755 autoproxy-agent_linux_amd64 /usr/local/bin/autoproxy-agent
systemctl restart autoproxy-agent
autoproxy-agent -version
```

Forwards and tunnels stay up across the restart; the agent re-applies both from its state on start. To go back,
put `autoproxy-agent.previous` in place the same way and restart: an older agent rewrites its table without the
0.3.0 additions, and ignores the client state file 0.3.0 added (`/var/lib/autoproxy/clients.json`).

Re-running the installer also works, and is the way to pick up a change in how setup renders the base firewall or
the units:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-vps.sh | sudo bash
```

This downloads the latest checksummed binary and runs setup again. Setup is idempotent: it never regenerates your
existing token, certificate or WireGuard key, so nothing already forwarding needs to be reconfigured, and no
existing peer needs to rejoin. It only refreshes derived config (units, base firewall tables) if something about how
setup renders them has changed.

To pin a version instead of taking the latest, set `AUTOPROXY_VERSION` to a release tag:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-vps.sh \
  | sudo AUTOPROXY_VERSION=v1.2.3 bash
```

See [dev/release.md](dev/release.md) for how versions are tagged.

## Node client

No join code is needed. The panel's **Status** page shows every client's version and, when a newer release exists,
the exact command for that machine with a Copy button. There are three ways to run it.

**From 0.3.0 on**, on the node host:

```bash
sudo autoproxy-client update
```

It looks up the latest release, downloads `autoproxy-client.tar.gz` and `SHA256SUMS` from the same GitHub release the
installer uses, refuses to install anything whose checksum does not match, replaces the script and the unit, and
restarts the client. The restart leaves the tunnel alone (no rekey), so players stay connected; the output ends with
"the tunnel was left untouched". Options:

- `--version v0.3.1` installs that release instead of the latest.
- It refuses a **downgrade** (and says so); add `--force` to go back to an older release deliberately.
- `--no-restart` installs the files and leaves the running client alone until you restart it.

**Clients older than 0.3.0** have no `update` command yet. Run the installer **without** a join code; on a machine
that already has a client, that updates it in place and keeps its configuration and keys:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-client.sh | sudo bash
```

(Running the installer with the same join code as the first time still works too, and gives the same result.
`AUTOPROXY_VERSION=v1.2.3` pins a release for either form.)

**One click from the panel** (0.3.0 clients on the system-service flavour): on the Status page, press **Allow remote
updates** for that client, then **Update to …**. The client picks the request up within about three minutes, runs the
same update as above, and reports back; the Status and Setup pages show *requested*, then *updated*, or *failed* with
the client's own reason (for example a checksum mismatch or an unreachable GitHub). Nothing is changed on the node
when it fails. Remote updates are off until you allow them, and the node's owner can refuse them with
`sudo autoproxy-client remote-updates off`; see [security.md](security.md) for exactly what this allows.

**Docker flavour:** the container cannot replace itself. Pull the new image and recreate it, in the folder with its
`compose.yml`:

```bash
docker compose pull && docker compose up -d
```

The same `AUTOPROXY_JOIN_CODE` in the compose file keeps it tied to the same peer.

## Plugin

Pelican's one-click plugin update only appears when a plugin publishes an `update_url` the panel can poll — Auto
Proxy's `plugin.json` sets this, but until the hub listing is live, updating is a manual re-import, the same as a
first install:

1. Download (or build) the new version's zip.
2. **Admin → Plugins → Import**, upload it. Because the zip's plugin id (`autoproxy`) matches the plugin already on
   disk, the panel replaces the old plugin folder with the new one — nothing is merged by hand.
3. The plugin row reads "Not installed" again after a re-import; press **Install**, same as a first install. This is
   a queued job, so it needs the panel's queue worker (or scheduler-driven queue) running — the same dependency the
   sync banner already tells you about if it's down.
4. Confirm the plugin is still **enabled** afterwards; a re-import occasionally resets that flag.

**What survives a plugin update:** your Setup configuration (VPS endpoint, token, certificate), every node's mode
and peer state, manual forwards, and settings — these live in the plugin's own database tables, which Laravel's
migrator only ever adds to for a new version, never drops or recreates. You will not need to re-paste the VPS code
or re-run any join command just because the plugin itself updated.
