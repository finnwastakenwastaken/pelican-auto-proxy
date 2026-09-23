#!/bin/bash
# Bring the REAL autoproxy-agent up inside a throwaway container, then hold it
# there so a second container can talk to its API over the same netns.
#
# Runs INSIDE the container only. Never run this on a real host: it provisions
# nftables, WireGuard and systemd units.
#
# It writes what a caller needs into /shared:
#   vps-code   the base64url VPS code the plugin's Setup page would be given
#   agent.pem  the agent's TLS certificate
#   ready      created last, so a waiting container knows the API is listening
set -u

IP=203.0.113.10
SHARED=/shared
mkdir -p "$SHARED"

# --network none leaves only lo. Give it the documentation address and a
# default route so "ip route get" has something to answer.
ip addr add "$IP/32" dev lo 2>/dev/null
ip link set lo up
ip route add default dev lo 2>/dev/null

# systemd and apt do not run here. Everything else is the real thing.
mkdir -p /stub
cat > /stub/systemctl <<'EOS'
#!/bin/sh
echo "[stub systemctl] $*" >> /tmp/systemctl.log
case "$1" in is-active) echo active ;; esac
exit 0
EOS
cat > /stub/apt-get <<'EOS'
#!/bin/sh
echo "[stub apt-get] $*" >> /tmp/apt.log
exit 0
EOS
chmod +x /stub/systemctl /stub/apt-get
export PATH=/stub:$PATH

echo ">> setup"
if ! /agent setup --yes --public-ip "$IP" > /tmp/setup.log 2>&1; then
	echo "setup failed"; cat /tmp/setup.log; exit 1
fi

echo ">> wg0 up"
if ! wg-quick up wg0 > /tmp/wg.log 2>&1; then
	echo "wg-quick up wg0 failed"; cat /tmp/wg.log; exit 1
fi

cp /etc/autoproxy/vps-code "$SHARED/vps-code"
cp /etc/autoproxy/tls/agent.crt "$SHARED/agent.pem"

set -a
# shellcheck source=/dev/null
. /etc/autoproxy/agent.env
set +a

echo ">> agent run"
/agent run > /tmp/agent.log 2>&1 &

for _ in $(seq 1 60); do
	sleep 0.25
	if ss -lnt 2>/dev/null | grep -q ':7443'; then break; fi
done
if ! ss -lnt 2>/dev/null | grep -q ':7443'; then
	echo "agent is not listening"; cat /tmp/agent.log; exit 1
fi

# A real join code for each mode, straight out of the agent, for the client
# decoder to be pointed at. Written before "ready" so the probe and the client
# check see the same agent state.
TOKEN="$(sed -n 's/^AUTOPROXY_TOKEN=//p' /etc/autoproxy/agent.env)"
api() {
	curl -sS --cacert /etc/autoproxy/tls/agent.crt \
		-H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' "$@"
}
api -X POST -d '{"name":"joincode-real","mode":"real"}' \
	"https://$IP:7443/v1/peers" > /tmp/jc-real.json
api -X POST -d '{"name":"joincode-site","mode":"site","lan_cidrs":["192.168.40.0/24"]}' \
	"https://$IP:7443/v1/peers" > /tmp/jc-site.json
python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["join_code"])' /tmp/jc-real.json > "$SHARED/join-real"
python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["join_code"])' /tmp/jc-site.json > "$SHARED/join-site"

# --- tunnel check-in: the client's own function against the real agent -----
#
# The real-mode peer's tunnel address is put on lo, so a connection from it to
# the VPS's tunnel address reaches the agent exactly as one through WireGuard
# would: local address 10.66.66.1, remote address the peer's own. Results go
# to /shared/tunnel-checks for run.sh to print and count.
REAL_TIP="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["peer"]["tunnel_ip"])' /tmp/jc-real.json)"
REAL_ID="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["peer"]["id"])' /tmp/jc-real.json)"
ip addr add "$REAL_TIP/32" dev lo
ip addr add 10.66.66.200/32 dev lo
printf '%s\n' "$REAL_ID" > "$SHARED/checkin-peer"
checks="$SHARED/tunnel-checks"
: > "$checks"
tc() { if [ "$2" = "$3" ]; then echo "PASS  $1" >> "$checks"; else echo "FAIL  $1 (got '$2', want '$3')" >> "$checks"; fi; }

checkin_as() {
	# $1 = source address; prints the HTTP status the agent answered
	curl -sk -o /tmp/checkin-body -w '%{http_code}' --interface "$1" --max-time 5 \
		-H 'Content-Type: application/json' -d '{"version":"0.3.0","flavour":"systemd"}' \
		"https://10.66.66.1:7443/v1/tunnel/checkin"
}

# The client script's own check-in, loaded without running main().
client_rc=0
# shellcheck disable=SC2034  # read by the sourced client script
client_out="$(
	AUTOPROXY_RENDER_ONLY=1
	# shellcheck source=/dev/null
	. /client/autoproxy-client
	VPS_TUNNEL_IP=10.66.66.1 CLIENT_ADDRESS="$REAL_TIP/32" API_PORT=7443
	client_checkin
)" || client_rc=$?
tc "the real client's client_checkin() is answered 200 by the real agent" "$client_rc" "0"
tc "the answer carries poll_s" "$(printf '%s' "$client_out" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("poll_s"))' 2>/dev/null)" "120"
tc "a tunnel address that is not a peer gets 401" "$(checkin_as 10.66.66.200)" "401"
tc "the public address gets 401 without a token" "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 -d '{"version":"0.3.0"}' https://$IP:7443/v1/tunnel/checkin)" "401"
tc "the tunnel path with the token is not an API route (404)" "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 -H "Authorization: Bearer $TOKEN" -d '{}' https://$IP:7443/v1/tunnel/checkin)" "404"
tc "the agent rendered tunnel_guard into its own table at start" "$(nft list chain inet autoproxy_rules tunnel_guard >/dev/null 2>&1 && echo yes || echo no)" "yes"
tc "the base firewall table is untouched by the agent (no tunnel_guard there)" "$(nft list table inet autoproxy_base 2>/dev/null | grep -c tunnel_guard)" "0"
tc "the join code carries api_port" "$(python3 -c 'import json,sys,base64;c=json.load(open(sys.argv[1]))["join_code"];c+="="*(-len(c)%4);print(json.loads(base64.urlsafe_b64decode(c))["api_port"])' /tmp/jc-real.json)" "7443"
# The old-agent path: a client 0.3.0 talking to an agent without the route gets
# 401, which client_checkin must turn into "3" (back off), not an error loop.
old_rc=0
# shellcheck disable=SC2034  # read by the sourced client script
(
	AUTOPROXY_RENDER_ONLY=1
	# shellcheck source=/dev/null
	. /client/autoproxy-client
	VPS_TUNNEL_IP=10.66.66.1 CLIENT_ADDRESS="10.66.66.200/32" API_PORT=7443
	client_checkin >/dev/null
) || old_rc=$?
tc "client_checkin() reads a 401 as 'agent too old' (3), not as a failure to retry" "$old_rc" "3"
cat "$checks"

touch "$SHARED/ready"
echo ">> ready; API on https://$IP:7443"

# Hold the container open for the probe, then print the agent's own log so the
# request lines it recorded can be compared with what the probe reported.
for _ in $(seq 1 600); do
	sleep 1
	if [ -f "$SHARED/done" ]; then break; fi
done

echo ">> agent log"
cat /tmp/agent.log
