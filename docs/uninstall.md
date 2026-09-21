# Uninstalling

Each part removes only what it created. There's no combined "remove everything" command across all three, because
they run on different machines by design — uninstall each one on the machine it's actually on.

## VPS agent

```bash
sudo autoproxy-agent uninstall
```

Prints the exact list of what it's about to remove — the `autoproxy-agent` and `wg-quick@wg0` systemd units, the
`wg0` interface (and every peer route that referenced it, in whatever routing table it was in), the peer-routing
`ip rule` (`not fwmark 0x2b lookup 201`, priority `90` — this outlives the interface because it names a table and
a mark rather than a device, so it needs removing on its own), `/etc/autoproxy`, `/var/lib/autoproxy`, the `inet
autoproxy_base` and `inet autoproxy_rules` nftables tables, `/etc/wireguard/wg0.conf` and the server keys,
`/etc/sysctl.d/99-autoproxy.conf`, the unit file and the binary — and asks for confirmation before doing it. Pass
`--yes` (or `-y`) to skip the prompt (for scripted teardown, e.g. before decommissioning a VPS entirely).

It also restores `/etc/nftables.conf` from `/etc/nftables.conf.pre-autoproxy` if the installer kept a copy. If there
is no copy and the current file is one the installer wrote, it is deleted — the machine then has **no firewall**,
which the uninstaller says out loud before it starts. Install your own.

**What stays:** the `wireguard-tools` and `nftables` packages, and anything you added yourself outside those paths —
including the `autoproxy_apiguard` table from [security.md](security.md) if you created it. Remove that separately
with `sudo nft delete table inet autoproxy_apiguard`. SSH configuration and keys are never touched by install or
uninstall.

## Node client

```bash
sudo autoproxy-client uninstall
```

Removes the `autoproxy-client` systemd unit, `/etc/autoproxy/client.json`, `/etc/wireguard/autoproxy0.conf`,
`/etc/sysctl.d/90-autoproxy.conf`, `/usr/local/bin/autoproxy-client`, the `autoproxy0` interface, the `inet
autoproxy_client` nftables table, its `ip rule` and routing table, and the `DOCKER-USER` accept rules if it added
them — same "list first, ask unless `--yes`" behaviour as the agent. (`autoproxy0` is the node host's tunnel
interface; the VPS's is `wg0`.) The live `ip_forward` and `rp_filter` kernel values are left as they are until the
next reboot; only the file that made them persistent is removed.

For the Docker flavour, remove the container and its image the normal Docker
way (`docker compose down`, or `docker rm -f` plus `docker rmi`) — uninstall inside the container handles the
in-container config, not the container's own existence.

**What stays:** the game server and Wings installation themselves — this only removes the tunnel and its firewall
rules, nothing Pelican-related.

## Plugin

**Admin → Plugins**, find Auto Proxy, and remove it the same way you would remove any other Pelican plugin. This
drops the plugin's own database tables (Setup configuration, node modes, manual forwards) — there is no separate
"keep my settings for later" option, so note down your VPS code first if you plan to reinstall later rather than
starting over with a fresh one.

**What stays:** the VPS agent and any node clients keep running exactly as they were — removing the plugin only
removes the panel's ability to manage them. Existing forwards on the VPS keep serving traffic (nothing pushes an
empty rule set on plugin removal); uninstall the agent and clients separately if you want forwarding to actually
stop.
