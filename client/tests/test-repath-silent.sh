#!/usr/bin/env bash
# A path that still passes handshakes but drops the traffic must recover too.
# Seen on a live node: the network dropped almost every packet of the node's
# flow towards the VPS, but the odd one got through, so the handshake kept
# renewing, the handshake-age repath never fired, and the tunnel carried
# nothing. The client's check-ins through the tunnel time out in that state;
# two timeouts in a row must move the tunnel to a new local port.
#
# Two throwaway containers from the real client image (NET_ADMIN, on an
# internal Docker network with no route out): a stand-in VPS with a stand-in
# check-in endpoint on its tunnel address, and a node, and an nft rule that drops only
# WireGuard data packets (message type 4) from the client's current port, so
# handshakes (types 1 and 2) still complete. Runs the real
# "autoproxy-client run" loop, unmodified; takes about three minutes.
#
# Needs Docker and the wireguard kernel module on the host.
# Usage: client/tests/test-repath-silent.sh
#   BREAK=1 runs a copy that never repaths on timeouts; the test must then fail.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="autoproxy-test-repath-silent:local"

docker build -q -t autoproxy-client:test-base "$here/client" >/dev/null
docker build -q -t "$image" - >/dev/null <<'EOF'
FROM autoproxy-client:test-base
RUN apk add --no-cache socat openssl >/dev/null
EOF

script="$here/client/autoproxy-client"
if [[ "${BREAK:-0}" == 1 ]]; then
	script="$(mktemp)"
	sed 's/^readonly CHECKIN_REPATH_AFTER=2$/readonly CHECKIN_REPATH_AFTER=99/' "$here/client/autoproxy-client" >"$script"
	grep -q 'CHECKIN_REPATH_AFTER=99' "$script" || { echo "BREAK: could not patch the copy"; exit 2; }
	chmod 0755 "$script"
fi

net="autoproxy-silent-$$"
cleanup() { docker rm -f "$net-vps" "$net-node" >/dev/null 2>&1 || true; docker network rm "$net" >/dev/null 2>&1 || true; }
trap cleanup EXIT
# Two machines: a stand-in VPS and a node, on an internal network (no route out).
docker network create --internal "$net" >/dev/null
docker run -d --name "$net-vps" --network "$net" --cap-add NET_ADMIN --entrypoint sleep "$image" infinity >/dev/null
docker run -d --name "$net-node" --network "$net" --cap-add NET_ADMIN --entrypoint sleep \
	-v "$script:/usr/local/bin/autoproxy-client:ro" "$image" infinity >/dev/null
vps_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}" "$net-vps")"
vps() { docker exec "$net-vps" bash -c "$1"; }
node() { docker exec "$net-node" bash -c "$1"; }
fail() { echo "FAIL: $*"; echo "--- client log"; node 'grep -vE "transfer:" /tmp/run.log | tail -20'; exit 1; }

ck="$(node 'wg genkey')"; cp="$(node "printf %s '$ck' | wg pubkey")"
vp="$(vps 'umask 077; wg genkey >/tmp/vk; wg pubkey </tmp/vk')"
vps "ip link add wg0 type wireguard && wg set wg0 private-key /tmp/vk listen-port 51820 peer $cp allowed-ips 10.66.66.3/32 \
	&& ip addr add 10.66.66.1/24 dev wg0 && ip link set wg0 up"
# Stand-in for the agent's check-in route: any HTTPS request gets 200 "{}".
vps 'printf "#!/bin/sh\nprintf \"HTTP/1.1 200 OK\\r\\nContent-Type: application/json\\r\\nContent-Length: 2\\r\\nConnection: close\\r\\n\\r\\n{}\"\n" >/tmp/resp.sh; chmod +x /tmp/resp.sh
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=t -days 1 -keyout /tmp/k.pem -out /tmp/c.pem >/dev/null 2>&1
	(socat OPENSSL-LISTEN:7443,bind=10.66.66.1,cert=/tmp/c.pem,key=/tmp/k.pem,verify=0,fork,reuseaddr EXEC:/tmp/resp.sh >/tmp/socat.log 2>&1 &)'

# Until the client has picked its port, nothing reaches the VPS port at all.
node 'nft -f - <<NFT
table inet silent {
    chain out {
        type filter hook output priority 0;
        udp dport 51820 drop
    }
}
NFT'
json="{\"v\":1,\"endpoint\":\"$vps_ip:51820\",\"vps_wg_pubkey\":\"$vp\",\"client_privkey\":\"$ck\",\"client_address\":\"10.66.66.3/32\",\"tunnel_subnet\":\"10.66.66.0/24\",\"vps_tunnel_ip\":\"10.66.66.1\",\"mode\":\"real\",\"lan_cidrs\":\"\",\"keepalive\":25,\"api_port\":7443}"
code="$(printf %s "$json" | base64 -w0 | tr "+/" "-_" | tr -d =)"
node "(AUTOPROXY_JOIN_CODE=$code timeout 330 autoproxy-client run >/tmp/run.log 2>&1 &)"

p=""
for _ in $(seq 50); do p="$(node 'wg show autoproxy0 listen-port 2>/dev/null' || true)"; [[ -n "$p" && "$p" != 0 ]] && break; sleep 0.2; done
[[ -n "$p" && "$p" != 0 ]] || fail "the client never brought autoproxy0 up"
# From its port: handshakes pass, data packets (first payload byte 4) do not.
node "nft flush chain inet silent out; nft add rule inet silent out udp sport $p udp dport 51820 @th,64,8 4 drop"
echo "ok:   client up on local port $p; its data packets are dropped, handshakes are not"

waitlog() { for _ in $(seq "$2"); do node "grep -q \"$1\" /tmp/run.log" && return 0; sleep 2; done; return 1; }
waitlog "reporting to the VPS agent over the tunnel timed out" 45 || fail "the check-in never timed out, so this proves nothing"
hs="$(node 'wg show autoproxy0 latest-handshakes | cut -f2')"
(( hs > 0 && $(date +%s) - hs < 180 )) || fail "the handshake is not fresh, so the handshake-age repath could be what fires"
echo "ok:   check-in timed out while the handshake is $(( $(date +%s) - hs ))s old"

waitlog "moved the tunnel to a new local port" 60 || fail "no repath after timed-out check-ins"
node 'grep -q "reports to the VPS in a row timed out although the handshake looks fresh: moved the tunnel" /tmp/run.log' \
	|| fail "the tunnel moved, but not because of the timed-out check-ins"
new="$(node 'wg show autoproxy0 listen-port')"
[[ "$new" != "$p" ]] || fail "local port did not change ($p)"
echo "ok:   moved from local port $p to $new after timed-out check-ins"

waitlog "reporting to the VPS agent works again" 60 || fail "check-ins did not recover on the new port"
echo "ok:   check-ins work again on the new port"
echo "test-repath-silent: all green"
