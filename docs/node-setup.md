# Setting up a node machine

**What you'll have at the end:** a fresh Debian machine ready to be added to your Pelican Panel as a node, with
Docker, Wings and a working HTTPS certificate that renews itself. You answer four questions and read one screen of
instructions at the end.

This page is about the machine that runs your game servers. It is a separate job from
[installing the tunnel client](install-client.md), which is what connects that machine to the Auto Proxy VPS. The
setup script here does not install the tunnel client; it tells you when to run it.

## Before you start

- A fresh Debian 12 or 13 machine with root access. Ubuntu is not supported in this release.
- A name for the node, such as `node1.example.com`, in a domain you control.
- Your Pelican Panel, where you can sign in as an admin.

## Run it

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/setup-node.sh -o setup-node.sh
sudo bash setup-node.sh
```

A minimal Debian image may have no `curl`. If the command says `curl: command not found`, run
`sudo apt-get install -y curl ca-certificates` once and repeat it.

To see exactly what it would do without changing anything, add `--dry-run`. Running it a second time is safe:
anything already in place is reported and left alone.

## The four questions

**1. What is this node called?**
The panel reaches this machine by name, and the certificate is issued for that name. The script checks what the
name resolves to right now and tells you whether that is this machine.

**2. Is this machine local or remote?**
Local means the same network as the panel, with no public address of its own, such as a machine at home or in an
office. Remote means a rented server with its own public address. The script guesses from this machine's own
address and you confirm or change it.

**3. How should it get a certificate?**

- `cloudflare`: your DNS is at Cloudflare. Works for local and remote machines, and nothing on this machine has to
  be reachable from the internet. You paste an API token with Zone:DNS:Edit for that zone. The token is written to
  `/etc/letsencrypt/cloudflare.ini`, readable by root only, and is never shown on screen.
- `http`: remote machines only. Let's Encrypt opens a connection to port 80 on this machine to check the name, so
  port 80 has to be reachable from the internet. The script refuses this for a local machine, because a machine
  with no public address of its own cannot be reached that way.
- `existing`: you already have a certificate. You give the two paths and nothing is issued.

**4. Do you want Auto Proxy for this node's game ports?**
Default yes for a local machine, no for a remote one. The script installs nothing for this; it prints what to do
next. Players reach a proxied node through the VPS, so nothing has to be opened at the node.

## DNS at Cloudflare, and the one setting that breaks everything

The node's DNS record must be **DNS only**, the grey cloud. With the orange cloud (proxied) the node does not work:

- SFTP on port 2022 fails, because Cloudflare only carries web ports.
- Cloudflare's own certificate sits in front of Wings, so the panel cannot verify the node.
- Websocket consoles may break, so the server console in the panel stops updating.

There is no partial version of this. Orange cloud means a node that does not work, not a node that works a bit
slower.

### Local node

The node has no public address of its own, so nothing on the internet can reach it directly. Point
`node1.example.com` at the **Auto Proxy VPS's public address**, grey cloud. For example, if your VPS is
`198.51.100.10`, that is the address in the record.

After the node has joined the proxy, the panel admin adds two manual forwards on the **Forwards** page, both
targeting "A machine running a tunnel client" and this node:

- TCP 8080 (the panel talking to Wings)
- TCP 2022 (SFTP)

Panel to node traffic then goes through the VPS. That is the only route in when the node has no public address of
its own.

### Remote node

The node has its own public address. Point `node1.example.com` at that address, grey cloud. For example
`203.0.113.20`. Auto Proxy is optional here: it hides the node's address and gives you one public address for every
server, at the cost of one extra network hop through the VPS.

### The honest downsides

- Orange cloud: nothing works. Not slower, not partly. See the list above.
- Hiding a rented server's address costs one extra hop, so latency goes up by the distance between the VPS and the
  node. Put the VPS near the node.
- The HTTP challenge needs port 80 open on this machine, which is one more open port than a local node has.
- The Cloudflare token can edit DNS for the whole zone. Keep it on this machine, do not reuse it elsewhere, and
  delete it at Cloudflare if the machine is ever lost.

## What the script installs

- Docker CE from Docker's own apt repository.
- The Wings binary for this machine's architecture at `/usr/local/bin/wings`, with `/etc/pelican` for its config.
- A `wings` systemd service, enabled at boot but **not started**. Wings has no config until the panel gives it one,
  so starting it early only produces errors.
- `certbot` (plus the Cloudflare DNS plugin when you chose that method), a certificate for the node's name, and
  Debian's `certbot.timer` enabled and verified active. The script checks the timer really is active and says so
  if it is not, because a renewal that never runs looks exactly like one that works until the certificate expires.
- `/etc/letsencrypt/renewal-hooks/deploy/wings-reload.sh`, which restarts Wings after a renewal, and only when it
  was this node's certificate that renewed. Other certificates on the same machine do not bounce your game servers.

If Wings is already installed, the script says so and changes nothing. It never overwrites an existing
`/etc/pelican/config.yml`.

## After the script

It prints the exact values to enter in **Admin -> Nodes -> Create Node**:

| Field | Value |
| --- | --- |
| FQDN | the hostname you gave, for example `node1.example.com` |
| Communicate over SSL | yes |
| Daemon port | 8080 |
| Daemon SFTP port | 2022 |
| Memory and disk | the amounts detected on this machine, shown on screen |

Wings reads its certificate from `/etc/letsencrypt/live/<your hostname>/fullchain.pem` and `privkey.pem`. Those are
the paths the panel writes into the node config, so leave them alone.

Then, on the node's **Configuration** tab in the panel, copy the auto-deploy command, run it on this machine, and
start Wings:

```bash
systemctl start wings
```

If you chose Auto Proxy in question 4, mark the node proxied on **Admin -> Auto Proxy -> Setup** and run the join
command it shows. See [quick start step 3](quickstart.md#step-3-add-a-node). For a remote node, your provider needs
exactly one outbound rule, UDP to the VPS on port 51820, and no inbound rules at all.

The script can also run the join command for you at the end if you paste it. It only accepts the command in the
exact form the Setup page prints and refuses anything else, so a mistyped or unexpected paste is never run.

### Let the panel reach this node directly

If the node's hostname points at the VPS (so that its own IP stays hidden, or because players' browsers reach the
console through the VPS), the panel's own calls to Wings take that detour too. Pelican gives some of those calls one
second, so a single lost packet on the way makes a node flicker "offline" or breaks the console page. Keep public
DNS as it is and give only the panel a direct route, with a hosts entry for the node's hostname:

- a node on the panel's own network: its LAN address;
- a node elsewhere: its own public address, with Wings' port open to the panel's address only.

On a panel in Docker, add the entry to the panel service in `compose.yml` and re-create the container:

```yaml
    extra_hosts:
      - "node1.example.com:192.0.2.10"
```

Nothing else changes: the certificate is still checked against the same hostname, and players and browsers still go
through the VPS. See [troubleshooting](troubleshooting.md#nodes-flicker-offline-or-the-console-page-shows-403-errors).

## Scripting it

Every question has a flag and an environment variable, so the whole thing can run unattended with `--yes`:

```bash
sudo AUTOPROXY_NODE_HOSTNAME=node1.example.com \
     AUTOPROXY_NODE_PLACEMENT=local \
     AUTOPROXY_NODE_CERT=cloudflare \
     CLOUDFLARE_API_TOKEN=... \
     bash setup-node.sh --yes --proxied yes
```

`--dry-run` prints every action and changes nothing. `--print-certbot-command` prints the certbot command that
would be used and exits, which is useful if you want to run the certificate step yourself.

## If something goes wrong

- The script refuses a hostname without a dot in it. Use the full name, `node1.example.com`, not `node1`.
- Certificate issue fails with the Cloudflare method: the token needs Zone:DNS:Edit **for that zone**, and DNS for
  the domain has to actually be at Cloudflare.
- Certificate issue fails with the HTTP method: port 80 on this machine has to be reachable from the internet
  while the check runs, and nothing else may be listening on it.
- The panel says it cannot reach the node: check the DNS record is grey cloud, and for a local node check the two
  manual forwards exist on the Forwards page.

More symptoms, and the command to run for each: [troubleshooting.md](troubleshooting.md).
