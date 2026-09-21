# Pelican Auto Proxy (panel plugin)

Publishes a game port on a VPS you rent, so players connect to the VPS instead of
to your home connection. Your own IP stays private, and the game server can still
see each player's real IP address.

You open a port by giving a Pelican allocation the alias `proxy`. Within a minute
the port is open on the VPS and the panel shows players a connectable address.
Remove the alias and the port closes within a minute. That alias is the only
control; nothing else in Pelican changes.

This plugin is one of three parts:

| Part | Runs on | What it does |
|---|---|---|
| `autoproxy-agent` | your VPS | opens ports and forwards them into the tunnel |
| `autoproxy-client` | each machine that runs Wings | the tunnel end, one per machine |
| this plugin | your Pelican panel | tells the VPS what to forward, and generates every command |

Forwarding happens in the Linux kernel (nftables DNAT over WireGuard), not in a
per-port helper process, so a hundred open ports cost about as much memory as one.

## What you need

- A Pelican panel, version 1.0.0-beta38 or newer, with its scheduler (`schedule:run`)
  and queue worker running. Without the scheduler nothing is ever pushed to the VPS.
- Root on a small VPS with a public IPv4 address (Debian 12/13 or Ubuntu 22.04/24.04).
- Root on each machine that runs Wings, if you want real player IPs there.

## 1. Install the plugin

1. **Admin → Plugins → Import** and upload `autoproxy-<version>.zip`.
2. Press **Install** on the Pelican Auto Proxy row.

Install is a queued job: it creates the plugin's four database tables and enables
the plugin. If the row stays on "installing", the panel's queue worker is down —
that is a panel problem, not a plugin one.

Everything after this happens on **Auto Proxy → Setup** in the sidebar, which only
root admins can see.

## 2. Setup, step by step

### Step 1 — Connect the VPS

The page shows one command to run on your VPS as root. It installs WireGuard,
nftables and the agent, and prints a **VPS code**: one long line that carries the
VPS's address, its API port, its certificate and this panel's access token.

Paste that code into the box and press **Connect**. The panel stores it (token
encrypted in the database, certificate written to `storage/app/autoproxy/agent.pem`
with 0600 permissions) and immediately asks the VPS for its status, so you know
within a second whether it worked.

> _Screenshot placeholder: Setup step 1, VPS connected, showing the agent version._

The panel verifies that certificate on every request, and the certificate names
the VPS's IP address, so a wrong or intercepted VPS is refused rather than trusted.
If the VPS code also carried a public-key fingerprint, the panel pins that too.

### Step 2 — Nodes

Every Pelican node is listed. For each one you choose:

- **Proxied** on or off. Switching it on in real-IP mode creates a tunnel client
  for that node on the VPS and shows you the **join command** to run on that
  node's machine — as a system service, or as a Docker Compose snippet.
- **Mode**:
  - *Tunnel client on this node's machine (real player IPs)* — the default, and the
    reason to use this project. The client runs next to Wings, so the game server
    sees each player's own address and IP bans work.
  - *Via another client + LAN IP (shared IP)* — for a node you cannot install the
    client on. Traffic reaches it across the LAN from another machine's client, so
    the game server sees one address for everybody.
- **Status**: no client yet → waiting for the first handshake → connected, with the
  age of the last handshake. Anything older than three minutes reads as stale.

> _Screenshot placeholder: Setup step 2, one node connected and one waiting for its
> first handshake, with the join command open on the Docker tab._

The join command is shown until that client connects for the first time, then the
panel deletes its copy: it contains the client's private key. Lost it before
using it? **Regenerate join code** issues a new one and invalidates the old.

Switching a node off asks for confirmation, removes its client from the VPS and
closes its ports immediately.

Below the node list you can add **tunnel clients on other machines**: a client on a
LAN machine that forwards to other addresses on that LAN. That is what "via another
client" needs, and what manual forwards to non-Pelican services use.

### Step 3 — Public address

- **Public hostname** (optional): what players are shown. Point the name at the VPS
  IP first. Leave it empty and the VPS IP is used instead.
- **Alias keywords** (default `proxy,public`): the aliases that make an allocation
  public. The match is exact, so `proxy` and `Proxy` count and `proxy server` does not.
- **Warn after**: how many minutes without a successful sync before the dashboard
  warns you.

## 3. Make a port public

**Admin → Nodes → (node) → Allocations**, type `proxy` in the **Alias** column of the
port you want open. Within a minute, if the node is proxied and a server is assigned
to that allocation (a stopped server still counts):

- the port is open on the VPS and forwarded to that server;
- the alias is rewritten to your public address, so the panel shows players a
  connectable address;
- the allocation appears under **Auto Proxy → Forwards** in the read-only list.

An allocation with the alias but **no server assigned** gets the alias rewrite but
nothing is forwarded — the port stays closed. An allocation on a node that is **not
proxied** is left alone entirely, alias included, so players are never shown an
address that nothing answers on. Both cases are listed on the Status page with the
reason, rather than silently doing nothing.

Clearing the alias closes the port within a minute. There is no confirmation step:
the alias is the only source of truth.

## Setting up without a browser

Everything on the Setup page is also available as a panel command, for unattended installs, scripted
rebuilds, and panels where nobody opens the admin UI. It calls the same code the page does, so the two
cannot behave differently. Run these from the panel's own directory, as the panel's user:

```
php artisan autoproxy:setup store-code --code-file=/path/to/vps-code.txt
php artisan autoproxy:setup test
php artisan autoproxy:setup nodes
php artisan autoproxy:setup enable-node <id-or-name> --mode=real
php artisan autoproxy:setup enable-node <id-or-name> --mode=site --via-peer=<peer-id> --lan-ip=10.0.0.10
php artisan autoproxy:setup disable-node <id-or-name>
php artisan autoproxy:setup join-command <id-or-name>
```

- **store-code** stores the VPS code and immediately tests the connection. Prefer `--code-file` over
  `--code=`: an argument is visible in `ps` and in your shell history.
- **test** asks the VPS for its status.
- **nodes** prints a table of your nodes: id, name, whether it is proxied, its mode, its peer id, and
  whether a join code is stored.
- **enable-node** turns a node on. Real mode creates the tunnel client on the VPS and stores the join
  code. Site mode needs `--via-peer` (the client that reaches it) and `--lan-ip`.
- **disable-node** removes that node's client from the VPS and closes its ports.
- **join-command** prints the install command to run on that node's machine. That line contains the
  client's private key, so treat it like a password — the command says so too.

Tunnel clients that are not nodes, and manual forwards, have commands too:

```
php artisan autoproxy:setup clients
php artisan autoproxy:setup add-client --name="office switch" --lan-cidrs=10.0.0.0/24,192.168.4.0/24
php artisan autoproxy:setup client-join-command <peer-id> [--yes]
php artisan autoproxy:setup remove-client <peer-id>

php artisan autoproxy:setup forwards
php artisan autoproxy:setup add-forward --name="panel" --proto=tcp --public-port=8080 \
    --target-ip=10.0.0.10 --via=<peer-id> [--target-port=80] [--notes="..."] [--disabled]
php artisan autoproxy:setup remove-forward <id>
```

- **clients** lists every tunnel client the VPS knows: peer id, name, mode, tunnel IP, LAN ranges, last
  handshake, the node that owns it if any, and whether a join code is still stored here.
- **add-client** creates a site-mode client for a LAN and prints its peer id, tunnel IP and join command.
  If the VPS refuses the LAN ranges — it rejects a range covering its own public address or the tunnel
  subnet — its own sentence is printed word for word, because it names the range and the reason.
- **client-join-command** reprints the join command while the panel still holds the code. Once it is gone
  the only way to get a working one is a new keypair, which kicks off the client currently using the old
  one; that path warns and needs `--yes`.
- **remove-client** deletes the client, closes its ports, and switches off any node reached through it.
  A client that belongs to a node is refused — use `disable-node` for those.
- **add-forward** creates a manual forward to a LAN address through a site client and pushes it straight
  away. It applies the same rules as the Forwards form, in the same words: RFC1918 target only, inside
  the ranges that client covers, ports 1–65535, a range end at or above the start, and no target port on
  a range.
- **remove-forward** deletes a manual forward by the id `forwards` lists, and pushes.

The Setup page remains the normal path, and the one this page walks through. The other command the
plugin registers is `autoproxy:sync`, which the panel's scheduler runs every minute.

## What each page does

- **Setup** — the three steps above. The only page that changes anything.
- **Status** — what the VPS says right now (version, applied forwards, tunnel
  clients with handshake ages and which node each serves), what the last sync did,
  and every allocation that asked to be published but is not, with the first thing
  to fix.
- **Forwards** — forwards for things that are not Pelican allocations: the panel
  itself, SFTP, a NAS. Each one points either at a tunnel client directly, or at a
  LAN address reached through a client. Below them, the read-only list of public
  allocations.
- **The dashboard banner** — appears for root admins only, when something is wrong:
  no VPS connected, no node proxied, the scheduler never ran, the VPS has not
  answered, the VPS refused the last push, or forwards are being withheld. No
  banner means the last minute's sync succeeded.

## Updating

The plugin's `plugin.json` carries an `update_url`, so the panel checks for new
versions itself and offers an **Update** button on the Plugins page when one exists.
That check needs a working queue worker, like any plugin install.

To update by hand instead: build or download the new zip and import it the same way
as the first time. The plugin id is unchanged, so the panel replaces the folder.

After a re-import the plugin shows as **not installed** on the Plugins page, and you
press **Install** again. That is expected, not a failure: the panel keeps a plugin's
install state inside the plugin's own folder, and importing a new build overwrites
that file. Nothing is lost by pressing Install — your settings, node settings and
manual forwards live in the database, and install only runs migrations that have not
run before.

## Uninstalling

Uninstall from the Plugins page. That drops the plugin's four tables, so the stored
token, node settings and manual forwards are gone. It does **not** touch the VPS:
the agent keeps applying the last set of forwards it was given. Remove the agent on
the VPS (`autoproxy-agent uninstall`) and the clients on each node machine
(`autoproxy-client uninstall`) to close everything down.

A dead panel does not close ports. That is deliberate: a panel restart must not
disconnect players.

## API assumptions

This plugin is written against the VPS agent's v1 HTTPS API. Where the contract was
not fixed at the time of writing, the plugin assumes the following. If the agent
disagrees, its own rejection message is shown on screen.

- **Base URL** `https://<vps ip>:<api port>`, bearer token, JSON.
- `GET /v1/status` returns `{version, uptime_s, wg{}, applied{tcp,udp,rules},
  applied_at, last_error, peers{total,healthy}}`. Extra keys are ignored.
- `PUT /v1/rules {"rules":[…]}` fully replaces the set. Each rule carries exactly
  one target form — `target_peer`, or `target_ip` plus `via_peer` — and keys that do
  not apply are **omitted, not sent as null**.
- `GET /v1/peers` may return a bare list or `{"peers": […]}`; both are accepted.
  A peer's `handshake_age_s` is `null` until it has ever connected, which is what
  "waiting for the first handshake" is read from.
- `POST /v1/peers` returns `201 {peer:{id,…}, join_code}`, and the join code is
  returned exactly once. The plugin stores it encrypted until the peer's first
  handshake, then deletes it.
- `422` bodies look like `{"rejected":[{"id","reason"}]}` and mean nothing was
  applied. `429` means a per-IP lockout after repeated bad tokens; the plugin shows
  the `Retry-After` value.
- `POST /v1/token/rotate` returns `{token}`, and the old token stops working at
  once, so the plugin stores the new one before telling you it worked.
- The VPS code is base64url JSON `{v:1, endpoint_ip, api_port, api_ca_pem (base64),
  api_spki_sha256, token, wg_pubkey, wg_port, tunnel_subnet, vps_tunnel_ip, version}`.
  Only `v`, `endpoint_ip`, `api_port`, `token` and `api_ca_pem` are required; the rest
  is stored for display.
- Peer ids are treated as opaque strings.
- A site-mode forward may be routed through any peer the VPS lists. If your agent
  requires that peer to be in site mode, it will say so and the message is shown.

## Limitations

- **Up to a minute of delay.** Alias changes are picked up by the once-a-minute
  reconcile. Changes made inside the plugin push immediately.
- **The reconcile is the truth, not events.** Pelican deletes allocations in bulk
  without firing model events, so the whole picture is rebuilt every minute.
- **Real player IPs need a client on that node's own machine.** Anything reached
  across a LAN shares one address.
- **Conflicts withhold both sides** rather than picking a winner, so a mistake
  closes a port instead of misrouting one.
- **IPv4 only**, and LAN targets must be private (10.x, 172.16–31.x, 192.168.x).
- **Root admins only.** Any other admin sees no Auto Proxy page, resource or banner.
- **The copy buttons need a secure context.** On a panel served over plain HTTP the
  browser blocks the clipboard API; the buttons fall back to an older copy method,
  and failing that they show the text for you to copy by hand.
- **One VPS.** No failover, and no IPv6, in this version.

## Developing against a fake VPS

No VPS needed. From a checkout of the project:

```bash
python3 test/fake-agent.py --tls --host 127.0.0.1 --port 17443 --token devtoken
```

It generates a self-signed certificate with an IP SAN, prints the PEM and the
public-key pin, and serves the whole v1 API: status, rules, peers, join codes,
token rotation and the per-IP lockout after five bad tokens. Build a VPS code from
its output and paste it into Setup.

The pure logic has a standalone test, no Laravel and no database required:

```bash
docker run --rm -v "$PWD":/w -w /w php:8.4-cli php test/rules-test.php
```

Build the importable zip with `scripts/make-plugin-zip.sh`; it refuses to ship a
manifest missing required fields.
