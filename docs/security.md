# Security

Plain-language threat model. If you only read one section, read "recommendations" at the end.

## What the VPS can see

The VPS is a relay, not a database. It sees:

- The forwarding rules the plugin sends it (public port → target), and applies them.
- The game traffic that flows through those forwarded ports — the same traffic a plain port-forward on your own
  router would carry, no more.
- WireGuard tunnel metadata (which peers are connected, handshake times, byte counters).

It never receives your panel's database, server files, player accounts, or anything else Pelican stores. Compromise
of the VPS lets an attacker see and redirect the traffic passing through it, and read the token and certificate
stored on it — it does not, by itself, hand over your panel or your node hosts.

## What a leaked VPS code allows

The VPS code contains the API token, the pinned certificate, and the WireGuard public key and endpoint. Anyone who
has it can:

- Call the VPS's API with full rights: read and replace the entire forwarding rule set, add or remove peers.
- Ask a tunnel client to install a different **official** release of the client, from 0.3.0 on (see "Remote client
  updates" below for exactly what that can and cannot do).
- They cannot join the tunnel with it alone — joining requires a peer key that only a join code carries.

**To rotate:** open **Admin → Plugins**, press the settings button on the Auto Proxy row, and use **Rotate API token**
under VPS API. The panel asks the VPS for a new token and stores it before telling you it worked — the token itself is
never shown, because the panel is the only thing that needs it. The old token stops working the moment the VPS
answers, so any copy of it elsewhere (your notes, a second panel) is dead; nothing that was already forwarding
stops, since the rule set on the VPS does not change.

## What a leaked join code allows

A join code is scoped to one peer: it lets whoever has it bring up a tunnel as that node and receive the traffic the
VPS forwards to it. It does not carry the API token, so it cannot be used to change forwarding rules or add other
peers.

A join code does **not** expire on its own, and nothing stops it being used twice: it carries that peer's private
key, and the VPS accepts any client holding that key. Treat it like a password, not a one-time code. Two things
kill one: **regenerating the join code** for that node (the plugin's rotate — it mints a new keypair, so the old
code stops working the moment the VPS answers, and the machine already using it drops off until you re-run the
installer with the new code), and **deleting the peer** from the Setup page, which drops its tunnel and removes its
routes immediately. The panel keeps its own copy of a join code encrypted and deletes it as soon as that peer's
first handshake arrives.

## Remote client updates (0.3.0 and later)

The Status page can update a node's tunnel client with one click. It is **off for every client** until an admin turns
on "Allow remote updates" for that client, and a node's owner can refuse it on the machine itself no matter what the
panel says.

How it works, and why each step is there:

1. The admin presses **Update**. The panel tells the VPS agent, with its API token, one thing: the release number that
   client should run (`PUT /v1/peers/{id}/client`, `{"desired_version": "0.3.1"}`). Only a plain release number is
   accepted, `X.Y.Z`; no URL, no file, no command.
2. Every two minutes each client checks in with the VPS over its WireGuard tunnel and hears that number back.
3. The client decides for itself. It acts only if remote updates are on for that machine
   (`autoproxy-client remote-updates`, default on), the number is **newer** than what it runs (never a downgrade,
   never a reinstall), and it has not already tried this exact request.
4. It downloads `autoproxy-client.tar.gz` and `SHA256SUMS` for that tag from this project's GitHub release page — the
   address is built into the client, the panel and the VPS cannot change it — and refuses to install anything whose
   checksum does not match, exactly as the installer does. Then it replaces its script and unit and restarts itself
   without touching the tunnel's keys or config.
5. It reports "updated" or "failed" with its reason, which the Status and Setup pages show.

**What a leaked API token can now do**, on top of what it could before: make a client that has remote updates on
install a *newer official release* of the client, published on this project's GitHub release page and verified
against that release's checksums. It cannot make a client run code the attacker wrote, download from anywhere else,
downgrade to an older release with a known bug, or reinstall the same one. The worst it can do is move a node to a
newer version of software the node's owner already chose to run, at a moment of the attacker's choosing — which is
why remote updates are **off by default**, per client, and why the node keeps the final say. (The API token could
already do far worse — rewrite every forward and remove every peer — so this does not make a leaked token much more
dangerous. It is still a new capability, and it is listed here so nobody has to discover it.)

The GitHub release page itself is trusted, as it already is by every install and every manual update. If you do not
want a node to trust it without you watching, keep remote updates off for that node.

**Turning it off:**

- For one client, in the panel: Auto Proxy → Status → Tunnel client updates → press **Remote updates allowed** to
  switch it off. A pending request is withdrawn on the VPS at the same time; if the VPS cannot be reached, the switch
  stays on and says why, rather than pretending.
- On the node itself, regardless of the panel or anyone holding the API token:

  ```bash
  sudo autoproxy-client remote-updates off
  ```

  The client then refuses every request and reports the refusal to the panel. `remote-updates on` allows them again;
  `remote-updates status` prints the current setting. It is stored in `/etc/autoproxy/remote-updates`.
- The Docker flavour never updates itself: a container cannot replace its own image. Pull the new image instead.

## The tunnel check-in endpoint (0.3.0 and later)

Clients report their version to the agent on the VPS's **tunnel** address (`10.66.66.1`, the API port), inside
WireGuard, without the API token. That is the one route without a token, and it is not reachable from the internet:

- The agent answers it only for a connection addressed to its tunnel address **and** coming from a tunnel address that
  belongs to a peer. WireGuard delivers a packet from `10.66.66.5` only if it was sealed with the key of the peer that
  owns `10.66.66.5`, so the source address identifies the client. A request from anywhere else, including one that
  uses the same path on the public address, is treated like any other request without the token: 401, and it counts
  towards the lockout.
- The agent's own nftables table drops any packet addressed to the tunnel address that did not arrive on `wg0` (chain
  `tunnel_guard` in `inet autoproxy_rules`). Nothing on the internet routes `10.66.66.1` to your VPS anyway; this
  also covers a machine on the VPS's own network segment trying to hand-route a packet to it.
- What a check-in can do: set that one client's own reported version, flavour and update result, and read back the
  release number requested for it. It cannot change a forward, a peer or anything about another client.

## Public API hardening

The API is HTTPS-only, token-protected, and rejects unknown routes the same as known ones without the token — there
is no unauthenticated endpoint to probe from the internet (the check-in route above answers only through the tunnel). Repeated failed attempts from one address trigger a lockout (a short window
of rejected requests, logged), so a guessed or brute-forced token is not a fast attack even before you rotate it.
The certificate is self-signed and pinned by the plugin at setup time, so a network position between panel and VPS
cannot swap in a different certificate later without the plugin noticing.

## Why the tunnel client needs root

Bringing up a WireGuard interface, adding routes, and writing the firewall rules that make real player IPs work all
require kernel network administration rights on the node host — there is no unprivileged way to do this on Linux.
The installer does not ask for more than that: it does not touch SSH or user accounts. What it does touch on the
node host is `/etc/autoproxy/client.json`, `/etc/wireguard/autoproxy0.conf`, `/etc/sysctl.d/90-autoproxy.conf`,
`/usr/local/bin/autoproxy-client`, its own systemd unit, the `autoproxy0` interface with one `ip rule` and one
routing table, its own `inet autoproxy_client` nftables table, and two accept rules in Docker's `DOCKER-USER` chain
if that chain exists; from 0.3.0 also `/etc/autoproxy/remote-updates` (only if you run `remote-updates on|off`) and
runtime state under `/run/autoproxy-client`. `autoproxy-client uninstall` lists and removes exactly that set.

## What an admin can expose

Any root admin who can reach the Setup or Forwards page can forward any private target on the LAN a client has
access to, and can therefore expose any service, including ones unrelated to Pelican, through the manual Forwards
page. This mirrors the trust already implied by root-admin access to the panel; the plugin adds no separate
authorization layer beyond "root admin or nothing," matching how the rest of Pelican's admin area works.

A LAN range is not accepted unconditionally, though. The VPS refuses a range that overlaps the tunnel subnet, one
that overlaps another client's ranges, one that is not a private (RFC1918) range, and one that contains the VPS's
own address. The last two matter here: a public range routed into the tunnel would let one client claim traffic
for addresses it has no business with, and a range covering the VPS's own address would cut the VPS off from the
network it has to reach directly. No client is ever given `0.0.0.0/0`, which would let a single compromised client
claim every address on the tunnel, including the other clients' game servers.

## Three things the VPS installer does that may surprise you

All three are deliberate, and all three are the kind of thing worth knowing before you run an installer as root.

- **It replaces a stock `/etc/nftables.conf` without asking.** The `nftables` package on Debian and Ubuntu ships a
  skeleton firewall file, and setup needs to own that file. It asks dpkg for the checksum of the file the package
  installed: if yours is still byte-for-byte that default, setup replaces it without needing `--yes`, because
  otherwise the one-command install would stop on every fresh VPS. If you have edited that file at all, it is
  treated as your firewall — setup refuses to write it and asks you to re-run with `--yes`, and if dpkg cannot be
  asked or its answer cannot be parsed, it refuses as well. Whatever was there is copied to
  `/etc/nftables.conf.pre-autoproxy` before it is replaced, and `autoproxy-agent uninstall` restores it.
- **It adds a routing rule of its own, at priority 90.** `ip rule show` will list
  `not from all fwmark 0x2b lookup 201`, and routing table `201` will hold one route per tunnel client range. The
  VPS's own tunnel packets carry that mark and so skip the table; everything else consults it first. Without this,
  a client's LAN range can shadow the path the VPS needs to reach that very client, and the tunnel silently never
  connects. Nothing else on the machine is affected: table `201` contains only tunnel client ranges, and
  `autoproxy-agent uninstall` removes both the rule and the table's contents.
- **It does not change `rp_filter` on the VPS.** Reverse-path filtering is left exactly as your provider's image set
  it, because the VPS does not need it loosened: every address a peer may send from is listed in that peer's
  `AllowedIPs` and is therefore routed back down `wg0`, so the strict check passes on its own. The node client is
  the one that sets `rp_filter=2`, and only on its own `autoproxy0` interface, where replies genuinely do leave by a
  different route than requests arrive by.

## Recommendations

- **Keep your normal SSH access to the VPS exactly as it was before installing.** The installer never changes
  `sshd_config` and never adds or removes keys — that is deliberate, so a provisioning script cannot lock you out of
  a box you still had a session on. If you want stronger SSH hardening, do it yourself, separately.
- **Restrict the API port to your panel's IP** once you know it (skip this if your panel's address changes often,
  or you're not sure yet — a wrong rule here can lock the plugin out, and the token/certificate already protect the
  port). On the VPS:

  ```bash
  sudo nft add table inet autoproxy_apiguard
  sudo nft add chain inet autoproxy_apiguard input { type filter hook input priority -10 \; policy accept \; }
  sudo nft add rule inet autoproxy_apiguard input tcp dport 7443 iifname != "wg0" ip saddr != <panel IP> drop
  ```

  `iifname != "wg0"` keeps the tunnel clients' version check-ins working (they reach the same port through the
  tunnel); without it the panel shows every node's client version as "not reported". This lives in its own table, separate from the ones the agent manages, so a plugin sync never removes it. To
  undo: `sudo nft delete table inet autoproxy_apiguard`.
- **Rotate the token after removing an admin who had panel access**, the same way you'd rotate any other shared
  credential that person could read.
- **Treat a join code like a one-time password**, not like a secret to store: generate it, paste it into the join
  command immediately, and let it expire otherwise.
