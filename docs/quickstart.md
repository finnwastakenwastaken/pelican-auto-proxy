# Quick start

This is the long version of the steps on the [README](../README.md), with something to check after each one. If a
check fails, jump to [troubleshooting.md](troubleshooting.md) — every symptom there names the command to run next.

Every command below with angle brackets (`<join code>`) is a placeholder. The plugin fills in the real value; you
never type it from memory.

## Before you start

You need:

- A VPS with a public IPv4 address, root SSH access, Debian 12 or 13. Tested live on Debian 13; Debian 12 is expected
  to work but has not been tested yet. Ubuntu is not supported in this release — see the FAQ for why.
- One or more node hosts (running Pelican Wings) on the same platforms, also with root access.
- A Pelican Panel where you can sign in as a root admin and import a plugin zip.

The VPS and the node hosts do not need to be reachable from each other directly — only the VPS needs a public IP.
The node hosts dial out to it.

The "about five minutes" this guide is timed at is measured on a clean test setup (a fresh VPS and a fresh
node machine, run end to end), not a first-time user's clock — your run may be faster or slower.

## Step 1: install the agent on the VPS

SSH into the VPS as root (or a user who can `sudo`) and run:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-vps.sh | sudo bash
```

This installs WireGuard, nftables and the `autoproxy-agent` binary, generates a token and a self-signed certificate,
opens the WireGuard and API ports in the firewall, and starts two services. It ends by printing a boxed **VPS code**:
a block of base64 text containing the VPS's public IP, its API port, the certificate fingerprint, the token and the
WireGuard public key. Copy the whole block.

The installer also writes the same information to a root-only file on the VPS, so losing the copy you made is not
fatal — see "how to re-print the VPS code" in [install-vps.md](install-vps.md).

**How to verify:** run `sudo autoproxy-agent show-code` on the VPS. It should print the same block again. Run
`systemctl status autoproxy-agent wg-quick@wg0` — both should be `active (running)`. (`wg0` is the VPS's tunnel
interface; the node hosts use a differently named one, `autoproxy0`.)

## Step 2: connect the plugin to the VPS

In your Pelican Panel: **Admin → Plugins → Import**, upload the Auto Proxy plugin zip, then press **Install** on its
row. Once installed, open **Admin → Auto Proxy → Setup**.

Paste the VPS code from step 1 into the box on step 1 of the Setup page and press **Test connection**.

**How to verify:** the Setup page shows a green "connected" state with the agent's version. If it stays red, the
panel could not reach the VPS on its API port (default 7443) — check that the port is open from wherever the panel
runs, not just from the VPS's own network.

## Step 3: add a node

Step 2 of the Setup page lists your Pelican nodes. For each one you want reachable from the internet, mark it
proxied and choose a mode:

- **This host runs the tunnel client** (real mode) — players see their real IP. Pick this when the node's Wings
  process runs on the same machine you are about to run the join command on.
- **Forwarded from another host on this LAN** (site mode) — pick this when the node is reachable only through
  another machine that already runs (or will run) the tunnel client; you provide that machine's LAN IP.

The plugin then shows a join command for that node, in two flavours:

```bash
curl -fsSL https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/install-client.sh | sudo bash -s -- <join code>
```

or, for Docker Compose, a snippet with `AUTOPROXY_JOIN_CODE=<join code>` set as an environment variable. Run whichever
fits on the node's host, as root.

A node that already has a public IP you control can be left unproxied — players reach it directly, and you only add
it here if you want one public address for everything or want its IP hidden. See
[is this node local or remote?](install-client.md#is-this-node-local-or-remote).

**How to verify:** run `sudo autoproxy-client status` on the node host. It should report a WireGuard handshake under
a couple of minutes old. Back in the plugin, the Setup page (and the Status page) shows the same node's handshake age
within a minute of the client starting — refresh if it still says "never joined".

## Step 4: make an allocation public

On step 3 of Setup you can set a public hostname (optional — without one, the VPS's own IP is shown to players) and
confirm the alias keywords (`proxy` and `public` by default).

To publish a port: **Admin → Nodes → (your node) → Allocations**, and type `proxy` or `public` in the **Alias**
column for the allocation you want open. This only takes effect once a server is assigned to that allocation — an
allocation with the alias but no server keeps the alias but forwards nothing.

**How to verify:** within a minute, **Admin → Auto Proxy → Forwards** lists the allocation with a target and the
public hostname or VPS IP shown in its alias. From another network (not the VPS's own, not the node's LAN), connect
to that address and port with the game client, or with `nc -vz <address> <port>` for a quick reachability check.

## You're done

Clearing the alias closes the port again within a minute. Adding more nodes repeats step 3 with a fresh join command
each time: each node is its own peer with its own join code. A join code stays valid until you rotate that node's
key or remove it from the Setup page, so treat it like a password rather than a one-time token.

If anything above did not match what you saw, [troubleshooting.md](troubleshooting.md) is organised by symptom, not
by step.
