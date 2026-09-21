#!/usr/bin/env python3
"""Fake Pelican Auto Proxy VPS agent: enough of the real API to exercise the plugin.

This is a test double. It speaks the same HTTP API as the real `autoproxy-agent`
on a VPS, but it touches nothing: no WireGuard, no nftables, no root. It keeps
its state in memory and forgets everything when you stop it. Run it on your own
machine, point the Pelican plugin at it, and you can click through the whole
Setup page (peers, join codes, rules, errors) without owning a VPS.

Quick start (plain HTTP, no certificates):

    python3 fake-agent.py --port 7443 --token devtoken
    curl -H 'Authorization: Bearer devtoken' http://127.0.0.1:7443/v1/status

Quick start (HTTPS, like the real agent, with a certificate made on the spot):

    python3 fake-agent.py --tls --host 127.0.0.1 --port 7443
    # it prints the certificate; save it as agent.pem, then:
    curl --cacert agent.pem -H 'Authorization: Bearer devtoken' \
         https://127.0.0.1:7443/v1/status

It also prints an `sha256//...` pin so you can test certificate pinning.
The private key is written to a temporary file, never printed, and deleted when
the process exits.

What it implements (bearer token required on every route):

    GET    /v1/status            version, uptime, tunnel and rule counters
    GET    /v1/rules             the last rule set it accepted
    PUT    /v1/rules             full replace, all-or-nothing
    GET    /v1/peers             every peer, with handshake age, rx and tx
    POST   /v1/peers             create a peer, returns a join code ONCE
    DELETE /v1/peers/{id}        remove a peer and every rule that used it
    POST   /v1/peers/{id}/rotate new key, new join code
    POST   /v1/token/rotate      new API token; the old one stops working

A rule sends one public port (or a range of them) somewhere down the tunnel,
and says where in exactly one of two ways:

    real-IP mode   {"id": "node1-a", "proto": "both", "public_port": 25565,
                    "target_peer": "ab12cd34ef56", "note": "Minecraft"}
        straight to that peer's own tunnel address, so the game server sees the
        player's real IP.

    site mode      {"id": "node2-a", "proto": "tcp", "public_port": 25566,
                    "target_ip": "10.0.0.10", "via_peer": "ab12cd34ef56"}
        to a machine on the LAN behind that peer. The address must be private
        and must sit inside one of the peer's own LAN ranges.

Things it refuses, the same way the real agent does, each with a plain reason:
ports outside 1-65535, a range that also tries to remap the port, its own SSH,
WireGuard and API ports, a rule with both target styles or neither, an unknown
peer, a public target address, two rules fighting over the same port, and more
than 4096 rules. Nothing is applied unless every rule passes: you get HTTP 422
and a list of {"id", "reason"}.

Error handling you can test on purpose:

  * Five bad tokens from one address and that address is locked out. The fifth
    bad token is still answered 401, like any other bad token; every request
    after it gets HTTP 429 with a `Retry-After` header, until the lockout
    lifts. `--lockout-seconds 2` makes that quick to try. One good token clears
    the counter.
  * `--simulate-handshake-after N` decides how long a new peer reports
    `handshake_age_s: null` ("waiting for first handshake") before it starts
    reporting an age ("connected"). Use `-1` for a peer that never connects.

Response shapes match the real agent exactly, including the ones that are easy
to get wrong:

    GET  /v1/peers            {"peers": [...]}, not a bare array
    POST /v1/peers            201 {"peer": {...}, "join_code": "...", "warning": "..."}
    POST /v1/peers/{id}/rotate 200, same three keys as a create
    PUT  /v1/rules            200 {"applied": {...}, "applied_at": "..."}
                              422 {"rejected": [{"id", "reason"}]}
    POST /v1/peers 422        {"error": "..."} - a different shape from a rules 422
    POST /v1/token/rotate     200 {"token": "...", "warning": "..."}
    DELETE /v1/peers/{id}     204, no body
    401                       {"error": "unauthorized"} + WWW-Authenticate
    429                       {"error": "too many failed authentication ..."} + Retry-After

Two deliberate differences from the real agent, both to help while developing:
a 404 body also lists the routes it does know, and the lockout window is
adjustable. Everything else that differs is a bug in this file.

Every request, every rejection and every accepted rule set is logged to stdout
in full, so you can see exactly what the plugin sent.

Nothing here is real: keys are random bytes, the tunnel addresses come from the
same 10.66.66.0/24 pool the real agent uses, and public addresses in the
examples are documentation ranges (192.0.2.0/24, 198.51.100.0/24,
203.0.113.0/24).
"""

import argparse
import atexit
import base64
import hashlib
import hmac
import ipaddress
import json
import os
import secrets
import shutil
import signal
import ssl
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

# ---------------------------------------------------------------- constants --

MAX_RULES = 4096               # the real agent's cap on one rule set
MAX_BODY = 1 << 20             # 1 MiB, the real agent's body limit
MAX_PEERS = 253                # a /24 tunnel has no more addresses than this
TUNNEL_SUBNET = "10.66.66.0/24"
VPS_TUNNEL_IP = "10.66.66.1"
POOL_FIRST, POOL_LAST = 2, 254  # peers get 10.66.66.2 .. 10.66.66.254
JOIN_CODE_VERSION = 1          # bumped by the real agent when fields change
KEEPALIVE = 25                 # seconds; keeps the home router's NAT open
HEALTHY_AGE_S = 180            # a peer quieter than this counts as unhealthy
HANDSHAKE_CYCLE_S = 120        # real WireGuard re-handshakes about this often
SSH_PORT = 22
MAX_NAME_LEN = 64              # the real agent refuses a longer peer name
ENV_FILE = "/etc/autoproxy/agent.env"  # where the real agent keeps the token

# Word for word what the real agent returns, so a caller that shows the warning
# to an admin shows the same sentence here.
CREATE_WARNING = ("This join code contains the client's private key and is shown "
                  "exactly once. If it is lost, rotate the peer.")
ROTATE_WARNING = ("The previous key stopped working. Re-run the client installer "
                  "with this join code; it is shown exactly once.")
TOKEN_WARNING = ("The previous token stopped working. This value is shown once; "
                 "it is also in %s (mode 0600)." % ENV_FILE)

RFC1918 = [
    ipaddress.ip_network("10.0.0.0/8"),
    ipaddress.ip_network("172.16.0.0/12"),
    ipaddress.ip_network("192.168.0.0/16"),
]


def log(fmt, *args):
    """One timestamped line on stdout. Everything this agent does is logged."""
    print("%s  %s" % (time.strftime("%H:%M:%S"), (fmt % args) if args else fmt), flush=True)


def rfc3339(when=None):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(when))


def is_rfc1918(text):
    """True only for a private IPv4 address. Loopback and IPv6 are not."""
    try:
        addr = ipaddress.ip_address(str(text).strip())
    except ValueError:
        return False
    if addr.version != 4:
        return False
    return any(addr in net for net in RFC1918)


def fake_wg_key():
    """A random 32-byte value in WireGuard's base64 shape. Not a real key."""
    return base64.b64encode(os.urandom(32)).decode()


def new_peer_id():
    """Short opaque id, the same shape the real agent uses (12 hex chars)."""
    return os.urandom(6).hex()


def is_int(value):
    return isinstance(value, int) and not isinstance(value, bool)


# -------------------------------------------------------------------- state --

class Agent:
    """Everything the fake agent knows. One lock: requests arrive on threads."""

    def __init__(self, opts):
        self.lock = threading.RLock()
        self.started = time.monotonic()
        self.token = opts.token
        self.lockout_after = opts.lockout_after
        self.lockout_seconds = opts.lockout_seconds
        self.handshake_after = opts.simulate_handshake_after
        self.wg_port = opts.wg_port
        self.wg_iface = opts.wg_iface
        self.endpoint = "%s:%d" % (opts.advertise_ip, opts.wg_port)
        self.wg_pubkey = fake_wg_key()
        self.version = opts.version
        # Ports the real agent will never forward: its own SSH, its WireGuard
        # listener, and the port this API is listening on.
        self.reserved = sorted({SSH_PORT, opts.wg_port, opts.port})
        self.peers = {}          # id -> internal peer dict
        self.rules = []          # last accepted set, as sent
        self.applied_at = None
        self.last_error = ""
        self.failures = {}       # client ip -> {"count", "until", "logged"}

    # -- authentication and lockout ------------------------------------------

    def check_auth(self, client_ip, header):
        """Returns ("ok", 0) | ("bad", 0) | ("locked", retry_after_seconds)."""
        now = time.monotonic()
        with self.lock:
            entry = self.failures.get(client_ip)
            if entry and entry["until"] > now:
                return "locked", int(entry["until"] - now) + 1

            if hmac.compare_digest(header or "", "Bearer " + self.token):
                if entry:
                    log("AUTH ok again from %s; failure count reset", client_ip)
                    self.failures.pop(client_ip, None)
                return "ok", 0

            entry = entry or {"count": 0, "until": 0.0, "logged": False}
            entry["count"] += 1
            self.failures[client_ip] = entry
            if entry["count"] >= self.lockout_after:
                entry["until"] = now + self.lockout_seconds
                if not entry["logged"]:
                    entry["logged"] = True
                    log("LOCKOUT %s after %d bad tokens; locked for %d s",
                        client_ip, entry["count"], self.lockout_seconds)
                # Still a 401, exactly like the real agent: the request that
                # trips the lockout is answered as the bad token it was. The
                # 429 starts on the NEXT request, which is what the plugin has
                # to handle.
                return "bad", 0
            log("AUTH failed from %s (%d of %d before lockout)",
                client_ip, entry["count"], self.lockout_after)
            return "bad", 0

    def rotate_token(self):
        with self.lock:
            self.token = secrets.token_hex(32)
            self.failures.clear()
            log("!" * 60)
            log("TOKEN ROTATED. The old token is dead from this second on.")
            log("New token: %s", self.token)
            log("Paste it into the plugin settings or every call returns 401.")
            log("!" * 60)
            return self.token

    # -- peers ---------------------------------------------------------------

    def _handshake_age(self, peer):
        """None until the simulated first handshake, then a plausible age.

        Real WireGuard re-handshakes every couple of minutes while traffic
        flows, so the age cycles instead of growing forever.
        """
        if self.handshake_after < 0:
            return None
        alive = time.monotonic() - peer["_created_mono"] - self.handshake_after
        if alive < 0:
            return None
        return int(alive) % HANDSHAKE_CYCLE_S

    def _traffic(self, peer):
        if self._handshake_age(peer) is None:
            return 0, 0
        alive = max(0.0, time.monotonic() - peer["_created_mono"] - self.handshake_after)
        return int(alive * 1500), int(alive * 2300)

    def peer_view(self, peer):
        rx, tx = self._traffic(peer)
        return {
            "id": peer["id"],
            "name": peer["name"],
            "public_key": peer["public_key"],
            "tunnel_ip": peer["tunnel_ip"],
            "mode": peer["mode"],
            "lan_cidrs": peer["lan_cidrs"],
            "handshake_age_s": self._handshake_age(peer),
            "rx": rx,
            "tx": tx,
            "created_at": peer["created_at"],
        }

    def _next_tunnel_ip(self):
        taken = {p["tunnel_ip"] for p in self.peers.values()}
        base = ipaddress.ip_network(TUNNEL_SUBNET).network_address
        for offset in range(POOL_FIRST, POOL_LAST + 1):
            candidate = str(base + offset)
            if candidate not in taken:
                return candidate
        return None

    def _join_code(self, peer, private_key):
        blob = {
            "v": JOIN_CODE_VERSION,
            "endpoint": self.endpoint,
            "vps_wg_pubkey": self.wg_pubkey,
            "client_privkey": private_key,
            "client_address": peer["tunnel_ip"] + "/32",
            "tunnel_subnet": TUNNEL_SUBNET,
            "vps_tunnel_ip": VPS_TUNNEL_IP,
            "mode": peer["mode"],
        }
        # lan_cidrs carries `omitempty` on the Go side, so a real-IP join code
        # has no lan_cidrs key at all. Sending an empty list instead would let
        # the client grow a dependency on a key the VPS never emits. Inserted
        # here, before keepalive, so the key order matches the Go struct too.
        if peer["lan_cidrs"]:
            blob["lan_cidrs"] = peer["lan_cidrs"]
        blob["keepalive"] = KEEPALIVE
        raw = json.dumps(blob, separators=(",", ":")).encode()
        # base64url without padding, so it survives being pasted into a shell.
        return base64.urlsafe_b64encode(raw).decode().rstrip("=")

    def _check_cidrs(self, mode, lan, self_id=None):
        """The real agent's lan_cidrs checks, in the same order.

        Returns (True, canonical_list) or (False, plain-language reason). Caller
        holds the lock. Overlaps are refused rather than merged: two peers
        claiming one range give the kernel two answers for the same address.
        """
        if mode == "real":
            if lan:
                return False, ("lan_cidrs is only valid in site mode; a real-IP "
                               "peer forwards to itself")
            return True, []
        if not lan:
            return False, ("site mode needs at least one lan_cidr: it describes "
                           "which addresses this peer may reach")

        tunnel = ipaddress.ip_network(TUNNEL_SUBNET)
        out, parsed = [], []
        for raw in lan:
            text = raw.strip()
            try:
                # strict=True: the real agent refuses host bits and tells the
                # admin what to write instead, rather than silently masking.
                net = ipaddress.ip_network(text, strict=True)
            except ValueError:
                try:
                    masked = ipaddress.ip_network(text, strict=False)
                except ValueError:
                    return False, '"%s" is not a valid IPv4 CIDR' % raw
                return False, ('lan_cidrs: "%s" has host bits set; write it as %s'
                               % (raw, masked))
            if net.version != 4:
                return False, ('lan_cidrs: "%s" is not IPv4; IPv6 is not supported yet' % raw)
            if not any(net.subnet_of(p) for p in RFC1918):
                return False, ('lan_cidrs: "%s" is not a private (RFC1918) range' % raw)
            if net.overlaps(tunnel):
                return False, ("lan_cidrs: %s overlaps the tunnel subnet %s"
                               % (net, tunnel))
            for other in parsed:
                if net.overlaps(other):
                    return False, ("lan_cidrs: %s and %s overlap each other" % (net, other))
            for peer in self.peers.values():
                if peer["id"] == self_id:
                    continue
                for claimed in peer["lan_cidrs"]:
                    if net.overlaps(ipaddress.ip_network(claimed)):
                        return False, ('lan_cidrs: %s overlaps %s, already claimed by peer "%s"'
                                       % (net, claimed, peer["name"]))
            parsed.append(net)
            out.append(str(net))
        return True, out

    def create_peer(self, body):
        name = body.get("name")
        mode = body.get("mode")
        lan = body.get("lan_cidrs") or []

        if not isinstance(name, str) or not name.strip():
            return 422, {"error": "name is required: it is what the admin sees in the peer list"}
        name = name.strip()
        if len(name) > MAX_NAME_LEN:
            return 422, {"error": "name is longer than %d characters" % MAX_NAME_LEN}
        if mode not in ("real", "site"):
            return 422, {"error": 'mode must be "real" (player IPs survive) or "site" (forwards on to a LAN)'}
        if not isinstance(lan, list) or not all(isinstance(c, str) for c in lan):
            return 422, {"error": "lan_cidrs must be a list of CIDR strings, for example [\"10.0.0.0/24\"]"}

        # Deliberately NOT checked, because the real agent does not check it
        # either: two peers may share a name. The id is what identifies a peer,
        # and refusing a duplicate name here would have the plugin tested
        # against a rule the VPS does not enforce.
        with self.lock:
            if len(self.peers) >= MAX_PEERS:
                return 422, {"error": "no room left: the tunnel holds at most %d peers" % MAX_PEERS}

            ok, result = self._check_cidrs(mode, lan)
            if not ok:
                return 422, {"error": result}
            lan = result

            tunnel_ip = self._next_tunnel_ip()
            if tunnel_ip is None:
                return 422, {"error": "the tunnel address pool %s is full" % TUNNEL_SUBNET}

            private_key = fake_wg_key()
            peer = {
                "id": new_peer_id(),
                "name": name,
                "public_key": fake_wg_key(),
                "tunnel_ip": tunnel_ip,
                "mode": mode,
                "lan_cidrs": lan,
                "created_at": rfc3339(),
                "_created_mono": time.monotonic(),
            }
            self.peers[peer["id"]] = peer
            code = self._join_code(peer, private_key)

        log("PEER CREATED %s name=%s mode=%s tunnel_ip=%s lan=%s",
            peer["id"], name, mode, tunnel_ip, lan or "-")
        log("  join code handed out once (%d chars); the private key is not stored", len(code))
        return 201, {
            "peer": self.peer_view(peer),
            "join_code": code,
            "warning": CREATE_WARNING,
        }

    def delete_peer(self, peer_id):
        with self.lock:
            peer = self.peers.pop(peer_id, None)
            if peer is None:
                return 404, {"error": 'unknown peer "%s"' % peer_id}
            kept, dropped = [], []
            for rule in self.rules:
                if rule.get("target_peer") == peer_id or rule.get("via_peer") == peer_id:
                    dropped.append(rule)
                else:
                    kept.append(rule)
            self.rules = kept
            if dropped:
                self.applied_at = rfc3339()
        log("PEER DELETED %s (%s)", peer_id, peer["name"])
        for rule in dropped:
            log("  dropped rule %s (public port %s) because its peer is gone",
                rule.get("id"), rule.get("public_port"))
        if dropped:
            log("  %d rule(s) removed, %d still applied", len(dropped), len(kept))
        return 204, None

    def rotate_peer(self, peer_id):
        with self.lock:
            peer = self.peers.get(peer_id)
            if peer is None:
                return 404, {"error": 'unknown peer "%s"' % peer_id}
            peer["public_key"] = fake_wg_key()
            peer["_created_mono"] = time.monotonic()
            code = self._join_code(peer, fake_wg_key())
        log("PEER ROTATED %s (%s): new key, new join code, handshake clock restarted",
            peer_id, peer["name"])
        # Same envelope as a create: the real agent returns the peer as well, so
        # the caller can show what it just rotated without a second request.
        return 200, {
            "peer": self.peer_view(peer),
            "join_code": code,
            "warning": ROTATE_WARNING,
        }

    # -- rules ---------------------------------------------------------------

    def counts(self, rules):
        return {
            "tcp": sum(1 for r in rules if r.get("proto") in ("tcp", "both")),
            "udp": sum(1 for r in rules if r.get("proto") in ("udp", "both")),
            "rules": len(rules),
        }

    def _reserved_hit(self, start, end):
        for port in self.reserved:
            if start <= port <= end:
                return port
        return None

    def _check_rule(self, rule, spans):
        """Returns a plain-language reason, or "" when the rule is fine.

        On success the rule's port span is recorded in `spans` so later rules
        can be checked against it.
        """
        if not isinstance(rule, dict):
            return "a rule must be a JSON object"

        rid = str(rule.get("id") or "").strip()
        if not rid:
            return "id is required so the panel can match this rule up again"

        proto = rule.get("proto")
        if proto not in ("tcp", "udp", "both"):
            return 'proto %r must be "tcp", "udp" or "both"' % (proto,)

        port = rule.get("public_port")
        if not is_int(port) or not 1 <= port <= 65535:
            return "public_port %r is out of range 1-65535" % (port,)

        end = rule.get("public_port_end")
        if end is None:
            end = port
        elif not is_int(end):
            return "public_port_end %r is not a port number" % (end,)
        elif end < port:
            return "public_port_end %d is below public_port %d" % (end, port)
        elif end > 65535:
            return "public_port_end %d is out of range 1-65535" % (end,)

        target_port = rule.get("target_port")
        if target_port is not None:
            if not is_int(target_port) or not 1 <= target_port <= 65535:
                return "target_port %r is out of range 1-65535" % (target_port,)
            if end != port:
                return "ranges cannot be remapped: a port range keeps its own port numbers, so drop target_port"

        hit = self._reserved_hit(port, end)
        if hit is not None:
            return "port %d is reserved on the VPS" % hit

        target_peer = rule.get("target_peer")
        target_ip = rule.get("target_ip")
        via_peer = rule.get("via_peer")
        has_peer = isinstance(target_peer, str) and target_peer.strip() != ""
        has_ip = isinstance(target_ip, str) and target_ip.strip() != ""
        has_via = isinstance(via_peer, str) and via_peer.strip() != ""

        if has_peer and has_ip:
            return ("a rule carries either target_peer (real-IP mode) or target_ip with via_peer "
                    "(site mode), never both")
        if not has_peer and not has_ip:
            return ("a rule needs a target: target_peer for real-IP mode, or target_ip with "
                    "via_peer for site mode")

        if has_peer:
            if has_via:
                return "via_peer belongs with target_ip; in real-IP mode target_peer is the whole target"
            peer = self.peers.get(target_peer.strip())
            if peer is None:
                return 'unknown peer "%s": no peer with that id has joined' % target_peer.strip()
            # The other half of the asymmetry the real agent enforces: a site
            # peer is a way on to a LAN, not a destination in itself. Without
            # this the fake agent would accept a rule the VPS refuses, and the
            # plugin's pickers would look right while offering the wrong half.
            if peer["mode"] == "site":
                return ('peer "%s" is in site mode: forward to an address on its LAN with '
                        'target_ip and via_peer' % peer["id"])
            signature = "%s:%s" % (peer["tunnel_ip"], target_port if target_port else "keep")
        else:
            if not has_via:
                return "target_ip needs via_peer: the agent must know which tunnel to send it down"
            peer = self.peers.get(via_peer.strip())
            if peer is None:
                return 'unknown peer "%s": no peer with that id has joined' % via_peer.strip()
            # Same order as the real agent: mode first, then the address.
            if peer["mode"] != "site":
                return ('peer "%s" runs in real-IP mode and cannot forward on to another machine; '
                        'use target_peer instead' % peer["id"])
            if not is_rfc1918(target_ip):
                return "target_ip is not a private address (10/8, 172.16/12 or 192.168/16)"
            addr = ipaddress.ip_address(target_ip.strip())
            nets = [ipaddress.ip_network(c) for c in peer["lan_cidrs"]]
            if not any(addr in n for n in nets):
                return ('target_ip %s is not inside peer "%s" LAN ranges (%s)'
                        % (target_ip.strip(), peer["id"], ", ".join(peer["lan_cidrs"]) or "none"))
            signature = "%s@%s:%s" % (target_ip.strip(), peer["id"],
                                      target_port if target_port else "keep")

        protos = ("tcp", "udp") if proto == "both" else (proto,)
        for one in protos:
            for start, stop, other_sig, other_id in spans[one]:
                if port <= stop and start <= end:
                    if other_sig != signature or (start, stop) != (port, end):
                        return ('%s ports %d-%d overlap rule "%s" (%d-%d) with a different target; '
                                'one public port can only go to one place'
                                % (one, port, end, other_id, start, stop))
        for one in protos:
            spans[one].append((port, end, signature, rid))
        return ""

    def validate(self, rules):
        if not isinstance(rules, list):
            return [{"id": "", "reason": "rules must be a list"}]
        if len(rules) > MAX_RULES:
            return [{"id": "", "reason": "%d rules is more than the limit of %d; nothing was applied"
                     % (len(rules), MAX_RULES)}]
        rejected = []
        seen_ids = set()
        spans = {"tcp": [], "udp": []}
        for rule in rules:
            rid = str(rule.get("id") or "").strip() if isinstance(rule, dict) else ""
            reason = self._check_rule(rule, spans)
            if not reason and rid in seen_ids:
                reason = 'duplicate rule id "%s"' % rid
            if reason:
                # An empty id stays empty, as in the real agent: it is how the
                # caller tells "this rule has no id" from a named rule.
                rejected.append({"id": rid, "reason": reason})
            else:
                seen_ids.add(rid)
        return rejected

    def describe(self, rule):
        """One readable line per rule, for the log."""
        span = str(rule.get("public_port"))
        if rule.get("public_port_end") not in (None, rule.get("public_port")):
            span += "-%s" % rule.get("public_port_end")
        if rule.get("target_peer"):
            peer = self.peers.get(rule["target_peer"])
            where = "peer %s (%s) real-IP" % (rule["target_peer"],
                                              peer["tunnel_ip"] if peer else "?")
        else:
            where = "%s via peer %s (site)" % (rule.get("target_ip"), rule.get("via_peer"))
        if rule.get("target_port"):
            where += " port %s" % rule["target_port"]
        note = (" note=%s" % rule["note"]) if rule.get("note") else ""
        return '  rule "%s" %s %s -> %s%s' % (rule.get("id"), rule.get("proto"), span, where, note)

    def put_rules(self, rules):
        rejected = self.validate(rules)
        if rejected:
            with self.lock:
                self.last_error = json.dumps(rejected)
            log("REJECTED %d rule(s); nothing applied, the old set is still live", len(rejected))
            for item in rejected:
                log("  %s: %s", item["id"], item["reason"])
            return 422, {"rejected": rejected}

        with self.lock:
            self.rules = rules
            self.applied_at = rfc3339()
            self.last_error = ""
            applied = self.counts(rules)
            stamp = self.applied_at
        log("APPLIED %d rule(s)  tcp=%d udp=%d", applied["rules"], applied["tcp"], applied["udp"])
        for rule in rules:
            log("%s", self.describe(rule))
        return 200, {"applied": applied, "applied_at": stamp}

    # -- status --------------------------------------------------------------

    def status(self):
        with self.lock:
            ages = [self._handshake_age(p) for p in self.peers.values()]
            known = [a for a in ages if a is not None]
            return {
                "version": self.version,
                "uptime_s": int(time.monotonic() - self.started),
                # No listen_port here: the real agent's wg block is
                # {iface, peers, handshake_age_s, rx, tx} plus an "error" key
                # only when reading the interface failed.
                "wg": {
                    "iface": self.wg_iface,
                    "peers": len(self.peers),
                    "handshake_age_s": min(known) if known else None,
                    "rx": sum(self._traffic(p)[0] for p in self.peers.values()),
                    "tx": sum(self._traffic(p)[1] for p in self.peers.values()),
                },
                "applied": self.counts(self.rules),
                "applied_at": self.applied_at,
                "last_error": self.last_error,
                "peers": {
                    "total": len(self.peers),
                    "healthy": sum(1 for a in ages if a is not None and a < HEALTHY_AGE_S),
                },
            }


AGENT = None  # set in main()

ROUTES_HELP = [
    "GET /v1/status", "GET /v1/rules", "PUT /v1/rules", "GET /v1/peers",
    "POST /v1/peers", "DELETE /v1/peers/{id}", "POST /v1/peers/{id}/rotate",
    "POST /v1/token/rotate",
]


# ------------------------------------------------------------------ handler --

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "autoproxy-fake-agent"
    sys_version = ""

    def log_message(self, fmt, *args):
        log(fmt, *args)

    def _client(self):
        return self.client_address[0]

    def _send(self, code, payload, extra_headers=None):
        body = b"" if payload is None else json.dumps(payload, indent=2).encode()
        self.send_response(code)
        if body:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        for key, value in (extra_headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        if body:
            self.wfile.write(body)
        log("  -> HTTP %d %s", code, "(%d bytes)" % len(body) if body else "(no body)")

    def _drain(self):
        """Swallow an unread request body so the next request on this
        connection is not read as garbage."""
        length = int(self.headers.get("Content-Length") or 0)
        if 0 < length <= MAX_BODY:
            self.rfile.read(length)

    def _authed(self):
        state, retry = AGENT.check_auth(self._client(), self.headers.get("Authorization", ""))
        if state == "ok":
            return True
        self._drain()
        if state == "locked":
            # Body and header word for word like the real agent: it does not
            # name the address or the remaining seconds in the body, only in
            # Retry-After, and the plugin reads the header.
            log("  locked out; Retry-After: %d s", retry)
            self._send(429, {"error": "too many failed authentication attempts from "
                                      "this address; try again later"},
                       {"Retry-After": str(retry)})
            return False
        self._send(401, {"error": "unauthorized"},
                   {"WWW-Authenticate": 'Bearer realm="autoproxy"'})
        return False

    def _body(self):
        """Returns (ok, parsed). Sends the error itself when not ok."""
        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY:
            self._send(413, {"error": "body larger than 1 MiB"})
            return False, None
        raw = self.rfile.read(length) if length else b""
        if raw:
            log("  body (%d bytes):\n%s", len(raw), raw.decode("utf-8", "replace"))
        try:
            parsed = json.loads(raw or b"{}")
        except json.JSONDecodeError as exc:
            self._send(400, {"error": "invalid json: %s" % exc})
            return False, None
        if not isinstance(parsed, dict):
            self._send(400, {"error": "the body must be a JSON object"})
            return False, None
        return True, parsed

    def _not_found(self):
        self._send(404, {"error": "no such route", "routes": ROUTES_HELP})

    def _path(self):
        return urlsplit(self.path).path.rstrip("/") or "/"

    def _start(self):
        log("%s %s from %s", self.command, self.path, self._client())

    def do_GET(self):
        self._start()
        if not self._authed():
            return
        path = self._path()
        if path == "/v1/status":
            self._send(200, AGENT.status())
        elif path == "/v1/rules":
            self._send(200, {"rules": AGENT.rules})
        elif path == "/v1/peers":
            # Wrapped in an object, exactly like the real agent: every route
            # answers with an object, so a field can be added beside the list
            # later without breaking a caller that expects a bare array.
            self._send(200, {"peers": [AGENT.peer_view(p) for p in AGENT.peers.values()]})
        else:
            self._not_found()

    def do_PUT(self):
        self._start()
        if not self._authed():
            return
        if self._path() != "/v1/rules":
            self._not_found()
            return
        ok, parsed = self._body()
        if not ok:
            return
        code, payload = AGENT.put_rules(parsed.get("rules", []))
        self._send(code, payload)

    def do_POST(self):
        self._start()
        if not self._authed():
            return
        path = self._path()
        if path == "/v1/token/rotate":
            self._send(200, {"token": AGENT.rotate_token(), "warning": TOKEN_WARNING})
            return
        if path == "/v1/peers":
            ok, parsed = self._body()
            if not ok:
                return
            code, payload = AGENT.create_peer(parsed)
            self._send(code, payload)
            return
        parts = path.split("/")
        if len(parts) == 5 and parts[1:3] == ["v1", "peers"] and parts[4] == "rotate":
            code, payload = AGENT.rotate_peer(parts[3])
            self._send(code, payload)
            return
        self._not_found()

    def do_DELETE(self):
        self._start()
        if not self._authed():
            return
        parts = self._path().split("/")
        if len(parts) == 4 and parts[1:3] == ["v1", "peers"]:
            code, payload = AGENT.delete_peer(parts[3])
            self._send(code, payload)
            return
        self._not_found()


# ---------------------------------------------------------------------- TLS --

def _san_kind(host):
    try:
        ipaddress.ip_address(host)
        return "IP"
    except ValueError:
        return "DNS"


def _generate_with_cryptography(host, cert_path, key_path):
    import datetime
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import rsa
    from cryptography.x509.oid import NameOID

    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, host)])
    if _san_kind(host) == "IP":
        san = x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address(host))])
    else:
        san = x509.SubjectAlternativeName([x509.DNSName(host)])
    now = datetime.datetime.now(datetime.timezone.utc)
    cert = (
        x509.CertificateBuilder()
        .subject_name(name)
        .issuer_name(name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - datetime.timedelta(minutes=5))
        .not_valid_after(now + datetime.timedelta(days=3650))
        .add_extension(san, critical=False)
        .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .sign(key, hashes.SHA256())
    )
    with open(key_path, "wb") as fh:
        fh.write(key.private_bytes(serialization.Encoding.PEM,
                                   serialization.PrivateFormat.TraditionalOpenSSL,
                                   serialization.NoEncryption()))
    with open(cert_path, "wb") as fh:
        fh.write(cert.public_bytes(serialization.Encoding.PEM))


def _generate_with_openssl(host, cert_path, key_path):
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", key_path, "-out", cert_path, "-days", "3650",
         "-subj", "/CN=%s" % host,
         "-addext", "subjectAltName=%s:%s" % (_san_kind(host), host)],
        check=True, capture_output=True,
    )


def cert_sans(cert_path):
    """The SANs in a certificate file, as ("IP Address", "127.0.0.1") pairs."""
    try:
        return list(ssl._ssl._test_decode_cert(cert_path).get("subjectAltName", ()))
    except Exception:
        out = subprocess.run(["openssl", "x509", "-in", cert_path, "-noout", "-ext",
                              "subjectAltName"], capture_output=True, text=True)
        found = []
        for line in out.stdout.splitlines():
            for item in line.strip().split(","):
                item = item.strip()
                if item.startswith("IP Address:"):
                    found.append(("IP Address", item.split(":", 1)[1]))
                elif item.startswith("DNS:"):
                    found.append(("DNS", item.split(":", 1)[1]))
        return found


def spki_pin(cert_path):
    """sha256//<base64> over the certificate's public key, like curl's --pinnedpubkey."""
    try:
        from cryptography import x509
        from cryptography.hazmat.primitives import serialization
        with open(cert_path, "rb") as fh:
            cert = x509.load_pem_x509_certificate(fh.read())
        der = cert.public_key().public_bytes(serialization.Encoding.DER,
                                             serialization.PublicFormat.SubjectPublicKeyInfo)
    except ImportError:
        pub = subprocess.run(["openssl", "x509", "-in", cert_path, "-pubkey", "-noout"],
                             check=True, capture_output=True)
        der = subprocess.run(["openssl", "pkey", "-pubin", "-outform", "DER"],
                             input=pub.stdout, check=True, capture_output=True).stdout
    return "sha256//" + base64.b64encode(hashlib.sha256(der).digest()).decode()


def setup_tls(opts):
    """Returns (cert_path, key_path). Generates one if none was given."""
    if opts.tls_cert and opts.tls_key:
        log("TLS: using the certificate you gave me (%s)", opts.tls_cert)
        return opts.tls_cert, opts.tls_key
    if opts.tls_cert or opts.tls_key:
        sys.exit("--tls-cert and --tls-key go together")

    workdir = tempfile.mkdtemp(prefix="autoproxy-fake-agent-")
    os.chmod(workdir, 0o700)
    atexit.register(shutil.rmtree, workdir, True)
    cert_path = os.path.join(workdir, "cert.pem")
    key_path = os.path.join(workdir, "key.pem")

    try:
        import cryptography  # noqa: F401
        _generate_with_cryptography(opts.host, cert_path, key_path)
        how = "the cryptography module"
    except ImportError:
        _generate_with_openssl(opts.host, cert_path, key_path)
        how = "the openssl command"
    os.chmod(cert_path, 0o600)
    os.chmod(key_path, 0o600)

    # A certificate without the right SAN would fail in curl and browsers in a
    # way that looks like a plugin bug, so check it here and refuse to start.
    wanted = ("IP Address" if _san_kind(opts.host) == "IP" else "DNS", opts.host)
    sans = cert_sans(cert_path)
    if wanted not in sans:
        sys.exit("TLS: the generated certificate has no %s SAN for %s (found %s). "
                 "Not starting: nothing would be able to verify it."
                 % (wanted[0], opts.host, sans or "none"))

    with open(cert_path) as fh:
        pem = fh.read().strip()
    print()
    print("TLS: self-signed certificate generated with %s, valid 10 years." % how)
    print("     SAN: %s   files: %s (0600), key never printed" % (sans, workdir))
    print("     Pin for certificate pinning: %s" % spki_pin(cert_path))
    print("     Save the block below as agent.pem and use it as curl's --cacert,")
    print("     or paste it into the plugin as the certificate to trust.")
    print("-----8<----- certificate (public, safe to copy) -----8<-----")
    print(pem)
    print("----->8----- end of certificate ----->8-----")
    print(flush=True)
    return cert_path, key_path


# --------------------------------------------------------------------- main --

def parse_args(argv=None):
    parser = argparse.ArgumentParser(
        description="Fake Pelican Auto Proxy VPS agent for testing the plugin. Touches nothing real.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="Examples:\n"
               "  python3 fake-agent.py --port 7443 --token devtoken\n"
               "  python3 fake-agent.py --tls --lockout-seconds 2\n"
               "  python3 fake-agent.py --tls --advertise-ip 203.0.113.10 "
               "--simulate-handshake-after 30\n",
    )
    parser.add_argument("--host", default="127.0.0.1", help="address to bind (default: 127.0.0.1)")
    parser.add_argument("--port", type=int, default=7443, help="API port (default: 7443)")
    parser.add_argument("--token", default="devtoken",
                        help="bearer token every request must send (default: devtoken)")
    parser.add_argument("--tls", action="store_true",
                        help="serve HTTPS like the real agent does")
    parser.add_argument("--tls-cert", help="use this certificate instead of generating one")
    parser.add_argument("--tls-key", help="the private key for --tls-cert")
    parser.add_argument("--lockout-after", type=int, default=5,
                        help="bad tokens from one address before it is locked out (default: 5)")
    parser.add_argument("--lockout-seconds", type=int, default=900,
                        help="how long a lockout lasts (default: 900; use 2 in a test)")
    parser.add_argument("--simulate-handshake-after", type=int, default=10, metavar="N",
                        help="seconds a new peer reports no handshake before it reports an age; "
                             "-1 means it never connects (default: 10)")
    parser.add_argument("--wg-port", type=int, default=51820,
                        help="the WireGuard port it claims to listen on (default: 51820)")
    parser.add_argument("--wg-iface", default="wg0", help="tunnel interface name (default: wg0)")
    parser.add_argument("--advertise-ip", default=None,
                        help="the address put inside join codes (default: whatever --host is; "
                             "203.0.113.10 is a good fake public address)")
    parser.add_argument("--version-string", dest="version", default="fake-0.2.0",
                        help="what GET /v1/status reports as the agent version")
    opts = parser.parse_args(argv)
    if opts.advertise_ip is None:
        opts.advertise_ip = opts.host
    if opts.lockout_after < 1:
        parser.error("--lockout-after must be at least 1")
    return opts


def main():
    global AGENT
    opts = parse_args()
    AGENT = Agent(opts)

    scheme = "https" if opts.tls else "http"
    server = ThreadingHTTPServer((opts.host, opts.port), Handler)
    if opts.tls:
        cert_path, key_path = setup_tls(opts)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert_path, key_path)
        server.socket = context.wrap_socket(server.socket, server_side=True)

    log("fake Pelican Auto Proxy agent on %s://%s:%d", scheme, opts.host, opts.port)
    log("  token: %s", opts.token)
    log("  reserved ports (never forwarded): %s",
        ", ".join(str(p) for p in AGENT.reserved))
    log("  lockout: %d bad tokens -> %d s", opts.lockout_after, opts.lockout_seconds)
    log("  peers report their first handshake after %s",
        "never" if opts.simulate_handshake_after < 0 else "%d s" % opts.simulate_handshake_after)
    log("  join codes point clients at %s", AGENT.endpoint)
    log("  routes: %s", "  ".join(ROUTES_HELP))
    # Leave on SIGTERM the same way as on Ctrl-C, so the temporary key file is
    # cleaned up when a test script kills us.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    try:
        server.serve_forever()
    except (KeyboardInterrupt, SystemExit):
        log("stopping")
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
