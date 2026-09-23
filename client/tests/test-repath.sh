#!/usr/bin/env bash
# A tunnel stuck on one local port must recover when the client moves to a new
# one. Reproduces the failure seen on a live node: one UDP flow (the client's
# local port -> the VPS's WireGuard port) was dropped for 22 hours while every
# other flow between the same machines worked, and restarting the service kept
# the same port, so it never recovered.
#
# In a throwaway container (--network none, NET_ADMIN): a stand-in VPS
# interface and the client's autoproxy0 talk over loopback; an nft rule drops
# only packets from autoproxy0's current port. The handshake must fail first
# (otherwise the test proves nothing), then succeed after repath_tunnel, sourced
# unmodified from client/autoproxy-client.
#
# Needs Docker and the wireguard kernel module on the host.
# Usage: client/tests/test-repath.sh
#   BREAK=1 skips the repath call; the test must then fail.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="autoproxy-test-repath:local"

docker build -q -t "$image" - >/dev/null <<'EOF'
FROM debian:trixie-slim
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends wireguard-tools nftables iproute2 >/dev/null
EOF

docker run --rm --network none --cap-add NET_ADMIN -e BREAK="${BREAK:-0}" \
	-v "$here/client/autoproxy-client:/autoproxy-client:ro" "$image" bash -c '
set -euo pipefail
fail() { echo "FAIL: $*"; exit 1; }
hs_of() { wg show "$1" latest-handshakes | awk "{print \$2}"; }

AUTOPROXY_RENDER_ONLY=1
# shellcheck source=/dev/null
source /autoproxy-client

ip link set lo up
vk=$(wg genkey); ck=$(wg genkey)
vp=$(printf "%s" "$vk" | wg pubkey); cp=$(printf "%s" "$ck" | wg pubkey)
printf "%s" "$vk" >/tmp/vk; printf "%s" "$ck" >/tmp/ck

ip link add wgvps type wireguard
wg set wgvps private-key /tmp/vk listen-port 51820 peer "$cp" allowed-ips 10.66.66.3/32
ip addr add 10.66.66.1/24 dev wgvps; ip link set wgvps up

ip link add "$IFACE" type wireguard
wg set "$IFACE" private-key /tmp/ck peer "$vp" endpoint 127.0.0.1:51820 allowed-ips 0.0.0.0/0
ip addr add 10.66.66.3/32 dev "$IFACE"; ip link set "$IFACE" up

stuck=$(wg show "$IFACE" listen-port)
nft -f - <<NFT
table inet stuck {
    chain out { type filter hook output priority 0; udp sport $stuck udp dport 51820 drop; }
}
NFT
wg set "$IFACE" peer "$vp" persistent-keepalive 1

sleep 8
[[ "$(hs_of "$IFACE")" == 0 ]] || fail "handshake got through the blocked port; the path is not stuck, so this proves nothing"
echo "ok:   no handshake while stuck on local port $stuck"

VPS_WG_PUBKEY="$vp"; ENDPOINT="127.0.0.1:51820"
if [[ "$BREAK" != 1 ]]; then repath_tunnel 190; fi

for _ in $(seq 20); do [[ "$(hs_of "$IFACE")" != 0 ]] && break; sleep 1; done
[[ "$(hs_of "$IFACE")" != 0 ]] || fail "no handshake after moving to a new local port"
new=$(wg show "$IFACE" listen-port)
[[ "$new" != "$stuck" ]] || fail "local port did not change ($stuck)"
echo "ok:   handshake after moving to local port $new"

seen=$(wg show wgvps endpoints | awk "{print \$2}")
[[ "$seen" == "127.0.0.1:$new" ]] || fail "VPS side did not follow the new port (sees $seen)"
echo "ok:   VPS side follows the new port ($seen)"
echo "test-repath: all green"
'
