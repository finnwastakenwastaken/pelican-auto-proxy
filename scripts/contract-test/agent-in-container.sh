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
