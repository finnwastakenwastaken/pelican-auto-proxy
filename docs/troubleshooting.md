# Troubleshooting

Organised by what you see, not by which component is actually at fault — you rarely know that in advance. Each
entry gives the likely cause, a command to confirm it, and the fix.

## No handshake

**Symptom:** `autoproxy-client status` (or the plugin's Status page) never shows a WireGuard handshake, or it stops
updating.

**Cause:** the node host cannot reach the VPS on its WireGuard port (default UDP 51820) — a network firewall
between them, a wrong endpoint in the join code (rare; means the VPS's public IP changed after the code was issued),
or the node host's own outbound UDP is blocked.

**Command:** on the node host (its tunnel interface is `autoproxy0`):

```bash
sudo wg show autoproxy0
```

and, on the VPS, where the interface is called `wg0`:

```bash
sudo wg show wg0
```

**Fix:** if `latest handshake` never appears, test raw UDP reachability from the node host to the VPS's WireGuard
port (a simple `nc -u` round trip, or the VPS's own logs for incoming attempts). If the VPS's public IP changed,
generate a fresh join code from the plugin rather than editing the old config by hand.

**If the handshake worked before and then stopped,** while every other connection between the node host and the
VPS still works, the network between them may be dropping that one UDP flow (this node's port to the VPS's
WireGuard port). A service restart does not help, because it keeps the same local port. Since 0.2.7 the client
moves to a new local port by itself after 3 minutes without a handshake, and again every 3 minutes while it stays
down; the log says `moved the tunnel to a new local port (old -> new)`. Since 0.3.1 it also moves when the path
drops nearly everything but the odd handshake, so the handshake looks fresh while no traffic arrives: two version
reports to the VPS in a row that time out trigger the same move (needs a 0.3.0 or later VPS agent). On an older client, do it by hand (it does
not rekey and does not change the config):

```bash
sudo wg set autoproxy0 listen-port 0
```

**If the VPS shows bytes sent and received but still no handshake,** the two sides are talking past each other
rather than not reaching each other. On the VPS, check that the tunnel's own packets are not being routed back
into the tunnel:

```bash
sudo wg show wg0 fwmark          # expect 0x2b
ip rule show | grep 201          # expect: 90: not from all fwmark 0x2b lookup 201
ip route show table 201          # every peer range belongs here
ip route show | grep wg0         # peer ranges must NOT appear here
```

If the mark or the rule is missing, `systemctl restart autoproxy-agent` puts both back; the agent re-applies them
on every start. A peer range showing up in the plain `ip route show` output is the thing to report: it means
something other than the agent added it, and if that range covers the address the client dials, the VPS answers
its own handshakes into the tunnel and the handshake can never complete. Versions of the agent before this check
existed did exactly that, and the only visible symptom was this one — no error, anywhere.

**If the VPS refuses to create the client at all,** saying a LAN range contains the VPS's own address: that is
deliberate. The VPS has to be able to reach its own network directly, so it will not accept a range that swallows
the address every client dials. Narrow the range, or list the individual hosts you need as `/32`s.

**A specific case that looks identical but is not a reachability problem:** for a site client, both sides can show
traffic counters moving (the VPS counts bytes "sent," the client counts bytes sent) while `latest handshake` never
appears on either end, and raw UDP reachability checks pass. If that client's LAN range happens to be broad enough
to cover the address the client itself dials the VPS from, an affected agent version could route the VPS's own
handshake and keepalive replies back into the tunnel instead of out to the client — a routing loop, not a blocked
path. `ip route get <the client's address>` on the VPS resolving to `dev wg0` instead of its normal public
interface confirms it. Updating the agent and restarting it fixes this without touching the client; see
[dev/decisions.md](dev/decisions.md) and [dev/architecture.md](dev/architecture.md) for the underlying cause.

## Port open but no traffic (Docker's FORWARD drop)

**Symptom:** the plugin says a rule is applied, `nft list ruleset` on the relevant host looks correct, the
WireGuard handshake is healthy, but nothing reaches the game server.

**Cause:** Docker sets the host's `FORWARD` chain policy to drop and expects firewall rules to go into its own
`DOCKER-USER` chain instead. Without an accept there for the tunnel interface, every forwarded packet is silently
dropped — no error anywhere, because as far as nftables logging is concerned, dropping to policy is normal.

**Command:**

```bash
sudo nft -a list chain ip filter DOCKER-USER
```

**Fix:** confirm two accept rules exist for the tunnel interface (`autoproxy0`) in both directions. The client
installer adds these automatically when Docker is present at install time; if Docker was installed **after** the
client, re-run `sudo autoproxy-client up` to have it re-check and re-insert them.

## UDP fails but TCP works

**Symptom:** a TCP-based check (or a TCP game) works through a forwarded port, but a UDP one (most game servers)
does not.

**Cause:** almost always the test method, not the forward: a UDP echo or test target running on the gateway host
itself (the VPS, or the client's own host) replies from a different local address than the one the request arrived
on, so the reply looks like an unrelated packet and gets dropped. This is a known trap even in local testing.

**Command:** on the node host (on the VPS, use `wg0` instead of `autoproxy0`):

```bash
tcpdump -ni autoproxy0 udp
```

**Fix:** test against a genuinely single-homed target — the actual game server on its normal host, not a quick
`socat`/`nc -u` listener started directly on the VPS or gateway. If the real game server also fails, check the
handshake and DOCKER-USER entries above first; a UDP-specific failure against the real target after those check out
usually means a provider is filtering UDP outright (see "cost and expectations" in [faq.md](faq.md)).

## Player IP is the tunnel IP, not real

**Symptom:** the game server logs show the tunnel's internal address (in the `10.66.66.0/24` range by default)
instead of the player's real IP.

**Cause:** either the node is in site mode (masquerading is expected there — see [layouts.md](layouts.md)), or it's
meant to be real mode but the tunnel client is running on a different machine than the one hosting the game server.

**Command:**

```bash
sudo autoproxy-client status   # run this ON the machine you think hosts the game server
```

**Fix:** real player IPs only happen when the client and the game server share a host. If they're on separate
machines, either move the client to the game server's host (real mode) and accept the LAN topology change, or
accept the shared IP that site mode gives you.

## "Node not proxied"

**Symptom:** the plugin's banner or Status page marks a node "not proxied" even though you've marked allocations
public on it.

**Cause:** the node's mode was never set on Setup step 2, or it's set to real mode but no client has ever completed
a join for that node.

**Command:** check Setup step 2's peer status column for that node — "never joined" versus a stale/healthy
handshake tells you which.

**Fix:** if never joined, run the join command shown there on the correct host. If it shows a mode but the wrong
one (e.g. real mode chosen for a node that's actually behind a LAN gateway), change it — this regenerates the join
command; the old one still installed on a host becomes redundant, not broken.

## Alias not recognised

**Symptom:** typing `proxy` (or a variant) into an allocation's alias does nothing.

**Cause:** the match against alias keywords is exact against the whole field, case-insensitive, trimmed of
surrounding spaces — but nothing else. `proxy server`, `my-proxy`, or a keyword with the alias-and-something-else
pattern does not match. Separately: an allocation with a recognised alias but **no server assigned** gets the alias
rewrite but nothing is forwarded.

**Command:** **Admin → Auto Proxy → Forwards** — an allocation that should be public but isn't will show up here as
"unassigned, not forwarded" if the server assignment is the problem, or won't show up at all if the alias itself
didn't match.

**Fix:** set the alias to exactly `proxy` or `public` (whichever your Setup step 3 configured), with nothing else in
the field, and confirm a server (stopped is fine) is assigned to that allocation.

## Panel banner says scheduler never ran

**Symptom:** the "panel scheduler has never run autoproxy:sync" banner never clears, no matter how long you wait.

**Cause:** Pelican's own scheduler (`php artisan schedule:run`, normally driven by a system cron entry or a queue
worker's own loop) isn't running at all — this isn't specific to Auto Proxy, but Auto Proxy depends on it entirely
for anything triggered by an alias edit.

**Command:**

```bash
docker exec <panel container> php artisan schedule:list
```

**Fix:** get the panel's scheduler running per Pelican's own setup instructions (a cron entry calling
`schedule:run` every minute is the usual fix). Once it runs once, the banner clears within a minute and stays clear
as long as the scheduler keeps running.

## Client version "not reported" or "unknown"

The Status page shows a client's version only when the client is 0.3.0 or newer **and** the VPS agent is 0.3.0 or
newer; "unknown" names the agent as the reason, "not reported" the client. When both are new enough and it still
says "not reported":

- The client checks in every two minutes, and only while its tunnel is up. Wait two minutes after a restart.
- If you updated the client before the agent, the client backed off for six hours after the old agent turned it away
  (it says so once in `journalctl -u autoproxy-client`). `sudo systemctl restart autoproxy-client` makes it try at
  once; the restart does not disconnect players.
- The check-in goes to the VPS's tunnel address on the API port. If you added the API-port restriction from
  [security.md](security.md#recommendations) without `iifname != "wg0"`, it drops the check-ins: add that match.
- An agent on a port other than 7443 with a client installed before 0.3.0: that client's config does not know the
  port. Add `Environment=AUTOPROXY_API_PORT=<port>` to the service (`systemctl edit autoproxy-client`), or re-run
  the installer with the node's join code.

## Client update failed

The Status page shows the client's own reason. Nothing was changed on the node in any of these cases; the client
keeps running the version it had.

- **CHECKSUM MISMATCH**: the download did not match the release's `SHA256SUMS`. Something between the node and GitHub
  altered it, or the release is broken. Do not retry blindly; report it.
- **could not download … / could not reach GitHub**: the node cannot reach `github.com` (outbound HTTPS blocked, DNS,
  a proxy), or that release does not exist. Check with `curl -I https://github.com` on the node.
- **remote updates are switched off on this node**: the node's owner refused them with
  `autoproxy-client remote-updates off`. They can allow them with `remote-updates on`, or update by hand.
- **this node runs the Docker image**: pull the new image instead (`docker compose pull && docker compose up -d`).
- **refusing to downgrade**: the requested release is older than what the node runs. The panel never asks for that;
  by hand, `autoproxy-client update --version vX.Y.Z --force` does it deliberately.

After fixing the cause, press **Update** again: every press is a new request, and a client never retries a failed
request on its own.

## Lockout / 429

**Symptom:** the plugin reports a 429 (too many requests) from the VPS, or "temporarily locked out."

**Cause:** the VPS's API rejected several requests with a bad token in a row (a wrong token pasted into the plugin,
or something else hitting the API with a guessed token) and is now refusing that source address for a short window.

**Fix:** wait out the lockout window (roughly 15 minutes) — it clears on its own, no VPS-side reset needed. If the
cause was a wrong token pasted into the plugin, fix that first so it doesn't immediately re-trigger the lockout.
Repeated unexpected lockouts from the panel's own address are worth investigating (a stale second copy of the
plugin's config pointed at the same VPS with an old token is a common cause).

## "The VPS certificate is missing" after updating the panel

**Symptom:** after updating or re-creating the panel's Docker container, every sync fails with "The VPS certificate
is missing from …/storage/app/autoproxy/agent.pem". Forwards that were already open keep working (the VPS keeps
them), but no change reaches the VPS.

**Cause:** the plugin keeps the VPS's certificate in the panel's storage folder, and the official Pelican Docker
image keeps that folder inside the container, so a new container starts without it. Before 0.3.2 that file was the
only copy.

**Fix:** since 0.3.2 the plugin also keeps a copy in the database and writes the file back by itself. An install
that lost the file before updating to 0.3.2 needs the VPS code pasted once more (Setup, step 1); the code is printed
again on the VPS with `sudo autoproxy-agent show-code`. Restoring cannot weaken the check: every request still pins
the certificate's public key, which is stored separately.

## Certificate error after VPS IP change

**Symptom:** the plugin suddenly reports a certificate mismatch or connection failure after the VPS's public IP
changed (a provider re-IP, a VPS rebuild, moving to a new provider).

**Cause:** the certificate the agent generated and the plugin pinned has the old IP as its subject alternative name;
a new IP doesn't match it, by design — this is the same protection that stops a network position swapping in a
different certificate.

**Fix:** re-running setup on its own is **not** enough — it keeps an existing, readable certificate on purpose, so
that a routine re-run never invalidates the code you already pasted into the plugin. To get a certificate for the
new address you have to remove the old one first:

```bash
sudo rm /etc/autoproxy/tls/agent.crt /etc/autoproxy/tls/agent.key
sudo autoproxy-agent setup                  # add --public-ip <new IP> if auto-detection gets it wrong
sudo autoproxy-agent show-code
```

Your API token and the VPS's WireGuard key are kept, so only the certificate changes. Paste the new VPS code into
the plugin's Setup step 1. Each node client still dials the old address, so regenerate each node's join code from
Setup step 2 and re-run the install command on that host. There is no way to make the old certificate work with a
new IP, and there shouldn't be.

## Orphaned firewall tables from a provider template

**Symptom:** forwarding looks correctly configured (`nft list ruleset` shows the agent's or client's own tables and
they look right) but traffic still doesn't get through, on a VPS or node host that came with a provider-supplied
firewall template.

**Cause:** nftables (and iptables-nft) evaluate every base chain at the same hook independently — **all** of them
must accept a packet for it to pass. A leftover table from a provider image (a different firewall product, a
previous project, a hosting panel's own management agent) with a drop policy in its own `forward` or `input` chain
silently kills traffic while the Auto Proxy tables look perfectly healthy on their own.

**Command:**

```bash
sudo nft list ruleset
```

**Fix:** look for any table you did not create yourself or that Auto Proxy did not create (check against the tables
named in [install-vps.md](install-vps.md) and [install-client.md](install-client.md)). Either remove the unrelated
table if you don't need it, or add the same tunnel/forward accepts that table's own chains would otherwise need.
Never run `nft flush ruleset` to "start clean" on a box you didn't build from scratch — it removes rules other
software on the box may depend on, not just the ones causing the problem.
