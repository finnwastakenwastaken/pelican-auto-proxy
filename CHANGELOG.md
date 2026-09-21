# Changelog

All notable changes are recorded here. Format: Keep a Changelog. Versions: SemVer.

## [Unreleased]

Nothing yet.

## [0.2.2] - 2026-09-21

Found in the same timed install, one step later. The plugin is unchanged; the version moves because the tag does.

- Client installer: the download URL was built from the OS version ("13 (trixie)") instead of the release tag,
  because sourcing `/etc/os-release` overwrote the variable holding the tag. Every install that did not use
  `AUTOPROXY_LOCAL_TARBALL` failed with `curl: (3) URL rejected`. The variable is renamed.
- Client installer: resolving the latest release could stop silently: `curl | grep -m1` closed the pipe early,
  curl exited 23, and `pipefail` ended the script with no message. The response is fetched first and parsed second.
- New gate `client/tests/test-install-resolve.sh`, also in CI: both installers, exactly as the docs print them,
  against the latest public release, must reach checksum verification. It fails on the 0.2.1 installers.

## [0.2.1] - 2026-09-21

Found in the first timed install by the maintainer, on a panel reached over plain http.

- Setup page: the Copy button floated on top of the join command and hid its end once the command was long
  enough to scroll. The button now sits above the block. Clicking the command selects all of it, and when a
  browser refuses the clipboard the page says so and leaves the text selected for Ctrl+C instead of pretending.
- Setup page: the node mode dropdown was one grid column wide and cut its own labels off mid-word; it now spans two.
- Client: the uninstall summary no longer suggests removing `nftables`. On Debian, Docker depends on it, so following that hint on a Wings host stopped Docker and every game server (seen on the test rig). The docs say the same.

## [0.2.0] - 2026-09-21

First public release, before 1.0: a public fork of an earlier single-deployment prototype, renamed throughout and
extended for multiple layouts. Everything below has passed the automated gates and a live end-to-end run on
Debian 13 (real player IPs, remote node, site mode). Debian 12 has passed the container gate only. What is still
missing before 1.0 is in [docs/dev/release.md](docs/dev/release.md): screenshots, a timed first-time install, and a
run on an internet-facing VPS. Treat the plugin's update mechanism and the install commands as final; treat the
docs as still settling. The three parts version independently; the plugin is at 0.2.0
(`plugin/autoproxy/plugin.json`), and the agent is stamped from the git tag at build time.

- `scripts/publish-sync.sh` brings an existing public checkout up to date from an export and commits under the
  public identity, so the public repository keeps its history from 0.2.0 onward.

- CI: the shellcheck job read its file list through a multi-line expression expansion, which ran every script after the first instead of linting it; the PHP job used a shell without process substitution; actions bumped to Node 24 runtimes (checkout v5, setup-go v6, action-gh-release v3) ahead of the Node 20 removal.
- Release workflow: a manual dry run (workflow_dispatch) builds and checks every asset and uploads them as an artifact without publishing, so the workflow can be exercised before the first tag and on a fork.
- `scripts/publish-init.sh` produces the public repository from an export: fresh single-commit history under the public identity, author sweep included, no push.
- The VPS container gate's Dockerfile takes `DEBIAN_CODENAME` (default trixie); run once against bookworm, 80/80 checks pass on Debian 12 as well.

### Plugin (admin UI)

- `php artisan autoproxy:setup` now covers tunnel clients and manual forwards, not just nodes: `clients`,
  `add-client`, `client-join-command`, `remove-client`, `forwards`, `add-forward` and `remove-forward`. A site
  client for a LAN and a forward through it could previously only be created in the browser, which left scripted
  rebuilds and headless panels with no way to finish a site-mode setup.
- The rules a manual forward has to pass moved out of the Forwards form into `Support\ForwardRuleInput`, and the
  checks on a new site client moved out of the Setup page into `Support\ClientInput`. Both the admin pages and the
  command now call them, so the two cannot drift into accepting different things. `test/rules-test.php` covers
  them directly.
- `add-client` prints the VPS's own refusal word for word when it rejects a LAN range — it refuses a range that
  covers the VPS's public address or the tunnel subnet, and the reason names the range.
- `client-join-command` reprints a stored join command; when the panel no longer has one it explains that the only
  way to get a working command is a new keypair, which stops the client currently using the old one, and refuses
  to do it without `--yes`.
- The Forwards create and edit pages carry visible Save and Cancel buttons again. Pelican turns every action into
  an icon-only button when the admin has that button style on, and Filament's own Create, Save and Cancel form
  actions ship without an icon, so they rendered as blank buttons: the form could only be submitted by pressing
  enter in a text box. Each now carries an icon.
- Those same Create, Create another, Save and Cancel actions always keep their text label too, even when the
  admin has the icon-only button style on panel-wide. They are the only way in or out of the form, so they stay
  labelled buttons rather than joining the compact icon style the rest of the panel uses.
- The forward form no longer sits in the left half of the page. A resource form is a two column grid, and a single
  section left at the default span filled one column; the sections now span the full width and the fields are laid
  out in three sections.
- The Status page refreshes itself every 30 seconds and shows when it last did.
- The dashboard banner re-checks itself every 60 seconds, so it appears and clears without a page reload.
- The banner and the Status page now report a tunnel client that is not connected: a proxied node whose client has
  never handshaked, has not handshaked inside the "warn after" window, or is no longer known to the VPS. This is the
  most common real failure and it was invisible before, because the VPS keeps answering and keeps accepting rules
  while that node's ports lead nowhere. The reconcile writes down the peer handshake ages it already reads, so the
  dashboard costs one query and no HTTP call.
- The Settings -> Plugins modal explains what the plugin does, shows the current state at a glance, links to the
  Setup, Status and Forwards pages and to the documentation, and lists the five most common problems with the first
  command to run for each. Rotating the API token now asks for confirmation.

### Agent (VPS)

- `autoproxy-agent` with four subcommands: `setup` (provision the box), `run` (the service, and the default when
  no subcommand is given), `show-code` (re-print the VPS code for the plugin), and `uninstall`.
- Dynamic WireGuard peers. `/etc/wireguard/wg0.conf` is written once at setup with no peers in it and is never
  rewritten; peers are added and removed live with `wg set` and re-applied from `peers.json` on start. Two writers
  on one config file is how peers silently vanish after a reboot.
- Join codes. Creating a peer mints its keypair on the VPS and returns the private key exactly once, inside one
  unpadded base64url token the admin pastes into the client installer. Nothing stores it; a lost code is replaced
  by a rotate, not by reading anything back.
- Two kinds of forward target. `target_peer` sends traffic straight to that peer's tunnel address with no
  masquerade, so the game server sees the player's own IP (real-IP mode). `target_ip` plus `via_peer` sends it to
  an address on that peer's LAN, masqueraded, because that host has no route back into the tunnel (site mode). The
  agent enforces the asymmetry: `target_peer` must name a real-IP peer, `via_peer` must name a site peer, and a
  `target_ip` must be RFC1918 and inside the ranges its peer covers.
- HTTPS control API on port 7443: TLS 1.3 minimum, a self-signed certificate carrying an IP SAN, a 32-byte bearer
  token compared in constant time, and a per-address lockout — five wrong tokens and that address gets HTTP 429
  with `Retry-After` for fifteen minutes. There is deliberately no unauthenticated health endpoint.
- `POST /v1/token/rotate` mints a new token, rewrites it into `/etc/autoproxy/agent.env` (0600) and returns it
  once. The old token stops working immediately, and the new one is not switched in unless it was persisted first.
- Rules survive a restart and a peer change: the applied set is stored, re-resolved on start, and rules naming a
  peer that no longer exists are dropped with a recorded reason rather than taking the whole set down with them.
- Deliberate deviation: a stock `/etc/nftables.conf` is replaced without `--yes` when it still matches the dpkg
  conffile checksum, because every fresh Debian and Ubuntu VPS ships one and refusing would break the advertised
  one-command install. A file the admin has touched is never replaced without `--yes`, and the previous file is
  always kept at `/etc/nftables.conf.pre-autoproxy`.
- Deliberate deviation: the agent does not change `rp_filter` on the VPS. The VPS forwards traffic rather than
  receiving return traffic by another path, so loosening reverse-path filtering there would weaken the box for
  nothing. Only the client sets `rp_filter = 2`, and only on its own `autoproxy0` interface.
- Fixed a routing loop that could leave a site peer stuck on "waiting for the first handshake" forever with no
  error anywhere. Peer routes (one `ip route replace <cidr> dev wg0` per `AllowedIP`) used to land in the main
  routing table; when a site peer's own `lan_cidrs` happened to cover the address WireGuard was actually dialing
  for that peer's transport (its live public endpoint), that route won over the interface route to the real
  endpoint, so the VPS's own encrypted handshake and keepalive packets got routed back into the tunnel they were
  trying to open. Found live: `wg show` showed bytes sent and received on the VPS with no `latest handshake`, ever.
  Peer routes now go into a dedicated routing table (`201`) instead, the WireGuard interface's own transport
  traffic is tagged with a fixed fwmark (`0x2b`), and a `not fwmark 0x2b lookup 201` ip rule (priority `90`) sends
  everything else through that table first — the same construction `wg-quick` uses for `AllowedIPs 0.0.0.0/0`. An
  agent upgraded in place migrates any route it previously left in the main table automatically, on the next
  restart. `setup` and `uninstall` install and remove the rule cleanly; see
  [docs/dev/decisions.md](docs/dev/decisions.md) for what was rejected.
- The fwmark is written into `/etc/wireguard/wg0.conf` as a plain `FwMark = 0x2b` key, not only applied by the
  agent. After a reboot nothing but `wg-quick` reads that file, so an agent-only mark would leave a window on
  every boot in which the interface is up and unmarked.
- A tunnel client's LAN range that contains the VPS's own address is now refused when the client is created,
  with a message naming both the range and the address. The VPS has to reach that address directly, and the
  refusal is what turns an unexplainable half-working VPS into a sentence the admin can act on. The check is
  skipped rather than guessed at when the agent cannot parse `AUTOPROXY_PUBLIC_IP`. The existing tunnel-subnet
  overlap refusal now explains itself the same way.
- The container end-to-end gate (`scripts/vps-test/run.sh`) grew from 68 checks to 79: the fwmark in `wg0.conf`
  and on the live interface, the ip rule after setup and again after a restart, every peer route present in table
  `201` and absent from the main table, the refusal of an overlapping LAN range, and the rule and table both gone
  after uninstall.
- Scope decision for the first release: the OS gate (`setup`, and both installers) now accepts Debian 12 and 13
  only. Ubuntu was implemented and previously accepted, but is dropped from what this release promises until it
  has had its own live run; on Ubuntu the refusal now says explicitly that support is tracked for a later release,
  rather than folding into the generic "unsupported OS" message. See
  [docs/dev/decisions.md](docs/dev/decisions.md).

### Client (node host)

- `autoproxy-client` with subcommands `install`, `up`, `run`, `status`, `down`, `uninstall` and `version`, for
  both a systemd install and a Docker Compose flavour.
- Real-IP mode: the nftables ruleset, connection mark and policy-routing rule that let a game server on this host
  see the player's own source address instead of the VPS's.
- Fixed, found in the first live end-to-end test: in real mode the game server saw a 172.x bridge address instead of
  the player's real IP, on every Wings host — the exact thing real mode exists to prevent. The client's masquerade
  rule was written in both modes, on the assumption that real-mode traffic is delivered locally and never reaches
  postrouting; on a Wings host the game server runs in a container, so the packet is forwarded across a bridge and
  does reach it. Excluding bridges by name did not help either, because Wings names its bridge `pelican0` and the
  rule only skipped `docker*` and `br-*`. The masquerade rule is now written only in site mode, and matches on
  destination — the LAN ranges that peer actually covers — rather than on interface names, so renaming a bridge
  cannot break it again. In real mode there is no masquerade rule at all. Anyone already running a client should
  reinstall or restart it to pick up the corrected ruleset.
- Fixed: `systemctl restart autoproxy-client` dropped traffic for roughly 15-30 seconds, long enough to disconnect
  players — the opposite of what restarting this service is documented to do. Re-syncing an interface that was
  already up handed WireGuard the tunnel's private key again, and a private key rekeys the tunnel even when it is
  byte-for-byte the key already in place, because the session secrets are derived from it. Nothing inbound can
  trigger the new handshake, so the tunnel stayed dead until the 25-second keepalive fired. `up` now compares the live
  tunnel against its config — private key, peer, endpoint, keepalive — and skips the re-sync entirely when all four
  already match, so a restart leaves the running session alone. Anything genuinely different still gets the full
  sync, with a log line saying the tunnel is being rekeyed, so a changed endpoint or keepalive still takes effect.
  Verified live on both paths: a full service restart during a once-per-second UDP exchange lost 0 of 15 packets and
  left the handshake timestamp unchanged. Anyone on an older client should update to get restarts that keep players
  connected.
- `autoproxy-client status` now prints a readable transfer line (`transfer:     3156 bytes received, 4668 bytes
  sent`). It previously printed the raw `wg show` output, which put the peer's public key on the `transfer:` line
  and no byte counts at all. The `transfer` field of `status --json` carries the same string; anything parsing the
  old value needs adjusting.
- `--host-ip` for allocations bound to one specific LAN address rather than the host's default, which adds a DNAT
  fallback so traffic arriving on the tunnel address still lands on the right address.
- Installs with `--no-install-recommends`, so a node host does not gain a pile of unrelated packages.
- The client's WireGuard interface is `autoproxy0`, kept distinct from the VPS's `wg0` on purpose: the two ends
  are configured separately, and confusing them is the easiest way to spend an evening debugging the wrong
  machine.

### Plugin (Pelican panel) — 0.2.0

- Renamed throughout from the prototype: plugin id `autoproxy`, "Pelican Auto Proxy", namespace
  `Arrowtje\AutoProxy`, tables `autoproxy_*`, command `autoproxy:sync`, views `autoproxy::`. Fresh install only;
  there is no upgrade path from the prototype's tables.
- New Setup page (root admins only): connect the VPS by pasting the code, switch nodes on and off, pick real-IP or
  site mode per node, and copy a generated join command (systemd or Docker Compose) per node.
- Site-mode tunnel clients can be created from the Setup page for a machine that is not a Pelican node — the way
  traffic reaches anything else on that LAN. It asks for the LAN ranges that client covers, which is what the VPS
  routes down the tunnel.
- Both peer pickers offer only what the VPS will accept: real-IP clients for a direct target, site clients for
  "reached through". A LAN target is checked against the ranges the chosen client covers before it is saved.
- Panel talks to the VPS over HTTPS, verifying the agent's own certificate (stored 0600 from the VPS code) with an
  optional public-key pin. Named errors for a wrong or damaged certificate, nothing listening, a rejected token, a
  lockout (with the wait in plain words) and a refused rule set, listed rule by rule.
- Status page shows the VPS's tunnel clients with handshake ages and which node each serves, and lists every
  allocation that asked to be published but is not, with the first thing to fix.
- The keyword alias rewrite is scoped to proxied nodes only. Allocations on nodes that are not proxied are left
  entirely alone, alias included, so players are never shown an address nothing answers on.
- Join codes are stored encrypted and dropped after the peer's first handshake.
- New `autoproxy:setup` command: the whole Setup page from the command line, for unattended installs, scripted
  rebuilds and headless panels. Actions `store-code`, `test`, `nodes`, `enable-node`, `disable-node` and
  `join-command`. It calls the same services the page calls, so the two cannot drift apart. `store-code` prefers
  `--code-file` over `--code`, because an argument is visible in `ps` and in shell history. The Setup page remains
  the documented normal path.
- Manual forwards for services that are not Pelican allocations, and a dashboard banner covering sync failures,
  port conflicts and unproxied nodes.
- `plugin.json` gains `update_url` and `panel_version` `^1.0.0-beta38`. The release now publishes the matching
  `update.json` manifest, in the shape the panel's own `Plugin::getUpdateData()` reads, so the panel's Update
  button appears when a newer version is tagged.

### Docs, CI and release tooling

- Full documentation set under `docs/` for strangers running Pelican Panel at home: quick start, four supported
  layouts, VPS/client install references, plugin usage, security threat model, troubleshooting by symptom,
  updating, uninstalling, FAQ, Coolify, and migrating from a prior tunnel tool.
- `docs/dev/` architecture, testing and release references for contributors, replacing the internal planning
  document now that its content has a permanent home.
- Corrected, after watching it happen: re-importing the plugin zip makes the panel show it as **not installed**
  again, and you press **Install**, not **Enable**. The panel keeps a plugin's install state inside the plugin's own
  folder, which a new build overwrites. Nothing is lost — settings, node settings and manual forwards live in the
  database, and install only runs migrations that have not run before.
- CI: Go vet/test, golden nftables checks, shellcheck, `php -l` and plugin tests, and the infra sweep, all on every
  push and pull request.
- `scripts/contract-test/` runs the plugin's own HTTP client against the real compiled agent in a throwaway
  container, and the same suite against `test/fake-agent.py`, so the test double cannot drift away from the API it
  stands in for. It also feeds the agent's own join codes to the real client decoder.
- Release workflow: builds the agent binary, the client archive, the plugin zip, both installers, `update.json`
  and `SHA256SUMS`, and publishes them to a GitHub release on a version tag. Installers verify checksums and
  refuse to install on a mismatch.
- `scripts/release-checklist.sh` for the manual pre-release checks that cannot be fully automated.
- CI: the `go` job now runs on the bare `ubuntu-latest` runner with `actions/setup-go@v5` instead of inside a
  `golang:1.25-alpine` container, and `nft-golden` runs natively on `ubuntu-latest` with `sudo apt-get install
  nftables` instead of a `--cap-add NET_ADMIN` container — checkout inside an Alpine container was failing on the
  hosted runners. `actionlint` now runs via the `reviewdog/action-actionlint` action and `shell` installs
  `shellcheck` directly, replacing anonymous `docker run` pulls from Docker Hub that hit rate limits on shared
  runners.
